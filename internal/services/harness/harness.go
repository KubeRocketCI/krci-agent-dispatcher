// Package harness is the agent loop that runs in every actor: the handshake, one poll of the issue notes per
// Step, the turn under the watchdog and the reply.
package harness

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

// postTimeout bounds one reply post.
const postTimeout = 30 * time.Second

// Getenv reads one env item; an unset item is empty.
type Getenv func(key string) string

// Ports are the adapters of a Harness. Log defaults to slog.Default().
type Ports struct {
	Notes     Notes
	Agent     Agent
	Workspace Workspace
	Clock     Clock
	State     StateStore
	Log       *slog.Logger
}

// Harness runs the turns of one issue in one session.
type Harness struct {
	cfg Config
	p   Ports
	log *slog.Logger

	known   *models.IssueView
	started bool
	pending *models.PendingReply
	saved   bool
}

// New runs the handshake and returns the harness. The error is a *HandshakeError.
func New(getenv Getenv, p Ports) (*Harness, error) {
	cfg, err := Handshake(getenv)
	if err != nil {
		return nil, err
	}

	log := p.Log
	if log == nil {
		log = slog.Default()
	}

	return &Harness{cfg: cfg, p: p, log: log}, nil
}

// Run polls until ctx ends: a Step, then a sleep of the poll interval. It returns the error of ctx.
func (h *Harness) Run(ctx context.Context) error {
	for {
		h.Step(ctx)

		if err := h.p.Clock.Sleep(ctx, h.cfg.Poll); err != nil {
			return err
		}
	}
}

// Step is one poll: it posts or drops a pending reply, else runs the next turn.
func (h *Harness) Step(ctx context.Context) {
	start := h.p.Clock.Now()

	if !h.recall() {
		return
	}

	hist, ok := h.history(ctx)
	if !ok {
		return
	}

	if h.pending != nil {
		if !hist.HasTurn(h.pending.Turn) {
			h.deliver(ctx)

			return
		}

		h.clearPending()
	}

	next, ok := h.startable(hist)
	if !ok {
		return
	}

	o := h.turn(ctx, start, next)

	if ctx.Err() != nil {
		return
	}

	h.pending, h.saved = &models.PendingReply{Turn: next.N, Body: o.body(h.cfg.Session, next)}, false

	h.deliver(ctx)
}

// startable returns the next turn when it can start: it has a comment to cover and is within the turn limit.
func (h *Harness) startable(hist turn.History) (turn.Turn, bool) {
	next := hist.Next()

	return next, next.Start && !hist.AtLimit(h.cfg.TurnLimit)
}

// history reads the issue notes: the first poll in full, later polls with Refresh. A Refresh result with
// something to act on is confirmed with a full read, because Refresh keeps notes deleted since the last read. A
// failed read ends the Step; the next poll retries it.
func (h *Harness) history(ctx context.Context) (turn.History, bool) {
	full := h.known == nil

	view, err := h.read(ctx, full)
	if err != nil {
		h.log.Warn("Cannot read the issue notes", "error", err)

		return turn.History{}, false
	}

	hist := turn.NewHistory(view.Notes, h.cfg.Bot, h.cfg.Session)

	if _, startable := h.startable(hist); !full && (startable || h.pending != nil) {
		if view, err = h.read(ctx, true); err != nil {
			h.log.Warn("Cannot read the issue notes", "error", err)

			return turn.History{}, false
		}

		hist = turn.NewHistory(view.Notes, h.cfg.Bot, h.cfg.Session)
	}

	h.known = &view

	return hist, true
}

func (h *Harness) read(ctx context.Context, full bool) (models.IssueView, error) {
	if full {
		return h.p.Notes.Load(ctx, h.cfg.Key)
	}

	return h.p.Notes.Refresh(ctx, *h.known)
}

// recall takes the pending reply and the session-started flag from the store when memory holds no pending reply.
// A store that cannot be read ends the Step: a turn would repeat one whose reply may be pending. A corrupt state is
// no state; the next save replaces it.
func (h *Harness) recall() bool {
	if h.pending != nil {
		return true
	}

	st, err := h.p.State.Load(h.cfg.Session)

	switch {
	case errors.Is(err, models.ErrStateCorrupt):
		h.log.Error("Harness state is corrupt; starting without it", "error", err)
	case err != nil:
		h.log.Error("Cannot read the harness state", "error", err)

		return false
	}

	h.pending, h.saved = st.Pending, st.Pending != nil
	h.started = h.started || st.Started

	return true
}

// deliver saves the pending reply, then posts it. A failed save or post leaves it pending for the next Step.
func (h *Harness) deliver(ctx context.Context) {
	if !h.saved {
		if err := h.p.State.Save(h.cfg.Session, h.state()); err != nil {
			h.log.Error("Cannot save the pending reply", "error", err)

			return
		}

		h.saved = true
	}

	if err := h.post(ctx, h.pending.Body); err != nil {
		h.log.Warn("Cannot post the reply; retrying on the next poll", "turn", h.pending.Turn, "error", err)

		return
	}

	h.clearPending()
}

// post posts body under a context of its own, bounded by postTimeout: a turn cut by the watchdog still replies.
func (h *Harness) post(ctx context.Context, body string) error {
	pctx, cancel := h.p.Clock.WithDeadline(ctx, h.p.Clock.Now().Add(postTimeout))
	defer cancel()

	_, err := h.p.Notes.PostReply(pctx, h.cfg.Key, body)

	return err
}

// clearPending forgets the pending reply in memory and in the store. A failed store write leaves the stored reply
// for the next Step, which drops it again.
func (h *Harness) clearPending() {
	h.pending, h.saved = nil, false

	if err := h.p.State.Save(h.cfg.Session, h.state()); err != nil {
		h.log.Error("Cannot clear the pending reply", "error", err)
	}
}

func (h *Harness) state() models.HarnessState {
	return models.HarnessState{Pending: h.pending, Started: h.started}
}
