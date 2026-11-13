// Package dispatcher runs issue lanes: one action at a time per issue, decided by package decide from facts it
// re-reads on each request, plus the activation loop, the restart recovery and the sweep.
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
	Bot           string
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

	if cfg.TriggerRole == 0 {
		cfg.TriggerRole = models.Developer
	}

	return &Dispatcher{
		gl: gl, sb: sb, clk: clk, cfg: cfg, log: cfg.Logger,
		limits: decide.Limits{
			TurnLimit:   cfg.TurnLimit,
			Bot:         cfg.Bot,
			Labels:      cfg.Labels,
			TriggerRole: cfg.TriggerRole,
			TurnTimeout: cfg.TurnTimeout,
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
