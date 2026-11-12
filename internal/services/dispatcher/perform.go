package dispatcher

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/decide"
)

// current is the view and the actor that the effects of a plan read and update, and the activation loop state of a
// poll decision (nil otherwise). A nil view is unknown.
type current struct {
	key   models.IssueKey
	view  *models.IssueView
	actor models.ActorView
	act   *activation
}

// perform carries out p in order, from the view and the actor the decision was made on. A failed effect follows its
// decide.Fail; ErrNoRoom on a resume notes no room and ends the plan.
func (d *Dispatcher) perform(ctx context.Context, log *slog.Logger, key models.IssueKey, view models.IssueView,
	actor models.ActorView, a *activation, p decide.Plan,
) {
	c := &current{key: key, view: &view, actor: actor, act: a}

	for _, e := range p {
		if !d.effect(ctx, log, c, e) {
			return
		}
	}
}

// effect carries out e and reports whether the plan goes on.
func (d *Dispatcher) effect(ctx context.Context, log *slog.Logger, c *current, e decide.Effect) bool {
	name := c.key.ActorName()

	var err error

	switch e.Kind {
	case decide.KindWrite:
		d.writeCurrent(ctx, log, c, e.Writes)
	case decide.KindNoteMR:
		d.noteAgentMR(ctx, log, c.key)
	case decide.KindReload:
		d.reload(ctx, log, c)
	case decide.KindInspect:
		var actor models.ActorView

		if actor, err = d.inspect(ctx, name); err == nil {
			c.actor = actor
		}
	case decide.KindDelete:
		if err = d.retry(ctx, func() error { return d.sb.Delete(ctx, name) }); err == nil {
			c.actor = models.ActorView{State: models.None}

			log.Info("Actor deleted", "actor", name)
		}
	case decide.KindCreate:
		err = d.createActor(ctx, log, c)
	case decide.KindRevert:
		err = d.retry(ctx, func() error { return d.sb.Revert(ctx, name) })
	case decide.KindResume:
		err = d.retry(ctx, func() error { return d.sb.Resume(ctx, name) })
		if errors.Is(err, models.ErrNoRoom) {
			log.Info("No free worker", "actor", name)
			d.reload(ctx, log, c)
			d.writeCurrent(ctx, log, c, models.Writes{Notes: []models.NewNote{{Body: models.NoRoomText}}})

			return false
		}

		if err == nil {
			log.Info("Activation started", "actor", name)
		}
	case decide.KindSuspend:
		err = d.retry(ctx, func() error { return d.sb.Suspend(ctx, name) })
	case decide.KindSuspendIfRunning:
		d.keepActor(ctx, log, name)
	case decide.KindLoop:
		d.loop(ctx, log, c.key, e.Since, e.Recovered)
	case decide.KindAdvance:
		d.advance(log, c.act, e.Since)
	case decide.KindRecheck:
	}

	if err == nil {
		return true
	}

	return d.failed(ctx, log, c, e, err)
}

// advance starts the next turn of the activation loop at since.
func (d *Dispatcher) advance(log *slog.Logger, a *activation, since int64) {
	if a.recovered && a.first {
		log.Info("Recovered activation; waiting for the next marker", "since", since)
	} else {
		log.Info("Turn ended; comments after it, staying resumed", "since", since)
	}

	a.since, a.start = since, d.clk.Now()
}

// failed handles the failure of e and reports whether the plan goes on.
func (d *Dispatcher) failed(ctx context.Context, log *slog.Logger, c *current, e decide.Effect, err error) bool {
	flog := log.With("effect", e.Kind.String(), "error", err)

	if ctx.Err() != nil {
		flog.Info("Effect failed; dispatcher stopping, left for restart recovery")
		return false
	}

	if e.Fail == decide.FailLog {
		flog.Warn("Effect failed")

		if e.Kind == decide.KindInspect {
			c.actor = models.ActorView{State: models.Unknown}
		}

		return true
	}

	flog.Error("Effect failed")

	switch e.Fail {
	case decide.FailError:
		d.writeCurrent(ctx, log, c, substrateError())
	case decide.FailErrorFresh:
		d.reload(ctx, log, c)
		d.writeCurrent(ctx, log, c, substrateError())
	case decide.FailLog, decide.FailAbort:
	}

	return false
}

// writeCurrent applies w with status rows from the current view and actor; an unknown view keeps the rows.
func (d *Dispatcher) writeCurrent(ctx context.Context, log *slog.Logger, c *current, w models.Writes) {
	if c.view == nil {
		d.put(ctx, log, c.key, nil, w)
		return
	}

	d.write(ctx, log, c.key, *c.view, c.actor, w)
}

// reload reads the issue, then the actor, each with retry. A failed issue read leaves both unknown; a failed actor
// read leaves the actor unknown.
func (d *Dispatcher) reload(ctx context.Context, log *slog.Logger, c *current) {
	var view models.IssueView

	if err := d.retry(ctx, func() (err error) { view, err = d.gl.Load(ctx, c.key); return err }); err != nil {
		log.Warn("Cannot read the issue for the status rows; rows kept", "error", err)

		c.view, c.actor = nil, models.ActorView{State: models.Unknown}

		return
	}

	c.view = &view

	actor, err := d.inspect(ctx, c.key.ActorName())
	if err != nil {
		log.Warn("Cannot read the actor for the status rows; rows kept", "error", err)

		c.actor = models.ActorView{State: models.Unknown}

		return
	}

	c.actor = actor
}

// createActor creates the actor with a new session and sets the session on the current view.
func (d *Dispatcher) createActor(ctx context.Context, log *slog.Logger, c *current) error {
	name := c.key.ActorName()
	session := d.cfg.NewSessionID()

	spec := models.ActorSpec{
		ProjectPath: c.view.Issue.ProjectPath, IID: c.key.IID, SessionID: session, BotUsername: d.cfg.Bot,
	}

	if err := d.retry(ctx, func() error { return d.sb.Create(ctx, name, spec) }); err != nil {
		return err
	}

	c.view.Status.Session = session

	log.Info("Actor created", "actor", name, "session", session)

	return nil
}
