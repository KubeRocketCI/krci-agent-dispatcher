package dispatcher

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/templates"
)

// act runs one request: gather facts, decide, execute. Transitional actors are re-read up to MaxRechecks.
func (d *Dispatcher) act(ctx context.Context, r intake.Request) {
	log := d.log.With("issue", r.Key.String(), "request", r.Kind.String())
	name := r.Key.ActorName()

	for rechecks := 0; ; rechecks++ {
		view, err := d.load(ctx, log, r)
		if errors.Is(err, gitlab.ErrNotFound) {
			d.issueGone(ctx, log, r.Key)
			return
		}

		if err != nil {
			log.Error("Cannot read the issue", "error", err)
			return
		}

		enabled := view.Issue.IssueState == models.IssueOpened && models.HasLabel(view.Issue.Labels, d.cfg.TriggerLabel)

		var actor models.ActorView

		if err := d.retry(ctx, func() error { actor, err = d.sb.Inspect(ctx, name); return err }); err != nil {
			_, labelled := models.StateFromLabels(view.Issue.Labels)
			if !enabled || (!view.HasRecord && !labelled) {
				log.Error("Cannot read the actor; the issue has no agent, nothing recorded", "error", err)
				return
			}

			d.failRPC(ctx, log, r.Key, view, models.ActorView{}, "Cannot read the actor", err)

			return
		}

		f := decide.Facts{View: view, Actor: actor, Request: r.Kind, Rechecks: rechecks}
		if enabled {
			f.TriggerAfterLast = d.triggerAfterLast(ctx, log, view)

			if r.Kind == intake.Start {
				f.AuthorOK = d.isDeveloper(ctx, log, r.Key.ProjectID, r.Author)
			}
		}

		dec := decide.Decide(f, d.limits)
		log.Info("Decision", "row", dec.Row, "action", dec.Action.String(), "actor_state", string(actor.State))

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
	case decide.SuspendOnly:
		d.write(ctx, log, key, view, actor, dec.Writes)

		if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, key.ActorName()) }); err != nil {
			log.Error("Cannot suspend the actor in error", "error", err)
			return
		}

		log.Info("Actor in error suspended", "actor", key.ActorName())
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

	tmpl, pol, err := templates.Render(templates.Params{
		Atespace: d.cfg.Atespace, Pool: d.cfg.Pool, SecretsNamespace: d.cfg.SecretsNamespace, Name: name,
		Image: d.cfg.Image, GitLabURL: d.cfg.GitLabURL, ProjectPath: view.Issue.ProjectPath, BotUsername: d.cfg.Bot,
		SessionID: session, IID: key.IID,
	})
	if err != nil {
		d.failRPC(ctx, log, key, view, actor, "Cannot render the actor template", err)

		return
	}

	if err := d.retry(ctx, func() error { return d.sb.Create(ctx, name, tmpl, pol) }); err != nil {
		d.failRPC(ctx, log, key, view, actor, "Cannot create the actor", err)

		return
	}

	view.Status.Session = session

	var created models.ActorView

	if err := d.retry(ctx, func() error { created, err = d.sb.Inspect(ctx, name); return err }); err != nil {
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
	case errors.Is(err, substrate.ErrNoRoom):
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

// stop suspends a running actor, notes the agent's merge request, deletes the actor, then records done. The note
// comes before the delete: a stop cut later runs again from restart recovery, and the marker keeps it to one note.
func (d *Dispatcher) stop(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView,
) {
	name := key.ActorName()

	if actor.State == models.Running {
		if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) }); err != nil {
			log.Warn("Cannot suspend before delete", "error", err)
		}
	}

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

	err := d.retry(wctx, func() error { return d.gl.NoteMergeRequest(wctx, key.ProjectID, key.AgentBranch(), n) })
	if err != nil {
		log.Warn("Cannot note the agent's merge request", "branch", key.AgentBranch(), "error", err)
	}
}

// issueGone deletes the actor of an issue that GitLab no longer has, when its project is still readable (the
// issue was deleted). A project that is not readable (deleted, or bot access lost) keeps the actor, suspended.
func (d *Dispatcher) issueGone(ctx context.Context, log *slog.Logger, key models.IssueKey) {
	name := key.ActorName()

	actor, err := d.sb.Inspect(ctx, name)
	if err != nil || !actor.Exists() {
		return
	}

	if err := d.retry(ctx, func() error { return d.gl.CheckProject(ctx, key.ProjectID) }); err != nil {
		log.Error("Issue not found and project not readable; actor kept", "actor", name, "error", err)

		if actor.State == models.Running {
			if err := d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) }); err != nil {
				log.Error("Cannot suspend the kept actor", "actor", name, "error", err)
			}
		}

		return
	}

	if actor.State == models.Running {
		_ = d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) })
	}

	if err := d.retry(ctx, func() error { return d.sb.Delete(ctx, name) }); err != nil {
		log.Error("Cannot delete the actor of a deleted issue", "error", err)
		return
	}

	log.Info("Actor of a deleted issue deleted", "actor", name)
}

// triggerAfterLast reports whether a comment above the last marker's last= is by a Developer or higher.
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

		if d.isDeveloper(ctx, log, view.Issue.Key.ProjectID, n.Author) {
			return true
		}
	}

	return false
}

func (d *Dispatcher) isDeveloper(ctx context.Context, log *slog.Logger, projectID int64, username string) bool {
	if username == "" {
		return false
	}

	level, err := d.gl.AccessLevel(ctx, projectID, username)
	if err != nil {
		log.Warn("Access level lookup failed; not a trigger", "user", username, "error", err)
		return false
	}

	if level < Developer {
		log.Info("Below Developer; not a trigger", "user", username, "level", level)
		return false
	}

	return true
}

// retry calls fn, then retries it 3 times (1 s, 2 s, 4 s) on errors other than ErrNoRoom and the ErrNotFound errors.
func (d *Dispatcher) retry(ctx context.Context, fn func() error) error {
	wait := retryFirstWait

	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt == retryAttempts || errors.Is(err, substrate.ErrNoRoom) ||
			errors.Is(err, substrate.ErrNotFound) || errors.Is(err, gitlab.ErrNotFound) {
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

// write applies w; a record without status rows gets them from the view and the actor.
func (d *Dispatcher) write(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView, w models.Writes,
) {
	if w.Record != nil && w.Status == nil {
		v := models.StatusView{
			Actor: key.ActorName(), ActorUID: actor.UID, Pod: actor.Pod, Session: view.Status.Session,
			Updated: d.clk.Now().UTC().Format(statusTimeUTC),
		}
		w.Status = &v
	}

	d.put(ctx, log, key, w)
}

// put applies w as given; a record without status rows keeps the rows of the status note.
func (d *Dispatcher) put(ctx context.Context, log *slog.Logger, key models.IssueKey, w models.Writes) {
	if w.Empty() {
		return
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()

	if err := d.retry(wctx, func() error { return d.gl.Write(wctx, key, w) }); err != nil {
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
		d.put(ctx, log, key, w)

		return
	}

	actor, err := d.sb.Inspect(ctx, key.ActorName())
	if err != nil {
		log.Warn("Cannot read the actor for the status rows; rows kept", "error", err)
		d.put(ctx, log, key, w)

		return
	}

	d.write(ctx, log, key, view, actor, w)
}

// load reads the issue; a recover request retries read errors other than ErrNotFound with backoff until ctx ends,
// so a GitLab outage at a dispatcher restart does not leave a running actor alone.
func (d *Dispatcher) load(ctx context.Context, log *slog.Logger, r intake.Request) (models.IssueView, error) {
	wait := retryFirstWait

	for {
		view, err := d.gl.Load(ctx, r.Key)
		if err == nil || r.Kind != intake.Recover || errors.Is(err, gitlab.ErrNotFound) {
			return view, err
		}

		log.Warn("Restart recovery: cannot read the issue; retrying", "error", err, "in", wait.String())

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
