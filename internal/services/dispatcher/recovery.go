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
	var names []string

	for wait := retryFirstWait; ; wait = min(2*wait, backoffMax) {
		var err error

		if names, err = d.sb.List(ctx, actorPrefix); err == nil {
			break
		}

		d.log.Error("Restart recovery: cannot list actors; retrying", "error", err, "in", wait.String())

		if d.clk.Sleep(ctx, wait) != nil {
			return
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, n := range names {
		key, ok := parseActorName(n)
		if !ok {
			d.log.Warn("Restart recovery: actor name is not issue-<project-id>-<iid>", "actor", n)
			continue
		}

		d.enqueueLocked(intake.Request{Key: key, Kind: intake.Recover})
	}

	d.log.Info("Restart recovery queued", "actors", len(names))
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
