package dispatcher

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

// act runs one request: observe, decide, perform the plan. Transitional actors are re-read up to MaxRechecks.
// A recover request holds a recovery slot until it ends or its activation loop starts.
func (d *Dispatcher) act(ctx context.Context, r intake.Request) {
	log := d.log.With("issue", r.Key.String(), "request", r.Kind.String())

	if r.Kind == intake.Recover {
		if !d.holdRecovery(ctx, r.Key) {
			return
		}

		defer d.releaseRecovery(r.Key)
	}

	for rechecks := 0; ; rechecks++ {
		o, ok := d.observe(ctx, log, r, rechecks)
		if !ok {
			return
		}

		f := o.facts
		dec := decide.Decide(f, d.limits)
		logDecision(ctx, log, o, dec)

		if dec.Plan.IsRecheck() {
			if d.clk.Sleep(ctx, recheckPause) != nil {
				return
			}

			continue
		}

		d.perform(ctx, log, r.Key, f.View, f.Actor, nil, dec.Plan)

		return
	}
}

// noteAgentMR posts the stopped note once on the agent's open merge request.
func (d *Dispatcher) noteAgentMR(ctx context.Context, log *slog.Logger, key models.IssueKey) {
	n := models.NewNote{Body: models.StoppedMRText + "\n" + models.StoppedMRMarker, Marker: models.StoppedMRMarker}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	err := d.retry(wctx, func() error { return d.gl.NoteMergeRequest(wctx, key.ProjectID, key.IssueBranch(), n) })
	if err != nil {
		log.Warn("Cannot note the agent's merge request", "branch", key.IssueBranch(), "error", err)
	}
}

// logDecision logs dec: with the actor state, or after a failed issue read with the read outcome and its error, at
// error level and with the actor state when the issue is unavailable.
func logDecision(ctx context.Context, log *slog.Logger, o observation, dec decide.Decision) {
	if o.facts.Issue == decide.ReadOK {
		log.Info("Decision", "rule", string(dec.Rule), "plan", dec.Plan.String(), "actor_state",
			string(o.facts.Actor.State))

		return
	}

	level := slog.LevelInfo
	attrs := []any{"rule", string(dec.Rule), "plan", dec.Plan.String(), "issue_read", o.facts.Issue.String(),
		"error", o.readErr}

	if o.facts.Issue == decide.ReadUnavailable {
		attrs = append(attrs, "actor_state", string(o.facts.Actor.State))
		level = slog.LevelError
	}

	log.Log(ctx, level, "Decision", attrs...)
}

// triggerAfterLast reports whether a comment of the next turn is a trigger comment: the authors are checked once
// each, in note id order, until one can trigger. A check that fails ends the search with false.
func (d *Dispatcher) triggerAfterLast(ctx context.Context, log *slog.Logger, view models.IssueView) bool {
	for _, author := range turn.NewHistory(view.Notes, d.cfg.Bot, view.Status.Session).Next().Authors() {
		ok, err := d.canTrigger(ctx, log, view.Issue.Key.ProjectID, author)
		if err != nil {
			log.Error("Cannot check whether a comment author can trigger the agent; no trigger comment found",
				"user", author, "error", err)

			return false
		}

		if ok {
			return true
		}
	}

	return false
}

// canTrigger reports whether username has the trigger role or higher on the project. The check
// is retried.
func (d *Dispatcher) canTrigger(ctx context.Context, log *slog.Logger, projectID int64, username string,
) (bool, error) {
	var ok bool

	err := d.retry(ctx, func() (err error) { ok, err = d.gl.CanTrigger(ctx, projectID, username); return err })
	if err != nil {
		return false, err
	}

	if !ok {
		log.Info("User cannot trigger the agent", "user", username)
	}

	return ok, nil
}

// inspect reads the actor with retry.
func (d *Dispatcher) inspect(ctx context.Context, name string) (models.ActorView, error) {
	var actor models.ActorView

	err := d.retry(ctx, func() (err error) { actor, err = d.sb.Inspect(ctx, name); return err })

	return actor, err
}

// retry calls fn, then retries it 3 times (1 s, 2 s, 4 s) on errors other than ErrNoRoom, ErrActorNotFound,
// ErrInvalidSpec, ErrNotFound and ErrForbidden.
func (d *Dispatcher) retry(ctx context.Context, fn func() error) error {
	wait := retryFirstWait

	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt == retryAttempts || errors.Is(err, models.ErrNoRoom) ||
			errors.Is(err, models.ErrActorNotFound) || errors.Is(err, models.ErrInvalidSpec) ||
			errors.Is(err, models.ErrNotFound) || errors.Is(err, models.ErrForbidden) {
			return err
		}

		if d.clk.Sleep(ctx, wait) != nil {
			return err
		}

		wait *= 2
	}
}

func stateWrites(s models.AgentState, reason string) models.Writes {
	return models.Writes{Record: &models.Record{State: s, Reason: reason}, Label: s}
}

func substrateError() models.Writes { return stateWrites(models.Error, models.ReasonSubstrateError) }

// write applies w; a record without status rows gets them from the view and the actor. An unknown actor keeps the
// rows of the status note.
func (d *Dispatcher) write(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView, w models.Writes,
) {
	if w.Record != nil && w.Status == nil && actor.State != models.Unknown {
		v := models.StatusView{
			Actor: key.ActorName(), ActorUID: actor.UID, Pod: actor.Pod, Session: view.Status.Session,
			Updated: d.clk.Now().UTC().Format(statusTimeUTC),
		}
		w.Status = &v
	}

	d.put(ctx, log, key, &view, w)
}

// put applies w as given; a record without status rows keeps the rows of the status note. known is the last
// view of the issue, nil when none was read. Retries pass nil.
func (d *Dispatcher) put(ctx context.Context, log *slog.Logger, key models.IssueKey, known *models.IssueView,
	w models.Writes,
) {
	if w.Empty() {
		return
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	if err := d.retry(wctx, func() error {
		defer func() { known = nil }()

		return d.gl.Write(wctx, key, known, w)
	}); err != nil {
		log.Error("Cannot write to GitLab", "error", err)
		return
	}

	if w.Record != nil {
		log.Info("Record written", "state", string(w.Record.State), "reason", w.Record.Reason)
	}
}

// load reads the issue with retry. A recover request retries read errors other than ErrNotFound and ErrForbidden
// with backoff until ctx ends, so a GitLab outage at a dispatcher restart does not leave a running actor alone.
func (d *Dispatcher) load(ctx context.Context, log *slog.Logger, r intake.Request) (models.IssueView, error) {
	if r.Kind != intake.Recover {
		var view models.IssueView

		err := d.retry(ctx, func() (err error) { view, err = d.gl.Load(ctx, r.Key); return err })

		return view, err
	}

	wait := retryFirstWait

	for {
		view, err := d.gl.Load(ctx, r.Key)
		if err == nil || errors.Is(err, models.ErrNotFound) || errors.Is(err, models.ErrForbidden) {
			return view, err
		}

		log.Warn("Recover request: cannot read the issue; retrying", "error", err, "in", wait.String())

		if d.clk.Sleep(ctx, wait) != nil {
			return view, err
		}

		wait = min(2*wait, backoffMax)
	}
}
