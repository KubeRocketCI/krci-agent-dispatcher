package dispatcher

import (
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// order is the run order of pending requests on a lane.
var order = []intake.Kind{intake.Stop, intake.Start, intake.ClearError, intake.Activate, intake.Recover}

// lane is the issue lane of one issue: one action at a time, one pending set while busy.
type lane struct {
	running bool
	pending map[intake.Kind]string
}

func (l *lane) add(r intake.Request) {
	if l.pending == nil {
		l.pending = map[intake.Kind]string{}
	}

	if _, ok := l.pending[r.Kind]; !ok || r.Author != "" {
		l.pending[r.Kind] = r.Author
	}
}

// take returns the pending requests in run order and empties the set.
func (l *lane) take(key models.IssueKey) []intake.Request {
	var out []intake.Request

	for _, k := range order {
		if author, ok := l.pending[k]; ok {
			out = append(out, intake.Request{Key: key, Kind: k, Author: author})
		}
	}

	l.pending = nil

	return out
}

func (d *Dispatcher) enqueueLocked(r intake.Request) {
	l := d.lanes[r.Key]
	if l == nil {
		l = &lane{}
		d.lanes[r.Key] = l
	}

	l.add(r)

	if d.started {
		d.startLocked(r.Key, l)
	}
}

func (d *Dispatcher) startLocked(key models.IssueKey, l *lane) {
	if d.stopping || l.running || len(l.pending) == 0 {
		return
	}

	l.running = true

	d.wg.Add(1)

	go d.runLane(key, l)
}

func (d *Dispatcher) runLane(key models.IssueKey, l *lane) {
	defer d.wg.Done()

	for {
		d.mu.Lock()
		reqs := l.take(key)

		if len(reqs) == 0 || d.ctx.Err() != nil {
			l.running = false

			delete(d.lanes, key)
			d.mu.Unlock()

			return
		}
		d.mu.Unlock()

		for _, r := range reqs {
			if d.ctx.Err() != nil {
				break
			}

			d.act(d.ctx, r)
		}
	}
}

// stopPending reports whether a stop request waits on the lane of key.
func (d *Dispatcher) stopPending(key models.IssueKey) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	l := d.lanes[key]
	if l == nil {
		return false
	}

	_, ok := l.pending[intake.Stop]

	return ok
}
