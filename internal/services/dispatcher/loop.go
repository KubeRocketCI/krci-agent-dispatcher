package dispatcher

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
)

// next is what the activation loop does after one poll.
type next int

const (
	pollLater next = iota // after PollInterval
	pollNow               // at once, with a full read
	pollEnd               // the loop ends
)

// activation is the activation loop state across polls and turns. since is the note id of the current turn's
// previous marker; start is the current turn start; first is true until a poll waits or a turn ends; recovered
// marks the loop of a running actor found by restart recovery; full asks for a full read; known is the last view
// read.
type activation struct {
	since     int64
	start     time.Time
	first     bool
	recovered bool
	full      bool
	known     *models.IssueView
}

// loop is the activation loop: every PollInterval it reads the issue and the actor state and applies
// decide.Poll. The first read is a full one; later polls read only the notes created or edited since. A
// decision other than wait is taken again on a full read, made at once, before it is executed. since is the note
// id of the turn's previous marker; recovered marks the loop of a running actor found by restart recovery. A
// cancelled ctx ends the loop without a change, so the next restart recovery continues it. While reads fail, the
// turn timeout still ends the activation.
func (d *Dispatcher) loop(ctx context.Context, log *slog.Logger, key models.IssueKey, since int64, recovered bool) {
	d.releaseRecovery(key)

	a := &activation{since: since, start: d.clk.Now(), first: true, recovered: recovered, full: true}

	for n := pollLater; n != pollEnd; n = d.poll(ctx, log, key, a) {
		if n == pollLater && d.clk.Sleep(ctx, d.cfg.PollInterval) != nil {
			log.Info("Activation left running for restart recovery", "actor", key.ActorName())
			return
		}
	}
}

// poll reads the issue and the actor once and applies decide.Poll.
func (d *Dispatcher) poll(ctx context.Context, log *slog.Logger, key models.IssueKey, a *activation) next {
	view, err := d.read(ctx, key, a)
	if errors.Is(err, models.ErrNotFound) {
		d.settleUnread(ctx, log, key, err)
		return pollEnd
	}

	if err != nil {
		log.Warn("Poll: cannot read the issue", "error", err)
		return d.timedOut(ctx, log, key, a)
	}

	a.known = &view
	confirmed := a.full
	a.full = false

	actor, err := d.sb.Inspect(ctx, key.ActorName())
	if err != nil {
		log.Warn("Poll: cannot read the actor", "error", err)
		return d.timedOut(ctx, log, key, a)
	}

	pd := decide.Poll(decide.PollFacts{
		View: view, Actor: actor, Since: a.since, Elapsed: d.clk.Now().Sub(a.start), StopPending: d.stopPending(key),
	}, d.limits)

	if pd.Action != decide.Wait && !confirmed {
		a.full = true
		return pollNow
	}

	return d.applyPoll(ctx, log, key, a, view, actor, pd)
}

// applyPoll executes a poll decision made on a full read.
func (d *Dispatcher) applyPoll(ctx context.Context, log *slog.Logger, key models.IssueKey, a *activation,
	view models.IssueView, actor models.ActorView, pd decide.PollDecision,
) next {
	switch pd.Action {
	case decide.Wait:
		a.first = false

		return pollLater
	case decide.StayResumed:
		if a.recovered && a.first {
			log.Info("Recovered activation; waiting for the next marker", "after_turn", pd.Marker.Turn)
		} else {
			log.Info("Turn ended; comments after it, staying resumed", "turn", pd.Marker.Turn)
		}

		a.since, a.start, a.first = pd.Marker.NoteID, d.clk.Now(), false

		return pollLater
	case decide.Suspend:
		name := key.ActorName()

		if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) }); err != nil {
			d.failRPC(ctx, log, key, view, actor, "Cannot suspend the actor", err)

			return pollEnd
		}

		suspended, err := d.sb.Inspect(ctx, name)
		if err != nil {
			log.Warn("Cannot read the suspended actor for the status rows; rows kept", "error", err)
			d.put(ctx, log, key, &view, pd.Writes)

			break
		}

		d.write(ctx, log, key, view, suspended, pd.Writes)
	case decide.End:
		d.write(ctx, log, key, view, actor, pd.Writes)
	case decide.StopNow:
		d.stop(ctx, log, key, view, actor)
	}

	log.Info("Activation ended", "rule", string(pd.Rule), "turn", pd.Marker.Turn)

	return pollEnd
}

// read returns the full view of the issue, or the last view with what changed since when no full read is due.
func (d *Dispatcher) read(ctx context.Context, key models.IssueKey, a *activation) (models.IssueView, error) {
	if a.full || a.known == nil {
		return d.gl.Load(ctx, key)
	}

	return d.gl.Refresh(ctx, *a.known)
}

// timedOut runs after a failed read. Past the turn timeout it ends the activation: best-effort suspend, then
// record turn timeout with status rows from a fresh read; the status note keeps its rows when that read fails.
// It returns pollEnd when the activation ended or ctx ended, pollLater otherwise.
func (d *Dispatcher) timedOut(ctx context.Context, log *slog.Logger, key models.IssueKey, a *activation) next {
	if ctx.Err() != nil {
		return pollEnd
	}

	if d.clk.Now().Sub(a.start) < d.cfg.TurnTimeout {
		return pollLater
	}

	log.Error("Turn timeout while reads fail; suspending", "actor", key.ActorName())

	if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, key.ActorName()) }); err != nil {
		log.Error("Cannot suspend the actor", "error", err)
	}

	d.writeFresh(ctx, log, key, stateWrites(models.Error, models.ReasonTurnTimeout))

	return pollEnd
}

// continueSince returns the note id of the marker before the last one (0 if none): the loop of a running actor
// found after a dispatcher restart then sees the last marker as new.
func continueSince(notes []models.Note, bot string) int64 {
	last, ok := models.LastMarker(notes, bot)
	if !ok {
		return 0
	}

	var prev int64

	for _, n := range notes {
		if n.ID < last.NoteID {
			if m, ok := models.MarkerAfter([]models.Note{n}, bot, 0); ok {
				prev = max(prev, m.NoteID)
			}
		}
	}

	return prev
}
