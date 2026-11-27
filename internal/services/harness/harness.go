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

// postTimeout bounds one reply post and one read of the token user.
const postTimeout = 30 * time.Second

const (
	// identityTimeout bounds the retries of the token user read at start.
	identityTimeout = 5 * time.Minute
	// identityFirstWait is the first pause between two reads of the token user; it doubles up to identityMaxWait.
	identityFirstWait = 2 * time.Second
	identityMaxWait   = 30 * time.Second
)

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

	known     *models.IssueView
	started   bool
	mrPending bool
	pending   *models.PendingReply
	saved     bool
	// corrupt is set once a corrupt state is logged, until a load succeeds.
	corrupt bool
	// stopped is set when a reply was posted by another user than the bot user; no turn starts after it.
	stopped error
}

// New returns the harness for the result of Handshake.
func New(cfg Config, p Ports) *Harness {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}

	return &Harness{cfg: cfg, p: p, log: log}
}

// Run runs CheckIdentity, then polls until ctx ends: a Poll, then a sleep of the poll interval. It returns the error
// of ctx, the error of CheckIdentity, or a *HandshakeError when a posted reply has another author than the bot user.
func (h *Harness) Run(ctx context.Context) error {
	err := h.CheckIdentity(ctx)

	for err == nil {
		h.Poll(ctx)

		if err = h.stopped; err == nil {
			err = h.p.Clock.Sleep(ctx, h.cfg.Poll)
		}
	}

	return err
}

// CheckIdentity reads the token user and compares it with the bot user, BOT_USERNAME. Each read is bounded by
// postTimeout. A failed read is retried with backoff for identityTimeout; models.ErrNoIdentity, models.ErrUnauthorized,
// models.ErrForbidden and models.ErrIdentityChanged are not retried. It returns a *HandshakeError when the token user
// cannot be read or is another user, and the error of ctx when ctx ends first.
func (h *Harness) CheckIdentity(ctx context.Context) error {
	deadline := h.p.Clock.Now().Add(identityTimeout)

	for wait := identityFirstWait; ; wait = min(2*wait, identityMaxWait) {
		user, err := h.currentUser(ctx)

		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err == nil && user == h.cfg.Bot:
			return nil
		case err == nil:
			return &HandshakeError{Item: botItem, Reason: "is not the user of the GitLab token"}
		case permanentIdentityError(err) || h.p.Clock.Now().Add(wait).After(deadline):
			return &HandshakeError{Item: botItem, Reason: "cannot be checked: the GitLab token user cannot be read",
				Cause: err}
		}

		h.log.Warn("Cannot read the GitLab token user; retrying", "error", err, "in", wait.String())

		if serr := h.p.Clock.Sleep(ctx, wait); serr != nil {
			return serr
		}
	}
}

// currentUser is one read of the token user, bounded by postTimeout.
func (h *Harness) currentUser(ctx context.Context) (string, error) {
	cctx, cancel := h.p.Clock.WithDeadline(ctx, h.p.Clock.Now().Add(postTimeout))
	defer cancel()

	return h.p.GitLab.CurrentUser(cctx)
}

// permanentIdentityError reports whether a token user read failed for a reason a retry does not change.
func permanentIdentityError(err error) bool {
	return errors.Is(err, models.ErrNoIdentity) || errors.Is(err, models.ErrUnauthorized) ||
		errors.Is(err, models.ErrForbidden) || errors.Is(err, models.ErrIdentityChanged)
}

// Poll is one poll: it posts or drops a pending reply, else runs the next turn. After a reply of another author than
// the bot user it runs no turn.
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

	h.log.Info("Turn started", "turn", next.N)

	o := h.runTurn(ctx, start, next, hist, h.known.Issue)

	if ctx.Err() != nil {
		return
	}

	if o.exit != 0 {
		h.log.Warn("Turn failed", "turn", next.N, "phase", o.phase, "exit", o.exit, "error", o.err)
	} else {
		h.log.Info("Turn done", "turn", next.N)
	}

	h.pending, h.saved = &models.PendingReply{Turn: next.N, Body: o.body(h.cfg.Session, next)}, false

	h.deliver(ctx)
}

// startable returns the next turn when it can start: it has a comment to cover, is within the turn limit and no
// reply of another author than the bot user was posted.
func (h *Harness) startable(hist turn.History) (turn.Turn, bool) {
	next := hist.Next()

	return next, next.Ready && !hist.AtLimit(h.cfg.TurnLimit) && h.stopped == nil
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
	h.mrPending = h.mrPending || st.MergeRequestPending

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

	author, err := h.post(ctx, h.pending.Body)
	if err != nil {
		h.log.Warn("Cannot post the reply; retrying on the next poll", "turn", h.pending.Turn, "error", err)

		return
	}

	h.clearPending()

	if author != h.cfg.Bot {
		h.stopped = &HandshakeError{Item: botItem, Reason: "is not the author of the posted reply"}
		h.log.Error("Reply posted by another user than the bot user; no more turns", "author", author)
	}
}

// post is bounded by postTimeout, independent of the watchdog context. It returns the author of the posted note.
func (h *Harness) post(ctx context.Context, body string) (string, error) {
	pctx, cancel := h.p.Clock.WithDeadline(ctx, h.p.Clock.Now().Add(postTimeout))
	defer cancel()

	n, err := h.p.GitLab.PostReply(pctx, h.cfg.Key, body)

	return n.Author, err
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
	return models.HarnessState{Pending: h.pending, Started: h.started, MergeRequestPending: h.mrPending}
}
