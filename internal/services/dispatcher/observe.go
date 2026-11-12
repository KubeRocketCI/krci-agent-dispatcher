package dispatcher

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// unknownActor is the actor view of a failed actor read.
var unknownActor = models.ActorView{State: models.Unknown}

// observation is what one request read: the facts for decide.Decide and the error of a failed issue read.
type observation struct {
	facts   decide.Facts
	readErr error
}

// observe reads the facts of one request: the issue with retry, the actor with retry, then the trigger checks
// decide.TriggerChecks names. A failed issue read is gone or unavailable; the actor of a gone issue is not read. A
// failed actor read is the actor state unknown. It reports false when ctx ended during a read or a check: nothing is
// decided, restart recovery continues.
func (d *Dispatcher) observe(ctx context.Context, log *slog.Logger, r intake.Request, rechecks int,
) (observation, bool) {
	f := decide.Facts{Request: r.Kind, Rechecks: rechecks}

	view, err := d.load(ctx, log, r)
	if err != nil {
		f.Issue = d.issueRead(ctx, r.Key.ProjectID, err)
		if f.Issue == decide.ReadGone {
			return observation{facts: f, readErr: err}, ctx.Err() == nil
		}
	}

	var ok bool
	if f.Actor, ok = d.observeActor(ctx, log, r.Key); !ok {
		return observation{}, false
	}

	if err != nil {
		return observation{facts: f, readErr: err}, ctx.Err() == nil
	}

	f.View = view

	comments, author := decide.TriggerChecks(f, d.limits)

	if comments {
		f.TriggerAfterLast = d.triggerAfterLast(ctx, log, view)
	}

	if author {
		if f.AuthorOK, err = d.canTrigger(ctx, log, r.Key.ProjectID, r.Author); err != nil {
			f.AuthorUnknown = true

			log.Error("Cannot check whether the request author can trigger the agent", "user", r.Author, "error", err)
		}
	}

	return observation{facts: f}, ctx.Err() == nil
}

// observeActor reads the actor with retry; a failed read is the actor state unknown. It reports false when ctx
// ended during the read.
func (d *Dispatcher) observeActor(ctx context.Context, log *slog.Logger, key models.IssueKey,
) (models.ActorView, bool) {
	actor, err := d.inspect(ctx, key.ActorName())
	if err == nil {
		return actor, true
	}

	if ctx.Err() != nil {
		log.Info("Cannot read the actor; dispatcher stopping, left for restart recovery", "error", err)
		return models.ActorView{}, false
	}

	log.Error("Cannot read the actor", "error", err)

	return unknownActor, true
}

// observePoll reads the facts of one poll: the issue, in full or the notes since the last read, then the actor,
// each once; the elapsed time and a pending stop are taken after the reads. A failed issue read is gone or
// unavailable; a failed actor read is the actor state unknown. It reports false when ctx ended during a read.
func (d *Dispatcher) observePoll(ctx context.Context, log *slog.Logger, key models.IssueKey, a *activation,
) (decide.PollFacts, bool) {
	var f decide.PollFacts

	view, err := d.read(ctx, key, a)
	if err != nil {
		log.Warn("Poll: cannot read the issue", "error", err)

		f.Issue = d.issueRead(ctx, key.ProjectID, err)
	} else {
		a.known, a.full = &view, false
		f.View = view

		if f.Actor, err = d.sb.Inspect(ctx, key.ActorName()); err != nil {
			log.Warn("Poll: cannot read the actor", "error", err)

			f.Actor = unknownActor
		}
	}

	f.Since, f.Elapsed, f.StopPending = a.since, d.clk.Now().Sub(a.start), d.stopPending(key)

	return f, ctx.Err() == nil
}

// read returns the full view of the issue, or the last view with what changed since when no full read is due.
func (d *Dispatcher) read(ctx context.Context, key models.IssueKey, a *activation) (models.IssueView, error) {
	if a.full || a.known == nil {
		return d.gl.Load(ctx, key)
	}

	return d.gl.Refresh(ctx, *a.known)
}

// issueRead classifies a failed issue read: gone when GitLab does not have the issue and its project is readable,
// unavailable otherwise. A read cut by the end of ctx is unavailable without a project check.
func (d *Dispatcher) issueRead(ctx context.Context, projectID int64, readErr error) decide.IssueRead {
	if ctx.Err() == nil && errors.Is(readErr, models.ErrNotFound) &&
		d.retry(ctx, func() error { return d.gl.CheckProject(ctx, projectID) }) == nil {
		return decide.ReadGone
	}

	return decide.ReadUnavailable
}
