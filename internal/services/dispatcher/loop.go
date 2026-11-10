package dispatcher

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
)

// loop is the activation loop: every PollInterval it reads the issue and the actor state and applies
// decide.Poll. The first read is a full one; later polls read only the notes created or edited since. A
// decision other than wait is taken again on a full read, made at once, before it is executed. since is the note
// id of the turn's previous marker; recovered marks the loop of a running actor found by restart recovery. A
// cancelled ctx ends the loop without a change, so the next restart recovery continues it. While reads fail, the
// turn timeout still ends the activation.
func (d *Dispatcher) loop(ctx context.Context, log *slog.Logger, key models.IssueKey, since int64, recovered bool) {
	d.releaseRecovery(key)

	name := key.ActorName()
	turnStart := d.clk.Now()
	first := true

	var known *models.IssueView

	for full, pause := true, true; ; {
		if pause && d.clk.Sleep(ctx, d.cfg.PollInterval) != nil {
			log.Info("Activation left running for restart recovery", "actor", name)
			return
		}

		pause = true

		view, err := d.read(ctx, key, known, full)
		if errors.Is(err, gitlab.ErrNotFound) {
			d.issueGone(ctx, log, key)
			return
		}

		if err != nil {
			log.Warn("Poll: cannot read the issue", "error", err)

			if d.timedOut(ctx, log, key, turnStart) {
				return
			}

			continue
		}

		known = &view
		confirmed := full
		full = false

		actor, err := d.sb.Inspect(ctx, name)
		if err != nil {
			log.Warn("Poll: cannot read the actor", "error", err)

			if d.timedOut(ctx, log, key, turnStart) {
				return
			}

			continue
		}

		pd := decide.Poll(decide.PollFacts{
			View: view, Actor: actor, Since: since, Elapsed: d.clk.Now().Sub(turnStart), StopPending: d.stopPending(key),
		}, d.limits)

		if pd.Action != decide.Wait && !confirmed {
			full, pause = true, false

			continue
		}

		switch pd.Action {
		case decide.Wait:
			first = false

			continue
		case decide.StayResumed:
			if recovered && first {
				log.Info("Recovered activation; waiting for the next marker", "after_turn", pd.Marker.Turn)
			} else {
				log.Info("Turn ended; comments after it, staying resumed", "turn", pd.Marker.Turn)
			}

			since, turnStart, first = pd.Marker.NoteID, d.clk.Now(), false

			continue
		case decide.Suspend:
			if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) }); err != nil {
				d.failRPC(ctx, log, key, view, actor, "Cannot suspend the actor", err)

				return
			}

			suspended, _ := d.sb.Inspect(ctx, name)
			d.write(ctx, log, key, view, suspended, pd.Writes)
		case decide.End:
			d.write(ctx, log, key, view, actor, pd.Writes)
		case decide.StopNow:
			d.stop(ctx, log, key, view, actor)
		}

		log.Info("Activation ended", "rule", string(pd.Rule), "turn", pd.Marker.Turn)

		return
	}
}

// read returns the full view of the issue, or known with what changed since when full is false.
func (d *Dispatcher) read(ctx context.Context, key models.IssueKey, known *models.IssueView, full bool,
) (models.IssueView, error) {
	if full || known == nil {
		return d.gl.Load(ctx, key)
	}

	return d.gl.Refresh(ctx, *known)
}

// timedOut ends an activation whose reads fail past the turn timeout: best-effort suspend, then record turn
// timeout with status rows from a fresh read; the status note keeps its rows when that read fails. It reports
// whether the loop must end.
func (d *Dispatcher) timedOut(ctx context.Context, log *slog.Logger, key models.IssueKey, turnStart time.Time) bool {
	if ctx.Err() != nil || d.clk.Now().Sub(turnStart) < d.cfg.TurnTimeout {
		return ctx.Err() != nil
	}

	log.Error("Turn timeout while reads fail; suspending", "actor", key.ActorName())

	if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, key.ActorName()) }); err != nil {
		log.Error("Cannot suspend the actor", "error", err)
	}

	d.writeFresh(ctx, log, key, stateWrites(models.Error, models.ReasonTurnTimeout))

	return true
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
