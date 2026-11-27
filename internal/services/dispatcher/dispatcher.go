// Package dispatcher runs issue lanes: one action at a time per issue, decided by package decide from facts it
// re-reads on each request, plus the activation loop, the restart recovery and the sweep.
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// RecoveryConcurrency is the number of issue lanes that run a recover request at the same time. An activation
// loop that a recover request continues or starts is not counted.
const RecoveryConcurrency = 8

// writeTimeout bounds one GitLab write with its retries, detached from ctx. It leaves room for a retry after one
// GitLab request timeout (30 s) when enough remains; not for every attempt.
const writeTimeout = 60 * time.Second

const (
	actorPrefix    = "issue-"
	recheckPause   = 2 * time.Second
	statusTimeUTC  = time.RFC3339
	retryAttempts  = 3
	retryFirstWait = time.Second
	backoffMax     = time.Minute
)

// Config holds the dispatcher settings.
type Config struct {
	Labels        models.AgentLabels
	TriggerRole   models.Role
	TurnLimit     int
	TurnTimeout   time.Duration
	PollInterval  time.Duration
	SweepInterval time.Duration
	NewSessionID  func() string
	Logger        *slog.Logger
}

// Dispatcher serves requests on issue lanes.
type Dispatcher struct {
	gl     GitLab
	sb     Substrate
	pr     Profiles
	clk    Clock
	cfg    Config
	bot    string
	limits decide.Limits
	log    *slog.Logger

	recovering chan struct{}

	mu       sync.Mutex
	lanes    map[models.IssueKey]*lane
	settling map[models.IssueKey]bool
	ctx      context.Context
	started  bool
	stopping bool
	wg       sync.WaitGroup
}

// New reads the bot user with gl.CurrentUser and returns a dispatcher that acts as it. A failed read is retried with
// backoff until ctx ends; an empty username, models.ErrNoIdentity, models.ErrUnauthorized, models.ErrForbidden and
// models.ErrIdentityChanged are an error at once. When ctx ends first, the error wraps the error of ctx. Requests
// wait until Run starts.
func New(ctx context.Context, gl GitLab, sb Substrate, pr Profiles, clk Clock, cfg Config) (*Dispatcher, error) {
	cfg = cfg.withDefaults()

	bot, err := botUser(ctx, gl, clk, cfg.Logger)
	if err != nil {
		return nil, err
	}

	return &Dispatcher{
		gl: gl, sb: sb, pr: pr, clk: clk, cfg: cfg, bot: bot, log: cfg.Logger,
		limits: decide.Limits{
			TurnLimit:   cfg.TurnLimit,
			Bot:         bot,
			Labels:      cfg.Labels,
			TriggerRole: cfg.TriggerRole,
			TurnTimeout: cfg.TurnTimeout,
		},
		lanes:      map[models.IssueKey]*lane{},
		settling:   map[models.IssueKey]bool{},
		recovering: make(chan struct{}, RecoveryConcurrency),
	}, nil
}

// botUser reads the bot user with the retries of New; an empty username is models.ErrNoIdentity.
func botUser(ctx context.Context, gl GitLab, clk Clock, log *slog.Logger) (string, error) {
	for wait := retryFirstWait; ; wait = min(2*wait, backoffMax) {
		bot, err := gl.CurrentUser(ctx)
		if err == nil && bot == "" {
			err = models.ErrNoIdentity
		}

		switch {
		case err == nil:
			return bot, nil
		case ctx.Err() != nil:
			return "", fmt.Errorf("read the bot user, the user of the GitLab token: %w", ctx.Err())
		case permanentIdentityError(err):
			return "", fmt.Errorf("read the bot user, the user of the GitLab token: %w", err)
		}

		log.Error("Cannot read the bot user, the user of the GitLab token; retrying", "error", err, "in", wait.String())

		if serr := clk.Sleep(ctx, wait); serr != nil {
			return "", fmt.Errorf("read the bot user, the user of the GitLab token: %w", serr)
		}
	}
}

// permanentIdentityError reports whether a bot user read failed for a reason a retry does not change.
func permanentIdentityError(err error) bool {
	return errors.Is(err, models.ErrNoIdentity) || errors.Is(err, models.ErrUnauthorized) ||
		errors.Is(err, models.ErrForbidden) || errors.Is(err, models.ErrIdentityChanged)
}

// withDefaults returns c with the defaults of its unset fields: a UUID session id, slog.Default(), the trigger role
// developer.
func (c Config) withDefaults() Config {
	if c.NewSessionID == nil {
		c.NewSessionID = uuid.NewString
	}

	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	if c.TriggerRole == 0 {
		c.TriggerRole = models.Developer
	}

	return c
}

// Bot returns the bot user read by New.
func (d *Dispatcher) Bot() string { return d.bot }

// Handle queues r on its issue lane and returns at once. Ignore requests are dropped.
func (d *Dispatcher) Handle(r intake.Request) {
	if r.Kind == intake.Ignore {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.enqueueLocked(r)
}

// Run runs the restart recovery once, then serves requests and, when SweepInterval is above 0, the sweep until ctx
// ends. It returns after every lane and the sweep stopped. An activation cut by ctx leaves its actor running for
// the next restart recovery.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.recover(ctx)

	d.mu.Lock()
	d.ctx, d.started = ctx, true

	for key, l := range d.lanes {
		d.startLocked(key, l)
	}

	if d.cfg.SweepInterval > 0 {
		d.wg.Add(1)

		go d.sweepLoop(ctx)
	}
	d.mu.Unlock()

	<-ctx.Done()

	d.mu.Lock()
	d.stopping = true
	d.mu.Unlock()

	d.wg.Wait()

	return nil
}

// Idle reports whether Run serves requests, no lane runs and no request waits.
func (d *Dispatcher) Idle() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.started && len(d.lanes) == 0
}
