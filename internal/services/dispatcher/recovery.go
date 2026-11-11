package dispatcher

import (
	"context"
	"fmt"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// recover queues a recover request on the lane of every issue actor in the atespace. The actor list
// is retried with backoff until it succeeds or ctx ends. Requests that arrive meanwhile wait as pending.
// RecoveryConcurrency lanes run their recover request at a time.
func (d *Dispatcher) recover(ctx context.Context) {
	var listed []models.ActorSummary

	for wait := retryFirstWait; ; wait = min(2*wait, backoffMax) {
		var err error

		if listed, err = d.sb.List(ctx, actorPrefix); err == nil {
			break
		}

		d.log.Error("Restart recovery: cannot list actors; retrying", "error", err, "in", wait.String())

		if d.clk.Sleep(ctx, wait) != nil {
			return
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	actors := d.issueActors(listed, "Restart recovery")
	for _, a := range actors {
		d.enqueueLocked(intake.Request{Key: a.key, Kind: intake.Recover})
	}

	d.log.Info("Restart recovery queued", "actors", len(actors))
}

// sweepLoop runs a sweep every SweepInterval until ctx ends.
func (d *Dispatcher) sweepLoop(ctx context.Context) {
	defer d.wg.Done()

	var seen map[models.IssueKey]bool

	for d.clk.Sleep(ctx, d.cfg.SweepInterval) == nil {
		seen = d.sweep(ctx, seen)
	}
}

// sweep requests recovery of each actor that is running or transitional with no issue lane in this sweep and in
// prev, the result of the previous sweep. It returns the actors of this sweep that are running or transitional with
// no issue lane; a failed list keeps prev.
func (d *Dispatcher) sweep(ctx context.Context, prev map[models.IssueKey]bool) map[models.IssueKey]bool {
	listed, err := d.sb.List(ctx, actorPrefix)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Warn("Sweep: cannot list actors", "error", err)
		}

		return prev
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	unattended := map[models.IssueKey]bool{}

	for _, a := range d.issueActors(listed, "Sweep") {
		if a.state.Settled() {
			continue
		}

		if _, busy := d.lanes[a.key]; busy {
			continue
		}

		unattended[a.key] = true

		if prev[a.key] {
			d.log.Warn("Sweep: actor without an issue lane; recover requested",
				"actor", a.key.ActorName(), "actor_state", string(a.state))
			d.enqueueLocked(intake.Request{Key: a.key, Kind: intake.Recover})
		}
	}

	return unattended
}

// issueActor is an actor of a list with the issue its name stands for.
type issueActor struct {
	key   models.IssueKey
	state models.ActorState
}

// issueActors returns the actors of listed whose names are issue-<project-id>-<iid>; pass names the pass in the
// warning for any other name.
func (d *Dispatcher) issueActors(listed []models.ActorSummary, pass string) []issueActor {
	out := make([]issueActor, 0, len(listed))

	for _, a := range listed {
		key, ok := parseActorName(a.Name)
		if !ok {
			d.log.Warn(pass+": actor name is not issue-<project-id>-<iid>", "actor", a.Name)
			continue
		}

		out = append(out, issueActor{key: key, state: a.State})
	}

	return out
}

// holdRecovery takes a recovery slot for the lane of key. It reports false when ctx ended first.
func (d *Dispatcher) holdRecovery(ctx context.Context, key models.IssueKey) bool {
	select {
	case d.recovering <- struct{}{}:
	case <-ctx.Done():
		return false
	}

	d.mu.Lock()
	d.settling[key] = true
	d.mu.Unlock()

	return true
}

// releaseRecovery frees the recovery slot of the lane of key, if it holds one.
func (d *Dispatcher) releaseRecovery(key models.IssueKey) {
	d.mu.Lock()
	held := d.settling[key]
	delete(d.settling, key)
	d.mu.Unlock()

	if held {
		<-d.recovering
	}
}

func parseActorName(name string) (models.IssueKey, bool) {
	var k models.IssueKey

	n, err := fmt.Sscanf(name, "issue-%d-%d", &k.ProjectID, &k.IID)
	if err != nil || n != 2 || k.ActorName() != name || k.ProjectID <= 0 || k.IID <= 0 {
		return models.IssueKey{}, false
	}

	return k, true
}
