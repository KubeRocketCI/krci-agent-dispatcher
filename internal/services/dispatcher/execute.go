package dispatcher

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// act runs one request: gather facts, decide, execute. Transitional actors are re-read up to MaxRechecks.
// A recover request holds a recovery slot until it ends or its activation loop starts.
func (d *Dispatcher) act(ctx context.Context, r intake.Request) {
	log := d.log.With("issue", r.Key.String(), "request", r.Kind.String())
	name := r.Key.ActorName()

	if r.Kind == intake.Recover {
		if !d.holdRecovery(ctx, r.Key) {
			return
		}

		defer d.releaseRecovery(r.Key)
	}

	for rechecks := 0; ; rechecks++ {
		view, err := d.load(ctx, log, r)
		if err != nil {
			d.settleUnread(ctx, log, r.Key, err)
			return
		}

		enabled := view.Enabled(d.cfg.TriggerLabel)

		var actor models.ActorView

		if err := d.retry(ctx, func() error { actor, err = d.sb.Inspect(ctx, name); return err }); err != nil {
			if ctx.Err() != nil {
				log.Info("Cannot read the actor; dispatcher stopping, left for restart recovery", "error", err)
				return
			}

			log.Error("Cannot read the actor", "error", err)

			actor = models.ActorView{State: models.Unknown}
		}

		f := decide.Facts{View: view, Actor: actor, Request: r.Kind, Rechecks: rechecks}

		if enabled && actor.State != models.Unknown {
			f.TriggerAfterLast = d.triggerAfterLast(ctx, log, view)

			if r.Kind == intake.Start || r.Kind == intake.ClearError {
				f.AuthorOK, err = d.canTrigger(ctx, log, r.Key.ProjectID, r.Author)
				if err != nil {
					f.AuthorUnknown = true

					log.Error("Cannot check whether the request author can trigger the agent", "user", r.Author,
						"error", err)
				}
			}
		}

		dec := decide.Decide(f, d.limits)
		log.Info("Decision", "rule", string(dec.Rule), "action", dec.Action.String(), "actor_state", string(actor.State))

		if dec.Action == decide.Recheck {
			if d.clk.Sleep(ctx, recheckPause) != nil {
				return
			}

			continue
		}

		d.execute(ctx, log, r.Key, view, actor, dec)

		return
	}
}

func (d *Dispatcher) execute(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView, dec decide.Decision,
) {
	switch dec.Action {
	case decide.Stop:
		d.stop(ctx, log, key, view, actor)
	case decide.Create:
		d.create(ctx, log, key, view, actor)
	case decide.Activate, decide.Revert:
		d.write(ctx, log, key, view, actor, dec.Writes)
		d.activate(ctx, log, key, lastMarkerID(view.Notes, d.cfg.Bot), dec.Action == decide.Revert)
	case decide.Continue:
		d.write(ctx, log, key, view, actor, dec.Writes)
		d.loop(ctx, log, key, continueSince(view.Notes, d.cfg.Bot), true)
	case decide.Discard:
		d.discard(ctx, log, key.ActorName())
	case decide.Keep:
		d.keepActor(ctx, log, key.ActorName())
	case decide.SuspendOnly:
		d.write(ctx, log, key, view, actor, dec.Writes)
		d.suspendInError(ctx, log, key.ActorName())
	default:
		d.write(ctx, log, key, view, actor, dec.Writes)
	}
}

// create deletes a partial create, creates the actor, records waiting, then runs the first activation.
func (d *Dispatcher) create(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView,
) {
	name := key.ActorName()
	session := d.cfg.NewSessionID()

	if actor.Exists() {
		if err := d.retry(ctx, func() error { return d.sb.Delete(ctx, name) }); err != nil {
			d.failRPC(ctx, log, key, view, actor, "Cannot delete the partial create", err)

			return
		}
	}

	spec := models.ActorSpec{
		ProjectPath: view.Issue.ProjectPath, IID: key.IID, SessionID: session, BotUsername: d.cfg.Bot,
	}

	if err := d.retry(ctx, func() error { return d.sb.Create(ctx, name, spec) }); err != nil {
		d.failRPC(ctx, log, key, view, actor, "Cannot create the actor", err)

		return
	}

	view.Status.Session = session

	var created models.ActorView

	err := d.retry(ctx, func() (err error) { created, err = d.sb.Inspect(ctx, name); return err })
	if err != nil {
		d.failRPC(ctx, log, key, view, actor, "Cannot read the created actor", err)

		return
	}

	w := stateWrites(models.Waiting, "")
	d.write(ctx, log, key, view, created, w)
	log.Info("Actor created", "actor", name, "session", session)

	d.activate(ctx, log, key, lastMarkerID(view.Notes, d.cfg.Bot), false)
}

// activate reverts first when asked, resumes, records working, then runs the activation loop. since is the
// note id of the last marker read before the resume.
func (d *Dispatcher) activate(ctx context.Context, log *slog.Logger, key models.IssueKey, since int64, revert bool) {
	name := key.ActorName()

	if revert {
		if err := d.retry(ctx, func() error { return d.sb.Revert(ctx, name) }); err != nil {
			d.failRPCFresh(ctx, log, key, "Cannot revert the actor", err)

			return
		}
	}

	err := d.retry(ctx, func() error { return d.sb.Resume(ctx, name) })

	switch {
	case errors.Is(err, models.ErrNoRoom):
		log.Info("No free worker", "actor", name)
		d.writeFresh(ctx, log, key, models.Writes{Notes: []models.NewNote{{Body: models.NoRoomText}}})

		return
	case err != nil:
		d.failRPCFresh(ctx, log, key, "Cannot resume the actor", err)

		return
	}

	d.writeFresh(ctx, log, key, stateWrites(models.Working, ""))
	log.Info("Activation started", "actor", name)

	d.loop(ctx, log, key, since, false)
}

func lastMarkerID(notes []models.Note, bot string) int64 {
	if m, ok := models.LastMarker(notes, bot); ok {
		return m.NoteID
	}

	return 0
}

// stop notes the agent's merge request, deletes the actor, then records done. A running actor is deleted as it
// is: Substrate ends its workload and frees its worker. The note comes before the delete: a stop cut later runs
// again from restart recovery, and the marker keeps it to one note.
func (d *Dispatcher) stop(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView,
) {
	name := key.ActorName()

	d.noteAgentMR(ctx, log, key)

	if actor.Exists() {
		if err := d.retry(ctx, func() error { return d.sb.Delete(ctx, name) }); err != nil {
			d.failRPC(ctx, log, key, view, actor, "Cannot delete the actor", err)

			return
		}

		log.Info("Actor deleted", "actor", name)
	}

	d.write(ctx, log, key, view, models.ActorView{State: models.None}, stateWrites(models.Done, ""))
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

// settleUnread decides a failed issue read and executes the decision. The issue is gone when GitLab does not have it
// and its project is readable; any other failure leaves it unavailable. A read cut by the end of ctx decides nothing.
func (d *Dispatcher) settleUnread(ctx context.Context, log *slog.Logger, key models.IssueKey, readErr error) {
	if ctx.Err() != nil {
		return
	}

	read := decide.ReadUnavailable

	if errors.Is(readErr, models.ErrNotFound) &&
		d.retry(ctx, func() error { return d.gl.CheckProject(ctx, key.ProjectID) }) == nil {
		read = decide.ReadGone
	}

	dec := decide.Decide(decide.Facts{Issue: read}, d.limits)

	level := slog.LevelInfo
	if dec.Action == decide.Keep {
		level = slog.LevelError
	}

	log.Log(ctx, level, "Decision", "rule", string(dec.Rule), "action", dec.Action.String(), "issue_read",
		read.String(), "error", readErr)

	d.execute(ctx, log, key, models.IssueView{}, models.ActorView{}, dec)
}

// suspendInError suspends the actor of an issue in error; the error stays.
func (d *Dispatcher) suspendInError(ctx context.Context, log *slog.Logger, name string) {
	if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) }); err != nil {
		log.Error("Cannot suspend the actor in error", "error", err)
		return
	}

	log.Info("Actor in error suspended", "actor", name)
}

// discard deletes the actor of a gone issue. Delete needs no actor read: a missing actor or part is not an error.
func (d *Dispatcher) discard(ctx context.Context, log *slog.Logger, name string) {
	if err := d.retry(ctx, func() error { return d.sb.Delete(ctx, name) }); err != nil {
		log.Error("Cannot discard the actor", "actor", name, "error", err)
		return
	}

	log.Info("Actor discarded", "actor", name)
}

// keepActor keeps an actor and suspends it when it runs. An actor that cannot be read gets a suspend request: a
// suspended or missing actor is left as it is.
func (d *Dispatcher) keepActor(ctx context.Context, log *slog.Logger, name string) {
	var actor models.ActorView

	if err := d.retry(ctx, func() (err error) { actor, err = d.sb.Inspect(ctx, name); return err }); err != nil {
		if ctx.Err() != nil {
			log.Info("Kept actor not read; dispatcher stopping, left for restart recovery", "actor", name)
			return
		}

		log.Error("Cannot read the kept actor; suspend requested", "actor", name, "error", err)
		d.requestSuspend(ctx, log, name)

		return
	}

	if actor.State == models.Running {
		d.requestSuspend(ctx, log, name)
	}
}

// requestSuspend suspends a kept actor. A missing actor is not an error; a suspend cut by the end of ctx leaves the
// actor for the next restart recovery.
func (d *Dispatcher) requestSuspend(ctx context.Context, log *slog.Logger, name string) {
	err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) })

	switch {
	case err == nil, errors.Is(err, models.ErrActorNotFound):
	case ctx.Err() != nil:
		log.Info("Kept actor not suspended; dispatcher stopping, left for restart recovery", "actor", name)
	default:
		log.Error("Cannot suspend the kept actor", "actor", name, "error", err)
	}
}

// triggerAfterLast reports whether a comment above the last marker's last= is a trigger comment. A check that
// fails ends the search with false.
func (d *Dispatcher) triggerAfterLast(ctx context.Context, log *slog.Logger, view models.IssueView) bool {
	var last int64
	if m, ok := models.LastMarker(view.Notes, d.cfg.Bot); ok {
		last = m.Last
	}

	checked := map[string]bool{}

	for _, n := range view.Notes {
		if n.System || n.Author == d.cfg.Bot || n.ID <= last || checked[n.Author] {
			continue
		}

		checked[n.Author] = true

		ok, err := d.canTrigger(ctx, log, view.Issue.Key.ProjectID, n.Author)
		if err != nil {
			log.Error("Cannot check whether a comment author can trigger the agent; no trigger comment found",
				"user", n.Author, "error", err)

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
	return models.Writes{Record: &models.Record{State: s, Reason: reason}, Label: models.StateLabel(s)}
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
// view of the issue, nil when none was read.
func (d *Dispatcher) put(ctx context.Context, log *slog.Logger, key models.IssueKey, known *models.IssueView,
	w models.Writes,
) {
	if w.Empty() {
		return
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	if err := d.retry(wctx, func() error { return d.gl.Write(wctx, key, known, w) }); err != nil {
		log.Error("Cannot write to GitLab", "error", err)
		return
	}

	if w.Record != nil {
		log.Info("Record written", "state", string(w.Record.State), "reason", w.Record.Reason)
	}
}

// writeFresh applies w with status rows from a fresh read of the issue and the actor. When a read fails, the
// status note keeps its rows.
func (d *Dispatcher) writeFresh(ctx context.Context, log *slog.Logger, key models.IssueKey, w models.Writes) {
	view, err := d.gl.Load(ctx, key)
	if err != nil {
		log.Warn("Cannot read the issue for the status rows; rows kept", "error", err)
		d.put(ctx, log, key, nil, w)

		return
	}

	actor, err := d.sb.Inspect(ctx, key.ActorName())
	if err != nil {
		log.Warn("Cannot read the actor for the status rows; rows kept", "error", err)
		d.put(ctx, log, key, &view, w)

		return
	}

	d.write(ctx, log, key, view, actor, w)
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
		if err == nil || r.Kind != intake.Recover || errors.Is(err, models.ErrNotFound) ||
			errors.Is(err, models.ErrForbidden) {
			return view, err
		}

		log.Warn("Recover request: cannot read the issue; retrying", "error", err, "in", wait.String())

		if d.clk.Sleep(ctx, wait) != nil {
			return view, err
		}

		wait = min(2*wait, backoffMax)
	}
}

// failRPC logs a failed Substrate call and records substrate error. A call cut by the end of ctx records
// nothing: the actor is left for the next restart recovery.
func (d *Dispatcher) failRPC(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView, msg string, err error,
) {
	if ctx.Err() != nil {
		log.Info(msg+"; dispatcher stopping, left for restart recovery", "error", err)
		return
	}

	log.Error(msg, "error", err)
	d.write(ctx, log, key, view, actor, substrateError())
}

// failRPCFresh is failRPC with status rows from a fresh read.
func (d *Dispatcher) failRPCFresh(ctx context.Context, log *slog.Logger, key models.IssueKey, msg string, err error) {
	if ctx.Err() != nil {
		log.Info(msg+"; dispatcher stopping, left for restart recovery", "error", err)
		return
	}

	log.Error(msg, "error", err)
	d.writeFresh(ctx, log, key, substrateError())
}
