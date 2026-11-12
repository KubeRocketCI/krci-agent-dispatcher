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
// previous marker; start is the current turn start; first is true until a poll that read the issue and the actor
// waits, or a turn ends; recovered
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

	f, ok := d.observePoll(ctx, log, key, a)
	if !ok {
		return pollEnd
	}

	pd := decide.Poll(f, d.limits)

	if !pd.Waits() && !confirmed {
		a.full = true
		return pollNow
	}

	if pd.Rule == decide.PollReadTimeout {
		log.Error("Turn timeout while reads fail; suspending", "actor", key.ActorName())
	}

	d.perform(ctx, log, key, f.View, f.Actor, a, pd.Plan)

	if pd.Next == decide.NextContinue {
		a.first = a.first && (f.Issue != decide.ReadOK || f.Actor.State == models.Unknown)
		return pollLater
	}

	log.Info("Activation ended", "rule", string(pd.Rule), "turn", pd.Marker.Turn)

	return pollEnd
}
