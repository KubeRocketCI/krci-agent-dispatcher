// Package dispatcher runs issue lanes: one action at a time per issue, decided by package decide from facts it
// re-reads on each request, plus the activation loop and the restart recovery.
package dispatcher

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// Developer is the lowest GitLab access level of a start author and of a trigger comment author.
const Developer = 30

// RecoveryConcurrency is the number of issue lanes that run a recover request at the same time. An activation
// loop that a recover request continues or starts is not counted.
const RecoveryConcurrency = 8

const (
	actorPrefix    = "issue-"
	recheckPause   = 2 * time.Second
	writeTimeout   = 30 * time.Second
	statusTimeUTC  = time.RFC3339
	retryAttempts  = 3
	retryFirstWait = time.Second
	backoffMax     = time.Minute
)

// Config holds the dispatcher settings.
type Config struct {
	Atespace         string
	Pool             string
	SecretsNamespace string
	Image            string
	GitLabURL        string
	Bot              string
	TriggerLabel     string
	TurnLimit        int
	TurnTimeout      time.Duration
	PollInterval     time.Duration
	NewSessionID     func() string
	Logger           *slog.Logger
}

// Dispatcher serves requests on issue lanes.
type Dispatcher struct {
	gl     GitLab
	sb     Substrate
	clk    Clock
	cfg    Config
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

// New returns a dispatcher. Requests wait until Run starts.
func New(gl GitLab, sb Substrate, clk Clock, cfg Config) *Dispatcher {
	if cfg.NewSessionID == nil {
		cfg.NewSessionID = uuid.NewString
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Dispatcher{
		gl: gl, sb: sb, clk: clk, cfg: cfg, log: cfg.Logger,
		limits: decide.Limits{
			TurnLimit:    cfg.TurnLimit,
			Bot:          cfg.Bot,
			TriggerLabel: cfg.TriggerLabel,
			TurnTimeout:  cfg.TurnTimeout,
		},
		lanes:      map[models.IssueKey]*lane{},
		settling:   map[models.IssueKey]bool{},
		recovering: make(chan struct{}, RecoveryConcurrency),
	}
}

// Handle queues r on its issue lane and returns at once. Ignore requests are dropped.
func (d *Dispatcher) Handle(r intake.Request) {
	if r.Kind == intake.Ignore {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.enqueueLocked(r)
}

// Run runs the restart recovery once, then serves requests until ctx ends. It returns after every lane
// stopped. An activation cut by ctx leaves its actor running for the next restart recovery.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.recover(ctx)

	d.mu.Lock()
	d.ctx, d.started = ctx, true

	for key, l := range d.lanes {
		d.startLocked(key, l)
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
