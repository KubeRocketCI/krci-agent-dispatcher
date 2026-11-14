// Package harness is the agent loop that runs in every actor: the handshake, one poll of the issue notes per
// Poll, the turn under the watchdog and the reply.
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
	GitLab    GitLab
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
	// corrupt is set once a corrupt state is logged, until a load succeeds.
	corrupt bool
}

// New returns the harness for the result of Handshake.
func New(cfg Config, p Ports) *Harness {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}

	return &Harness{cfg: cfg, p: p, log: log}
}

// Run polls until ctx ends: a Poll, then a sleep of the poll interval. It returns the error of ctx.
func (h *Harness) Run(ctx context.Context) error {
	for {
		h.Poll(ctx)

		if err := h.p.Clock.Sleep(ctx, h.cfg.Poll); err != nil {
			return err
		}
	}
}

// Poll is one poll: it posts or drops a pending reply, else runs the next turn.
func (h *Harness) Poll(ctx context.Context) {
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

	o := h.runTurn(ctx, start, next, h.known.Issue)

	if ctx.Err() != nil {
		return
	}

	h.pending, h.saved = &models.PendingReply{Turn: next.N, Body: o.body(h.cfg.Session, next)}, false

	h.deliver(ctx)
}

// startable returns the next turn when it can start: it has a comment to cover and is within the turn limit.
func (h *Harness) startable(hist turn.History) (turn.Turn, bool) {
	next := hist.Next()

	return next, next.Ready && !hist.AtLimit(h.cfg.TurnLimit)
}

// history reads the issue notes: in full on the first poll and while a reply is pending, else with Refresh. A
// Refresh result with a turn that can start is confirmed with a full read, because Refresh keeps notes deleted since
// the last read. A failed read ends the poll; the next poll retries it.
func (h *Harness) history(ctx context.Context) (turn.History, bool) {
	full := h.known == nil || h.pending != nil

	view, hist, ok := h.readHistory(ctx, full)
	if !ok {
		return turn.History{}, false
	}

	if _, startable := h.startable(hist); !full && startable {
		if view, hist, ok = h.readHistory(ctx, true); !ok {
			return turn.History{}, false
		}
	}

	h.known = &view

	return hist, true
}

func (h *Harness) readHistory(ctx context.Context, full bool) (models.IssueView, turn.History, bool) {
	view, err := h.read(ctx, full)
	if err != nil {
		h.log.Warn("Cannot read the issue notes", "error", err)

		return models.IssueView{}, turn.History{}, false
	}

	return view, turn.NewHistory(view.Notes, h.cfg.Bot, h.cfg.Session), true
}

func (h *Harness) read(ctx context.Context, full bool) (models.IssueView, error) {
	if full {
		return h.p.GitLab.Load(ctx, h.cfg.Key)
	}

	return h.p.GitLab.Refresh(ctx, *h.known)
}

// recall takes the pending reply and the session-started flag from the store when memory holds no pending reply.
// A store that cannot be read ends the poll: a turn would repeat one whose reply may be pending. A corrupt state is
// no state, logged once; the next save replaces it.
func (h *Harness) recall() bool {
	if h.pending != nil {
		return true
	}

	st, err := h.p.State.Load(h.cfg.Session)

	switch {
	case errors.Is(err, models.ErrStateCorrupt):
		if !h.corrupt {
			h.log.Error("Harness state is corrupt; starting without it", "error", err)
		}

		h.corrupt = true
	case err != nil:
		h.log.Error("Cannot read the harness state", "error", err)

		return false
	default:
		h.corrupt = false
	}

	h.pending, h.saved = st.Pending, st.Pending != nil
	h.started = h.started || st.Started

	return true
}

// deliver saves the pending reply, then posts it. A failed save or post leaves it pending for the next poll.
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

// post is bounded by postTimeout, independent of the watchdog context.
func (h *Harness) post(ctx context.Context, body string) error {
	pctx, cancel := h.p.Clock.WithDeadline(ctx, h.p.Clock.Now().Add(postTimeout))
	defer cancel()

	_, err := h.p.GitLab.PostReply(pctx, h.cfg.Key, body)

	return err
}

// clearPending forgets the pending reply in memory and in the store. A failed store write leaves the stored reply
// for the next poll, which drops it again.
func (h *Harness) clearPending() {
	h.pending, h.saved = nil, false

	if err := h.p.State.Save(h.cfg.Session, h.state()); err != nil {
		h.log.Error("Cannot clear the pending reply", "error", err)
	}
}

func (h *Harness) state() models.HarnessState {
	return models.HarnessState{Pending: h.pending, Started: h.started}
}
