package dispatcher

import (
	"context"
	"fmt"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// recover queues a recover request on the lane of every issue actor in the atespace (spec §5.5). The actor list
// is retried with backoff until it succeeds or ctx ends. Requests that arrive meanwhile wait as pending.
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

func parseActorName(name string) (models.IssueKey, bool) {
	var k models.IssueKey

	n, err := fmt.Sscanf(name, "issue-%d-%d", &k.ProjectID, &k.IID)
	if err != nil || n != 2 || k.ActorName() != name || k.ProjectID <= 0 || k.IID <= 0 {
		return models.IssueKey{}, false
	}

	return k, true
}
