package dispatcher

import (
	"context"
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
// decision other than wait is taken again on a full read, made at once, before it is performed. since is the note
// id of the turn's previous marker; recovered marks the loop of a running actor found by restart recovery. A
// cancelled ctx ends the loop without a change, so the next restart recovery continues it.
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

// poll reads the issue and the actor once, then performs the plan of decide.Poll made on a full read. A read
// cut by the end of ctx ends the loop without a change.
func (d *Dispatcher) poll(ctx context.Context, log *slog.Logger, key models.IssueKey, a *activation) next {
	confirmed := a.full
	f := decide.PollFacts{Since: a.since, Elapsed: d.clk.Now().Sub(a.start), StopPending: d.stopPending(key)}

	view, err := d.read(ctx, key, a)
	if err == nil {
		a.known, a.full = &view, false
		f.View = view

		if f.Actor, err = d.sb.Inspect(ctx, key.ActorName()); err != nil {
			log.Warn("Poll: cannot read the actor", "error", err)

			f.Actor = models.ActorView{State: models.Unknown}
		}
	} else {
		log.Warn("Poll: cannot read the issue", "error", err)

		f.Issue = d.issueRead(ctx, key.ProjectID, err)
	}

	if ctx.Err() != nil {
		return pollEnd
	}

	pd := decide.Poll(f, d.limits)

	if !pd.Waits() && !confirmed {
		a.full = true
		return pollNow
	}

	d.perform(ctx, log, key, f.View, f.Actor, a, pd.Plan)

	if pd.Next == decide.NextContinue {
		a.first = false
		return pollLater
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
