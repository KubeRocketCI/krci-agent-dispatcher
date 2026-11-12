package dispatcher_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/dispatcher"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/substratetest"
)

const (
	bot      = "krci-agent"
	dev      = "dev1"
	reporter = "rep1"
)

var (
	key   = models.IssueKey{ProjectID: 12, IID: 7}
	actor = key.ActorName()
	errRP = errors.New("rpc unavailable")
)

// fakeClock advances virtual time on Sleep and returns at once. onSleep runs after each Sleep.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []time.Duration
	onSleep func(d time.Duration)
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	onSleep := c.onSleep
	c.mu.Unlock()

	if onSleep != nil {
		onSleep(d)
	}

	time.Sleep(time.Microsecond)

	return ctx.Err()
}

func (c *fakeClock) setOnSleep(fn func(d time.Duration)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.onSleep = fn
}

func (c *fakeClock) count(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0

	for _, s := range c.sleeps {
		if s == d {
			n++
		}
	}

	return n
}

type env struct {
	t   *testing.T
	gl  *gitlabtest.Fake
	sb  *substratetest.Fake
	clk *fakeClock
	d   *dispatcher.Dispatcher

	mu     sync.Mutex
	turn   int
	cancel context.CancelFunc
	logs   *syncBuffer
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}

func newEnv(t *testing.T) *env {
	t.Helper()

	return newEnvWith(t, func(*dispatcher.Config) {})
}

// newEnvWith is newEnv with a change to the dispatcher config.
func newEnvWith(t *testing.T, mod func(*dispatcher.Config)) *env {
	t.Helper()

	e := &env{
		t:    t,
		logs: &syncBuffer{},
		gl:   gitlabtest.NewFake(bot),
		sb:   substratetest.NewFake("krci-agents"),
		clk:  &fakeClock{now: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)},
	}
	e.gl.SetMember(key.ProjectID, dev, 30)
	e.gl.SetMember(key.ProjectID, reporter, 20)

	cfg := dispatcher.Config{
		Bot: bot, TriggerLabel: "agent", TurnLimit: 5,
		TurnTimeout: 15 * time.Minute, PollInterval: 10 * time.Second,
		NewSessionID: func() string { return "session-1" },
		Logger:       slog.New(slog.NewTextHandler(e.logs, nil)),
	}
	mod(&cfg)
	e.d = dispatcher.New(e.gl, e.sb, e.clk, cfg)

	return e
}

// run starts Run and stops it at the end of the test.
func (e *env) run() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	done := make(chan error, 1)

	go func() { done <- e.d.Run(ctx) }()

	e.t.Cleanup(func() {
		cancel()

		select {
		case err := <-done:
			assert.NoError(e.t, err)
		case <-time.After(5 * time.Second):
			e.t.Error("Run did not return after cancel")
		}
	})
}

func (e *env) idle() {
	e.t.Helper()
	require.Eventually(e.t, e.d.Idle, 5*time.Second, time.Millisecond, "dispatcher did not become idle")
}

func (e *env) handle(k intake.Kind, author string) {
	e.d.Handle(intake.Request{Key: key, Kind: k, Author: author})
}

// issue adds the issue with labels; the record, when set, is written as the bot's status note.
func (e *env) issue(state string, rec *models.Record, labels ...string) {
	e.gl.AddIssue(key, "krci/test-go-app", state, labels...)

	if rec != nil {
		e.gl.AddNote(key, models.Note{Author: bot,
			Body: models.StatusBody(*rec, models.StatusView{State: rec.State, Actor: actor, Session: "session-0"})})
	}
}

func (e *env) comment(author string) {
	e.gl.AddNote(key, models.Note{Author: author, Body: "please continue"})
}

// reply posts the agent's reply for the next turn, covering every comment so far.
func (e *env) reply() {
	e.mu.Lock()
	e.turn++
	n := e.turn
	e.mu.Unlock()

	var last int64

	for _, nt := range e.gl.Notes(key) {
		if nt.Author != bot && !nt.System {
			last = max(last, nt.ID)
		}
	}

	e.gl.AddNote(key, models.Note{
		Author: bot,
		Body:   fmt.Sprintf("done\n<!-- krci-agent-turn n=%d exit=0 last=%d -->", n, last),
	})
}

// agentRepliesOnResume makes the agent reply once right after each resume.
func (e *env) agentRepliesOnResume() { e.sb.OnResume(func(string) { e.reply() }) }

func (e *env) record() models.Record {
	r, ok := e.gl.Record(key)
	require.True(e.t, ok, "no record")

	return r
}

func (e *env) stateLabels() []string {
	var out []string

	for _, l := range e.gl.Labels(key) {
		if strings.HasPrefix(l, "agent::") {
			out = append(out, l)
		}
	}

	return out
}

func (e *env) assertState(s models.AgentState, reason string) {
	e.t.Helper()
	assert.Equal(e.t, models.Record{State: s, Reason: reason}, e.record())
	assert.Equal(e.t, []string{models.StateLabel(s)}, e.stateLabels())
}

func (e *env) notesWith(text string) int {
	n := 0

	for _, nt := range e.gl.Notes(key) {
		if nt.Author == bot && strings.Contains(nt.Body, text) {
			n++
		}
	}

	return n
}

func waiting() *models.Record { return &models.Record{State: models.Waiting} }

func indexOf(ops []string, op string) int { return slices.Index(ops, op) }

// --- start

func TestStartOrder(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")

	var recordAtResume models.Record

	e.sb.OnResume(func(string) {
		recordAtResume, _ = e.gl.Record(key)
		e.reply()
	})
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	ops := e.sb.Ops()
	assert.Equal(t, []string{"create-template " + actor, "create-actor " + actor, "create-policy " + actor,
		"resume " + actor, "suspend " + actor}, ops)
	assert.Equal(t, models.Waiting, recordAtResume.State, "record waiting is written before the resume")
	e.assertState(models.Waiting, "")

	glOps := e.gl.Ops()
	assert.Less(t, indexOf(glOps, "create-status"), indexOf(glOps, "label agent::waiting"), "record before label")
	assert.Contains(t, glOps, "label agent::working")

	n, ok := models.StatusNote(e.gl.Notes(key), bot)
	require.True(t, ok)

	v, _ := models.ParseStatusView(n.Body)
	assert.Equal(t, "session-1", v.Session)
	assert.Equal(t, actor, v.Actor)
}

func TestStartActorSpec(t *testing.T) {
	var n int

	e := newEnvWith(t, func(c *dispatcher.Config) {
		c.NewSessionID = func() string { n++; return fmt.Sprintf("session-%d", n) }
	})
	e.issue(models.IssueOpened, nil, "agent")
	e.agentRepliesOnResume()
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	spec, ok := e.sb.Spec(actor)
	require.True(t, ok, "actor created")
	assert.Equal(t, models.ActorSpec{ProjectPath: "krci/test-go-app", IID: key.IID, SessionID: "session-1",
		BotUsername: bot}, spec)

	note, ok := models.StatusNote(e.gl.Notes(key), bot)
	require.True(t, ok)

	v, _ := models.ParseStatusView(note.Body)
	assert.Equal(t, spec.SessionID, v.Session, "the status note names the session the actor was created with")
}

func TestStartCleansPartial(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.sb.AddPartial(actor, true)
	e.agentRepliesOnResume()
	e.handle(intake.Start, dev)
	e.run()
	e.idle()

	ops := e.sb.Ops()
	require.GreaterOrEqual(t, len(ops), 3)
	assert.Equal(t, []string{"delete-actor " + actor, "delete-template " + actor, "create-template " + actor}, ops[:3])
	assert.Contains(t, e.logs.String(), "request=start rule=start-partial-create plan=\"delete!error > create!error",
		"the start runs before the recover request")
	assert.Equal(t, 1, e.sb.Count("create-actor", actor))
	e.assertState(models.Waiting, "")
}

func TestStartCompleteActorNoLabel(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.comment(dev)
	e.reply()
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops(), "no create, no resume")
	e.assertState(models.Waiting, "")
}

func TestStartRefusedOnDone(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Done}, "agent")
	e.run()
	e.handle(intake.Start, dev)
	e.idle()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Equal(t, 1, e.notesWith(models.RefusedMarker), "refused note posted once")
	assert.Empty(t, e.sb.Ops())
	assert.Equal(t, []string{models.StateLabel(models.Done)}, e.stateLabels())
}

func TestReopenWithoutLabelIgnored(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Done}, "bug", models.StateLabel(models.Done))
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Equal(t, 0, e.notesWith(models.RefusedMarker))
	assert.Empty(t, e.gl.Ops())
}

func TestStartRefusedForReporter(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.run()
	e.handle(intake.Start, reporter)
	e.idle()
	e.handle(intake.Start, reporter)
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Equal(t, 1, e.notesWith(models.StartRefusedMarker))
	assert.Equal(t, 1, e.notesWith("Only Developers or higher can start the agent."))
	assert.Empty(t, e.stateLabels())
	_, ok := e.gl.Record(key)
	assert.False(t, ok)
}

func TestStartRefusedNamesTriggerRole(t *testing.T) {
	e := newEnvWith(t, func(c *dispatcher.Config) { c.TriggerRole = models.Maintainer })
	e.gl.SetTriggerRole(models.Maintainer)
	e.issue(models.IssueOpened, nil, "agent")
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops(), "a developer is below the maintainer trigger role")
	assert.Equal(t, 1, e.notesWith("Only Maintainers or higher can start the agent."))
}

func TestStartAllowedForDeveloper(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.agentRepliesOnResume()
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("create-actor", actor))
	assert.Equal(t, 0, e.notesWith(models.StartRefusedMarker))
}

func TestStartAuthorLookupRetried(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.gl.FailCanTriggerNext(errRP, errRP)
	e.agentRepliesOnResume()
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("create-actor", actor))
	assert.Equal(t, 0, e.notesWith(models.StartRefusedMarker))
	e.assertState(models.Waiting, "")
}

func TestStartAuthorLookupErrorNotRefused(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.gl.FailCanTrigger(errors.New("gitlab down"))
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Empty(t, e.gl.Ops(), "a failed lookup is not a refusal")
	assert.Equal(t, 4, e.gl.CanTriggerCalls())
	assert.Contains(t, e.logs.String(), "Cannot check whether the request author can trigger the agent")
}

func TestStartLookupErrorKeepsDecisionWithoutAuthor(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Done}, "agent")
	e.gl.FailCanTrigger(errRP)
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Equal(t, 1, e.notesWith(models.RefusedMarker), "the done refusal does not depend on the author")
}

// --- activate

// waitingIssue: open issue with the trigger label, record waiting, a suspended actor, turn 1 replied to.
func waitingIssue(t *testing.T) *env {
	t.Helper()

	e := newEnv(t)
	e.issue(models.IssueOpened, waiting(), "agent", models.StateLabel(models.Waiting))
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.comment(dev)
	e.reply()

	return e
}

func TestActivateRequiresSuspended(t *testing.T) {
	e := waitingIssue(t)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.comment(dev)
	e.agentRepliesOnResume()
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor), "a running actor is not resumed again")
}

func TestActivateRereadsIssue(t *testing.T) {
	e := waitingIssue(t)
	e.gl.SetIssue(key, models.IssueOpened, "bug")
	e.comment(dev)
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor))
}

func TestActivateIgnoresNonTriggerComment(t *testing.T) {
	e := waitingIssue(t)
	e.comment(reporter)
	e.run()
	e.handle(intake.Activate, reporter)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor))
	e.assertState(models.Waiting, "")
}

func TestActivateStaysResumedOnAnyComment(t *testing.T) {
	e := waitingIssue(t)
	e.comment(dev)

	e.sb.OnResume(func(string) {
		e.reply()
		e.comment(reporter)
	})

	polls := 0

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		polls++
		if polls == 3 {
			e.reply()
		}
	})
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor))
	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	assert.Equal(t, 2, len(markers(e.gl.Notes(key)))-1, "two turns in one activation")
	e.assertState(models.Waiting, "")
}

func markers(notes []models.Note) []models.Note {
	var out []models.Note

	for _, n := range notes {
		if n.Author == bot && strings.Contains(n.Body, "krci-agent-turn") {
			out = append(out, n)
		}
	}

	return out
}

func TestActivateTurnTimeoutPerTurn(t *testing.T) {
	e := waitingIssue(t)
	e.comment(dev)

	polls := 0

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		polls++
		switch polls {
		case 60:
			e.reply()
			e.comment(dev)
		case 120:
			e.reply()
		}
	})
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	e.assertState(models.Waiting, "")
	assert.Equal(t, 1, e.sb.Count("suspend", actor))
}

func TestActivateLimitBeforeResume(t *testing.T) {
	e := waitingIssue(t)

	for range 4 {
		e.comment(dev)
		e.reply()
	}

	e.comment(dev)
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor))
	e.assertState(models.Error, models.ReasonTurnLimit)
}

func TestActivateLimitAfterMarker(t *testing.T) {
	e := waitingIssue(t)

	for range 3 {
		e.comment(dev)
		e.reply()
	}

	e.comment(dev)
	e.sb.OnResume(func(string) {
		e.comment(dev)
		e.reply()
	})
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	e.assertState(models.Error, models.ReasonTurnLimit)
}

func TestActivateNoRoomPostsNote(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.SetNoRoom(true)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.notesWith(models.NoRoomText))
	e.assertState(models.Waiting, "")
	assert.Equal(t, 0, e.clk.count(time.Second), "no room is not retried")

	e.sb.SetNoRoom(false)
	e.agentRepliesOnResume()
	e.comment(dev)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor))
	e.assertState(models.Waiting, "")
}

func TestActivateTimeoutSuspendsAndErrors(t *testing.T) {
	e := waitingIssue(t)
	e.comment(dev)
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	e.assertState(models.Error, models.ReasonTurnTimeout)
}

func TestActivateCrashDetectedOnPoll(t *testing.T) {
	e := waitingIssue(t)
	e.comment(dev)
	e.sb.OnResume(func(string) { e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_CRASHED) })
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("suspend", actor))
	e.assertState(models.Error, models.ReasonActorCrashed)
}

func TestActivateSuspendedByHand(t *testing.T) {
	e := waitingIssue(t)
	e.comment(dev)
	e.sb.OnResume(func(string) { e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED) })
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("suspend", actor))
	e.assertState(models.Waiting, "")
}

// --- stop

func TestStopOrderAndDone(t *testing.T) {
	e := waitingIssue(t)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Working))
	e.run()
	e.handle(intake.Stop, dev)
	e.idle()

	assert.Equal(t, []string{"delete-policy " + actor, "delete-actor " + actor, "delete-template " + actor}, e.sb.Ops(),
		"a running actor is deleted without a suspend")
	e.assertState(models.Done, "")
}

func TestStopAfterRunningActivation(t *testing.T) {
	e := waitingIssue(t)
	e.comment(dev)

	polls := 0

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		polls++
		switch polls {
		case 2:
			e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Working))
			e.handle(intake.Stop, dev)
		case 5:
			e.reply()
			e.comment(dev)
		}
	})
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	ops := e.sb.Ops()

	assert.Less(t, polls, 10, "stop runs after the marker, not at the timeout")
	assert.Equal(t, []string{"resume " + actor, "delete-policy " + actor, "delete-actor " + actor,
		"delete-template " + actor}, ops)
	assert.Len(t, markers(e.gl.Notes(key)), 2, "the turn finished before the stop")
	e.assertState(models.Done, "")
}

func TestStopNotesAgentMROnce(t *testing.T) {
	e := waitingIssue(t)
	e.gl.AddMergeRequest(key.ProjectID, 6, "agent/issue-7", "opened")
	e.run()
	e.idle()
	e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Waiting))
	e.handle(intake.Stop, dev)
	e.idle()
	e.handle(intake.Stop, dev)
	e.idle()

	notes := e.gl.MRNotes(key.ProjectID, 6)
	require.Len(t, notes, 1)
	assert.Equal(t, models.StoppedMRText+"\n"+models.StoppedMRMarker, notes[0].Body)
	assert.Equal(t, bot, notes[0].Author)
	e.assertState(models.Done, "")
}

func TestStopMRNoteFailureStillDone(t *testing.T) {
	e := waitingIssue(t)
	e.gl.AddMergeRequest(key.ProjectID, 6, "agent/issue-7", "opened")
	e.gl.FailMR(errRP, errRP, errRP, errRP)
	e.run()
	e.idle()
	e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Waiting))
	e.handle(intake.Stop, dev)
	e.idle()

	assert.Empty(t, e.gl.MRNotes(key.ProjectID, 6))
	assert.Contains(t, e.logs.String(), "Cannot note the agent's merge request")
	e.assertState(models.Done, "")
}

func TestStopInError(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{
		State:  models.Error,
		Reason: models.ReasonTurnTimeout,
	}, models.StateLabel(models.Error))
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.run()
	e.handle(intake.Stop, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor))
	e.assertState(models.Done, "")
}

func TestStopIgnoredWithoutAgent(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueClosed, nil, "bug")
	e.run()
	e.handle(intake.Stop, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Empty(t, e.gl.Ops())
}

// --- errors

func TestErrorClearedNextCommentActivates(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Error, Reason: models.ReasonTurnTimeout}, "agent")
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.comment(dev)
	e.reply()
	e.agentRepliesOnResume()
	e.run()
	e.idle()
	require.Equal(t, []string{models.StateLabel(models.Error)}, e.stateLabels(), "restart recovery re-adds agent::error")
	e.handle(intake.ClearError, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor))
	e.assertState(models.Waiting, "")

	e.comment(dev)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor))
	e.assertState(models.Waiting, "")
}

func TestCrashedRevertedBeforeResume(t *testing.T) {
	e := waitingIssue(t)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_CRASHED)
	e.comment(dev)
	e.agentRepliesOnResume()
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, []string{"revert " + actor, "resume " + actor, "suspend " + actor}, e.sb.Ops())
	e.assertState(models.Waiting, "")
}

func TestRPCRetryThenSubstrateError(t *testing.T) {
	t.Run("recovers on the last retry", func(t *testing.T) {
		e := waitingIssue(t)
		e.comment(dev)
		e.sb.Fail("resume", errRP, errRP, errRP)
		e.agentRepliesOnResume()
		e.run()
		e.handle(intake.Activate, dev)
		e.idle()

		assert.Equal(t, 1, e.sb.Count("resume", actor))
		assert.Equal(t, []int{1, 1, 1}, []int{
			e.clk.count(time.Second),
			e.clk.count(2 * time.Second),
			e.clk.count(4 * time.Second),
		})
		e.assertState(models.Waiting, "")
	})

	t.Run("fails after three retries", func(t *testing.T) {
		e := waitingIssue(t)
		e.comment(dev)
		e.sb.Fail("resume", errRP, errRP, errRP, errRP)
		e.run()
		e.handle(intake.Activate, dev)
		e.idle()

		assert.Equal(t, 0, e.sb.Count("resume", actor))
		e.assertState(models.Error, models.ReasonSubstrateError)
	})
}

func TestTransitionalRecheckThenActorBusy(t *testing.T) {
	t.Run("stays transitional", func(t *testing.T) {
		e := waitingIssue(t)
		e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDING)
		e.comment(dev)
		e.run()
		e.handle(intake.Activate, dev)
		e.idle()

		assert.Equal(t, 3, e.clk.count(2*time.Second))
		assert.Equal(t, 0, e.sb.Count("resume", actor))
		e.assertState(models.Error, models.ReasonActorBusy)
	})

	t.Run("settles", func(t *testing.T) {
		e := waitingIssue(t)
		e.sb.QueueStates(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDING, ateapi.ActorState_ACTOR_STATE_SUSPENDING)
		e.comment(dev)
		e.agentRepliesOnResume()
		e.run()
		e.handle(intake.Activate, dev)
		e.idle()

		assert.Equal(t, 1, e.sb.Count("resume", actor))
		e.assertState(models.Waiting, "")
	})
}

// --- lanes

func TestConcurrentHandleOneResume(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	burst := func() {
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() { e.handle(intake.Activate, dev) })
		}

		wg.Wait()
	}

	e.sb.OnResume(func(string) {
		burst()
		time.Sleep(10 * time.Millisecond)
		e.reply()
	})
	burst()
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor))
	assert.Equal(t, 1, e.sb.Count("suspend", actor), "one activation at a time")
	e.assertState(models.Waiting, "")
}

func TestPendingCoalesced(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	resumes := 0

	e.sb.OnResume(func(string) {
		resumes++
		if resumes == 1 {
			for range 5 {
				e.handle(intake.Activate, dev)
			}

			time.Sleep(10 * time.Millisecond)
		}

		e.reply()
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor), "pending activates found nothing new to do")
	assert.Equal(t, 1, e.sb.Count("suspend", actor), "one activation at a time")
}

func TestPendingPriorityStopFirst(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	e.sb.OnResume(func(string) {
		e.comment(dev)
		e.handle(intake.Activate, dev)
		e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Working))
		e.handle(intake.Stop, dev)
		e.reply()
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor))
	assert.Equal(t, 1, e.sb.Count("delete-actor", actor))
	e.assertState(models.Done, "")
}

func TestTwoIssuesRunInParallel(t *testing.T) {
	e := waitingIssue(t)
	other := models.IssueKey{ProjectID: 12, IID: 8}
	e.gl.AddIssue(other, "krci/test-go-app", models.IssueOpened, "agent", models.StateLabel(models.Waiting))
	e.gl.AddNote(other, models.Note{
		Author: bot,
		Body:   models.StatusBody(*waiting(), models.StatusView{State: models.Waiting}),
	})
	e.sb.AddComplete(other.ActorName(), ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.run()
	e.idle()
	e.gl.AddNote(other, models.Note{Author: dev, Body: "go"})
	e.comment(dev)

	otherResumed := make(chan struct{})
	timedOut := false

	e.sb.OnResume(func(name string) {
		if name == other.ActorName() {
			close(otherResumed)
			e.gl.AddNote(other, models.Note{Author: bot, Body: "<!-- krci-agent-turn n=1 exit=0 last=999 -->"})

			return
		}

		select {
		case <-otherResumed:
		case <-time.After(3 * time.Second):
			timedOut = true
		}

		e.reply()
	})
	e.handle(intake.Activate, dev)
	e.d.Handle(intake.Request{Key: other, Kind: intake.Activate, Author: dev})
	e.idle()

	assert.False(t, timedOut, "the second issue resumed while the first one was resuming")
	assert.Equal(t, 1, e.sb.Count("resume", actor))
	assert.Equal(t, 1, e.sb.Count("resume", other.ActorName()))
}

// --- restart recovery

func runningIssue(t *testing.T) *env {
	t.Helper()

	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Working}, "agent", models.StateLabel(models.Working))
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.comment(dev)
	e.reply()
	e.comment(dev)

	return e
}

func TestRunRecoversRunningAnswered(t *testing.T) {
	e := runningIssue(t)
	e.reply()
	e.run()
	e.idle()

	assert.Equal(t, []string{"suspend " + actor}, e.sb.Ops())
	e.assertState(models.Waiting, "")
}

func TestRunRecoversRunningUnanswered(t *testing.T) {
	e := runningIssue(t)

	polls := 0

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		polls++
		if polls == 4 {
			e.reply()
		}
	})
	e.run()
	e.idle()

	assert.Equal(t, []string{"suspend " + actor}, e.sb.Ops(), "no second resume")
	assert.GreaterOrEqual(t, polls, 4, "waited for the marker")
	e.assertState(models.Waiting, "")
}

func TestRunRecoveryCoversEveryActor(t *testing.T) {
	e := newEnv(t)

	issues := map[int64]struct {
		state  string
		rec    models.Record
		labels []string
		actor  func(name string)
	}{
		1: {models.IssueOpened, models.Record{State: models.Working}, []string{"agent"},
			func(n string) { e.sb.AddComplete(n, ateapi.ActorState_ACTOR_STATE_RUNNING) }},
		2: {models.IssueOpened, models.Record{State: models.Working}, []string{"agent"},
			func(n string) { e.sb.AddComplete(n, ateapi.ActorState_ACTOR_STATE_SUSPENDED) }},
		3: {models.IssueOpened, models.Record{State: models.Working}, []string{"agent"},
			func(n string) { e.sb.AddComplete(n, ateapi.ActorState_ACTOR_STATE_CRASHED) }},
		4: {models.IssueOpened, models.Record{State: models.Waiting}, []string{"agent"},
			func(n string) { e.sb.AddPartial(n, true) }},
		5: {models.IssueClosed, models.Record{State: models.Waiting}, []string{"agent"},
			func(n string) { e.sb.AddComplete(n, ateapi.ActorState_ACTOR_STATE_SUSPENDED) }},
	}

	for iid, is := range issues {
		k := models.IssueKey{ProjectID: 12, IID: iid}
		e.gl.AddIssue(k, "krci/test-go-app", is.state, is.labels...)
		e.gl.AddNote(k, models.Note{Author: dev, Body: "go"})
		e.gl.AddNote(k, models.Note{Author: bot, Body: models.StatusBody(is.rec, models.StatusView{State: is.rec.State})})
		e.gl.AddNote(k, models.Note{Author: bot, Body: "<!-- krci-agent-turn n=1 exit=0 last=999 -->"})
		is.actor(k.ActorName())
	}

	e.run()
	e.idle()

	want := map[int64]models.Record{
		1: {State: models.Waiting},
		2: {State: models.Waiting},
		3: {State: models.Error, Reason: models.ReasonActorCrashed},
		4: {State: models.Error, Reason: models.ReasonActorMissing},
		5: {State: models.Done},
	}

	for iid, r := range want {
		k := models.IssueKey{ProjectID: 12, IID: iid}
		got, ok := e.gl.Record(k)
		require.True(t, ok, iid)
		assert.Equal(t, r, got, "issue %d", iid)
		assert.Contains(t, e.gl.Labels(k), models.StateLabel(r.State), "issue %d", iid)
	}

	assert.Equal(t, 1, e.sb.Count("suspend", "issue-12-1"))
	assert.Equal(t, 1, e.sb.Count("delete-actor", "issue-12-5"))
	assert.Equal(t, 0, e.sb.Count("resume", "issue-12-2"))
}

func TestIssueDeletedDeletesActor(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.gl.DeleteIssue(key)
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.run()
	e.idle()

	assert.Equal(t, []string{"delete-policy " + actor, "delete-actor " + actor, "delete-template " + actor}, e.sb.Ops())
}

func TestIssueGoneProjectUnreadableKeepsActor(t *testing.T) {
	e := newEnv(t)
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.run()
	e.idle()

	assert.Empty(t, e.sb.Ops(), "project deleted or bot access lost: the actor stays")
	assert.Contains(t, e.logs.String(), "level=ERROR")
	assert.Contains(t, e.logs.String(), "rule=issue-unavailable")
}

func TestIssueGoneProjectErrorKeepsActor(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.gl.DeleteIssue(key)
	e.gl.FailProject(errRP, errRP, errRP, errRP)
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.run()
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Contains(t, e.logs.String(), "level=ERROR")
	assert.Contains(t, e.logs.String(), "rule=issue-unavailable")
}

func TestRunRecoveryBeforeRequests(t *testing.T) {
	e := runningIssue(t)
	e.reply()
	e.handle(intake.Activate, dev)

	assert.False(t, e.d.Idle(), "a request before Run waits as pending")

	e.run()
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor), "recovery continues the running activation; no second resume")
	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	e.assertState(models.Waiting, "")
}

func TestIgnoreRequestDropped(t *testing.T) {
	e := newEnv(t)
	e.run()
	e.handle(intake.Ignore, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Empty(t, e.gl.Ops())
}

// --- failure paths

func TestStaleStopKeepsReenabledAgent(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	polls := 0

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		polls++
		switch polls {
		case 2:
			e.gl.SetIssue(key, models.IssueOpened, models.StateLabel(models.Working))
			e.handle(intake.Stop, dev)
			e.gl.SetIssue(key, models.IssueOpened, "agent", models.StateLabel(models.Working))
			e.handle(intake.Start, dev)
		case 4:
			e.reply()
		}
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("delete-actor", actor), "a stop made stale by the label is not run")
	assert.Equal(t, 0, e.notesWith(models.RefusedMarker))
	e.assertState(models.Waiting, "")
}

func TestInspectOutageOnPlainIssueWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "bug")
	e.run()
	e.idle()
	e.sb.Fail("inspect", errRP, errRP, errRP, errRP)
	e.comment(dev)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Empty(t, e.gl.Ops(), "no status note or error label on an issue without an agent")
}

func TestInspectOutageOnAgentIssueErrors(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()

	before := statusRows(e)

	e.sb.Fail("inspect", errRP, errRP, errRP, errRP)
	e.comment(dev)
	e.handle(intake.Activate, dev)
	e.idle()

	e.assertState(models.Error, models.ReasonSubstrateError)
	assert.Equal(t, before.Updated, statusRows(e).Updated, "an unknown actor keeps the status rows")
	assert.Contains(t, e.logs.String(), "rule=actor-unknown-started")
}

func TestInspectOutageCutByCancelWritesNothing(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.sb.Fail("inspect", errRP, errRP, errRP, errRP)
	e.clk.setOnSleep(func(d time.Duration) {
		if d == time.Second {
			e.cancel()
		}
	})
	e.comment(dev)
	e.handle(intake.Activate, dev)

	require.Eventually(t, func() bool {
		return strings.Contains(e.logs.String(), "Cannot read the actor; dispatcher stopping, left for restart recovery")
	}, 5*time.Second, time.Millisecond)
	e.assertState(models.Waiting, "")
}

func TestCancelDuringRPCWritesNothing(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.Fail("resume", errRP, errRP, errRP, errRP)
	e.clk.setOnSleep(func(d time.Duration) {
		if d == time.Second {
			e.cancel()
		}
	})
	e.handle(intake.Activate, dev)

	require.Eventually(t, func() bool { return e.clk.count(time.Second) == 1 }, 5*time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	e.assertState(models.Waiting, "")
}

func TestLoopTimesOutWhileReadsFail(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	fails := make([]error, 10000)
	for i := range fails {
		fails[i] = errRP
	}

	start := e.clk.Now()

	e.sb.OnResume(func(string) { e.gl.FailLoad(fails...) })
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Less(t, e.clk.Now().Sub(start), 20*time.Minute, "the turn timeout holds while reads fail")
	assert.Equal(t, 1, e.sb.Count("suspend", actor))
}

func TestLoopCancelLeavesActivationRunning(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.clk.setOnSleep(func(d time.Duration) {
		if d == 10*time.Second {
			e.cancel()
		}
	})
	e.handle(intake.Activate, dev)

	require.Eventually(t, func() bool {
		return strings.Contains(e.logs.String(), "Activation left running for restart recovery")
	}, 5*time.Second, time.Millisecond)
	assert.Equal(t, 1, e.sb.Count("resume", actor))
	assert.Equal(t, 0, e.sb.Count("suspend", actor), "the next restart recovery continues the activation")
	e.assertState(models.Working, "")
}

func TestLoopSuspendFailureRecordsSubstrateError(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.OnResume(func(string) {
		e.sb.Fail("suspend", errRP, errRP, errRP, errRP)
		e.reply()
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("suspend", actor))
	e.assertState(models.Error, models.ReasonSubstrateError)
}

func TestLoopIssueDeletedDeletesActor(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.OnResume(func(string) { e.gl.DeleteIssue(key) })
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor))
}

func TestSuspendedActorReadFailureKeepsStatusRows(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.agentRepliesOnResume()
	e.sb.OnSuspend(func(string) { e.sb.Fail("inspect", errRP, errRP, errRP, errRP) })
	e.handle(intake.Activate, dev)
	e.idle()

	rows := statusRows(e)

	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	e.assertState(models.Waiting, "")
	assert.NotEmpty(t, rows.ActorUID, "a read after the suspend that fails every retry keeps the status rows")
	assert.NotEmpty(t, rows.Pod)
}

// rowsOnRecord returns the status rows the issue shows right after each write of a record in state s.
func rowsOnRecord(e *env, s models.AgentState) func() []models.StatusView {
	var (
		mu   sync.Mutex
		seen []models.StatusView
	)

	e.gl.OnWrite(func(_ models.IssueKey, w models.Writes) {
		if w.Record == nil || w.Record.State != s {
			return
		}

		if n, ok := models.StatusNote(e.gl.Notes(key), bot); ok {
			v, _ := models.ParseStatusView(n.Body)

			mu.Lock()
			seen = append(seen, v)
			mu.Unlock()
		}
	})

	return func() []models.StatusView {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(seen)
	}
}

func TestResumedActorReadRetriedForStatusRows(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	working := rowsOnRecord(e, models.Working)

	e.sb.OnResume(func(string) {
		e.sb.Fail("inspect", errRP)
		e.reply()
	})
	e.handle(intake.Activate, dev)
	e.idle()

	rows := working()
	require.Len(t, rows, 1)
	assert.Equal(t, "workers-pod-0", rows[0].Pod, "one failed actor read after the resume is retried")
}

func TestIssueReadRetriedForStatusRows(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	working := rowsOnRecord(e, models.Working)

	e.sb.OnResume(func(string) {
		e.gl.FailLoad(errRP)
		e.reply()
	})
	e.handle(intake.Activate, dev)
	e.idle()

	rows := working()
	require.Len(t, rows, 1)
	assert.Equal(t, "workers-pod-0", rows[0].Pod, "one failed issue read after the resume is retried")
}

func TestSuspendedActorReadRetriedForStatusRows(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	waiting := rowsOnRecord(e, models.Waiting)

	e.agentRepliesOnResume()
	e.sb.OnSuspend(func(string) { e.sb.Fail("inspect", errRP) })
	e.handle(intake.Activate, dev)
	e.idle()

	rows := waiting()
	require.Len(t, rows, 1)
	assert.Equal(t, "uid-1", rows[0].ActorUID)
	assert.Empty(t, rows[0].Pod, "one failed actor read after the suspend is retried")
}

func TestRecoveryRetriesListUntilSuccess(t *testing.T) {
	e := runningIssue(t)
	e.reply()
	e.sb.Fail("list", errRP, errRP, errRP, errRP, errRP, errRP)
	e.run()
	e.idle()

	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	e.assertState(models.Waiting, "")
}

func TestRecoveryRetriesIssueRead(t *testing.T) {
	e := runningIssue(t)
	e.reply()
	e.gl.FailLoad(errRP, errRP, errRP, errRP, errRP)
	e.run()
	e.idle()

	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	e.assertState(models.Waiting, "")
}

func TestCreateRetryAfterPartialStep(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.sb.Fail("create-actor", errRP)
	e.agentRepliesOnResume()
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("create-template", actor))
	assert.Equal(t, 1, e.sb.Count("create-actor", actor))
	assert.Equal(t, 1, e.sb.Count("resume", actor))
	e.assertState(models.Waiting, "")
}

func TestGitLabWriteRetried(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Waiting))
	e.gl.FailWrite(errRP)
	e.handle(intake.Stop, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor))
	e.assertState(models.Done, "")
}

func TestRunningActorWithErrorRecordSuspended(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Error, Reason: models.ReasonSubstrateError}, "agent",
		models.StateLabel(models.Error))
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.run()
	e.idle()

	assert.Equal(t, 1, e.sb.Count("suspend", actor), "an actor in error does not keep its worker")
	e.assertState(models.Error, models.ReasonSubstrateError)
}

func TestNoAccessLookupForDisabledIssue(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "bug")
	e.comment(dev)
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.gl.CanTriggerCalls())
}

func TestRecoveredLoopLogsRecovery(t *testing.T) {
	e := runningIssue(t)

	polls := 0

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		polls++
		if polls == 3 {
			e.reply()
		}
	})
	e.run()
	e.idle()

	assert.Contains(t, e.logs.String(), "Recovered activation")
	assert.NotContains(t, e.logs.String(), "Turn ended")
	assert.Contains(t, e.logs.String(), `plan="loop(since=0,recovered)"`, "decision log names the plan")
}

func TestFreshReadFailureKeepsStatusRows(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	var (
		mu  sync.Mutex
		got models.StatusView
	)

	e.gl.OnWrite(func(_ models.IssueKey, w models.Writes) {
		if w.Record == nil || w.Record.State != models.Working {
			return
		}

		n, _ := models.StatusNote(e.gl.Notes(key), bot)

		mu.Lock()
		got, _ = models.ParseStatusView(n.Body)
		mu.Unlock()
	})
	e.sb.OnResume(func(string) {
		e.gl.FailLoad(errRP)
		e.reply()
	})
	e.handle(intake.Activate, dev)
	e.idle()

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "session-0", got.Session, "a failed fresh read keeps the status rows")
	assert.Equal(t, actor, got.Actor)
}

func TestInvalidSpecRecordsErrorWithoutRetry(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.sb.Fail("create", fmt.Errorf("render actor %s: %w", actor, models.ErrInvalidSpec))
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops(), "an invalid spec is not retried")

	for _, d := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		assert.Equal(t, 0, e.clk.count(d), "no retry backoff")
	}

	e.assertState(models.Error, models.ReasonSubstrateError)
	assert.Contains(t, e.logs.String(), "msg=\"Effect failed\" issue=12/7 request=start effect=create")
}

func TestPostCreateInspectErrorRecordsError(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.sb.Fail("inspect", nil, errRP, errRP, errRP, errRP)
	e.run()
	e.handle(intake.Start, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("create-actor", actor))
	assert.Equal(t, 0, e.sb.Count("resume", actor))
	e.assertState(models.Error, models.ReasonSubstrateError)
}

func TestHandleDuringShutdownNoWaitGroupRace(t *testing.T) {
	for range 200 {
		e := newEnv(t)
		e.issue(models.IssueOpened, nil, "bug")

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)

		go func() { done <- e.d.Run(ctx) }()

		e.idle()

		var wg sync.WaitGroup

		for range 4 {
			wg.Go(func() { e.handle(intake.Activate, dev) })
		}

		cancel()
		wg.Wait()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
}

func TestStopCutAtDeleteStillNotesMR(t *testing.T) {
	e := waitingIssue(t)
	e.gl.AddMergeRequest(key.ProjectID, 6, "agent/issue-7", "opened")
	e.run()
	e.idle()
	e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Waiting))
	e.sb.Fail("delete", errRP, errRP, errRP, errRP)
	e.handle(intake.Stop, dev)
	e.idle()

	assert.Len(t, e.gl.MRNotes(key.ProjectID, 6), 1, "the note does not depend on the end of the stop")
}

func TestIssueGoneProjectUnreadableSuspendsRunningActor(t *testing.T) {
	e := newEnv(t)
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.run()
	e.idle()

	assert.Equal(t, []string{"suspend " + actor}, e.sb.Ops(), "kept, but not holding a worker")
}

func TestIssueDeletedDeletesRunningActorWithoutSuspend(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.gl.DeleteIssue(key)
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.run()
	e.idle()

	assert.Equal(t, []string{"delete-policy " + actor, "delete-actor " + actor, "delete-template " + actor}, e.sb.Ops())
}

// failing returns n copies of err, more than any retry of one call consumes.
func failing(err error, n int) []error { return slices.Repeat([]error{err}, n) }

func TestLoopIssueDeletedDeletesWithoutActorRead(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.OnResume(func(string) {
		e.gl.DeleteIssue(key)
		e.sb.Fail("inspect", failing(errRP, 20)...)
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor), "a deleted issue's actor is deleted even when it cannot be read")
}

func TestRequestOnDeletedIssueDeletesWithoutActorRead(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.gl.DeleteIssue(key)
	e.sb.Fail("inspect", failing(errRP, 20)...)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor))
}

func TestIssueGoneProjectUnreadableActorUnreadableRequestsSuspend(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.OnResume(func(string) {
		e.gl.DeleteIssue(key)
		e.gl.FailProject(failing(errRP, 4)...)
		e.sb.Fail("inspect", failing(errRP, 20)...)
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("delete-actor", actor))
	assert.Equal(t, 1, e.sb.Count("suspend", actor), "an actor that cannot be read is suspended, not left running")
	assert.Contains(t, e.logs.String(), "Cannot read the kept actor; suspend requested")
}

func TestKeptActorSuspendFailureLogged(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.OnResume(func(string) {
		e.gl.DeleteIssue(key)
		e.gl.FailProject(failing(errRP, 4)...)
		e.sb.Fail("suspend", failing(errRP, 4)...)
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("delete-actor", actor))
	assert.Contains(t, e.logs.String(), "Cannot suspend the kept actor")
}

func TestKeptActorMissingIsNotAnError(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.gl.DeleteIssue(key)
	e.gl.FailProject(failing(errRP, 4)...)
	e.sb.Fail("inspect", failing(errRP, 4)...)
	e.handle(intake.Activate, dev)
	e.run()
	e.idle()

	assert.Contains(t, e.logs.String(), "Cannot read the kept actor; suspend requested")
	assert.NotContains(t, e.logs.String(), "Cannot suspend the kept actor", "a suspend of a missing actor is no error")
}

func TestKeptActorLeftOnCancel(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.OnResume(func(string) {
		e.gl.DeleteIssue(key)
		e.gl.FailProject(failing(errRP, 4)...)
		e.sb.Fail("inspect", failing(errRP, 20)...)
		e.clk.setOnSleep(func(d time.Duration) {
			if d == 4*time.Second {
				e.cancel()
			}
		})
	})
	e.handle(intake.Activate, dev)

	require.Eventually(t, func() bool {
		return strings.Contains(e.logs.String(), "Kept actor not read; dispatcher stopping, left for restart recovery")
	}, 5*time.Second, time.Millisecond)
	assert.Equal(t, 0, e.sb.Count("suspend", actor))
	assert.Equal(t, 0, e.sb.Count("delete-actor", actor))
}

func TestRecoveryForbiddenActorUnreadableRequestsSuspend(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.gl.SetForbidden(key.ProjectID)
	e.sb.Fail("inspect", failing(errRP, 4)...)
	e.run()
	e.idle()

	assert.Equal(t, 1, e.sb.Count("suspend", actor), "a forbidden issue's unreadable actor is suspended")
	assert.Equal(t, 0, e.sb.Count("delete-actor", actor))
}

// --- lookup failures

func TestTriggerLookupRetried(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.gl.FailCanTriggerNext(errRP)
	e.agentRepliesOnResume()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor), "a transient lookup failure does not drop the trigger comment")
	e.assertState(models.Waiting, "")
}

func TestTriggerLookupErrorLogged(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.gl.FailCanTrigger(errRP)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor))
	assert.Equal(t, 4, e.gl.CanTriggerCalls())
	assert.Contains(t, e.logs.String(), "level=ERROR")
	assert.Contains(t, e.logs.String(), "Cannot check whether a comment author can trigger the agent")
	e.assertState(models.Waiting, "")
}

// --- stop with Substrate down

func TestStopInspectOutageRecordsError(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.gl.SetIssue(key, models.IssueClosed, "agent", models.StateLabel(models.Waiting))
	e.sb.Fail("inspect", errRP, errRP, errRP, errRP)
	e.handle(intake.Stop, dev)
	e.idle()

	e.assertState(models.Error, models.ReasonSubstrateError)
	assert.NotContains(t, e.logs.String(), "nothing recorded")

	n, _ := models.StatusNote(e.gl.Notes(key), bot)
	v, _ := models.ParseStatusView(n.Body)
	assert.Equal(t, "session-0", v.Session, "the status rows are kept")

	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor), "the next request runs the stop")
	e.assertState(models.Done, "")
}

func TestInspectOutageOnStoppedIssueWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueClosed, &models.Record{State: models.Done}, "agent", models.StateLabel(models.Done))
	e.run()
	e.idle()
	e.sb.Fail("inspect", errRP, errRP, errRP, errRP)
	e.handle(intake.Stop, dev)
	e.idle()

	assert.Empty(t, e.gl.Ops())
}

// --- turn timeout while reads fail

func statusRows(e *env) models.StatusView {
	n, ok := models.StatusNote(e.gl.Notes(key), bot)
	require.True(e.t, ok)

	v, _ := models.ParseStatusView(n.Body)

	return v
}

func TestTurnTimeoutWhileReadsFailKeepsStatusRows(t *testing.T) {
	many := make([]error, 10000)
	for i := range many {
		many[i] = errRP
	}

	t.Run("issue reads fail", func(t *testing.T) {
		e := waitingIssue(t)
		e.run()
		e.idle()
		e.comment(dev)

		polls := 0

		e.clk.setOnSleep(func(d time.Duration) {
			if polls++; d == 10*time.Second && polls == 1 {
				e.gl.FailLoad(many...)
			}
		})
		e.handle(intake.Activate, dev)
		e.idle()

		e.assertState(models.Error, models.ReasonTurnTimeout)

		v := statusRows(e)
		assert.Equal(t, "session-0", v.Session)
		assert.Equal(t, "uid-1", v.ActorUID)
	})

	t.Run("actor reads fail", func(t *testing.T) {
		e := waitingIssue(t)
		e.run()
		e.idle()
		e.comment(dev)

		polls := 0

		e.clk.setOnSleep(func(d time.Duration) {
			if polls++; d == 10*time.Second && polls == 1 {
				e.sb.Fail("inspect", many...)
			}
		})
		e.handle(intake.Activate, dev)
		e.idle()

		e.assertState(models.Error, models.ReasonTurnTimeout)

		v := statusRows(e)
		assert.Equal(t, "session-0", v.Session)
		assert.Equal(t, "uid-1", v.ActorUID)
	})
}

// --- restart recovery load

// waitingIssues adds n settled issues: record waiting, a suspended actor, nothing to do.
func waitingIssues(e *env, n int, state ateapi.ActorState) []models.IssueKey {
	keys := make([]models.IssueKey, 0, n)

	for i := range n {
		k := models.IssueKey{ProjectID: 12, IID: int64(100 + i)}
		rec := models.Record{State: models.Waiting}

		e.gl.AddIssue(k, "krci/test-go-app", models.IssueOpened, "agent", models.StateLabel(models.Waiting))
		e.gl.AddNote(k, models.Note{Author: bot, Body: models.StatusBody(rec, models.StatusView{State: rec.State})})
		e.sb.AddComplete(k.ActorName(), state)

		keys = append(keys, k)
	}

	return keys
}

func TestRecoveryReadsBounded(t *testing.T) {
	e := newEnv(t)
	waitingIssues(e, 40, ateapi.ActorState_ACTOR_STATE_SUSPENDED)

	var (
		mu            sync.Mutex
		inFlight, top int
	)

	e.gl.OnLoad(func(models.IssueKey) {
		mu.Lock()
		inFlight++
		top = max(top, inFlight)
		mu.Unlock()

		time.Sleep(2 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
	})
	e.run()
	e.idle()

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, 40, e.gl.LoadCalls(), "every actor is recovered")
	assert.LessOrEqual(t, top, dispatcher.RecoveryConcurrency)
}

func TestRecoveryForbiddenProjectKeepsAndReleases(t *testing.T) {
	e := newEnv(t)

	forbidden := make([]models.IssueKey, 0, dispatcher.RecoveryConcurrency)
	for i := range dispatcher.RecoveryConcurrency {
		forbidden = append(forbidden, models.IssueKey{ProjectID: 13, IID: int64(100 + i)})
	}

	others := []models.IssueKey{{ProjectID: 12, IID: 100}, {ProjectID: 12, IID: 101}}

	rec := models.Record{State: models.Error, Reason: models.ReasonSubstrateError}

	for _, k := range append(slices.Clone(forbidden), others...) {
		e.gl.AddIssue(k, "krci/test-go-app", models.IssueOpened, "agent", models.StateLabel(models.Error))
		e.gl.AddNote(k, models.Note{Author: bot, Body: models.StatusBody(rec, models.StatusView{State: rec.State})})
		e.sb.AddComplete(k.ActorName(), ateapi.ActorState_ACTOR_STATE_RUNNING)
	}

	e.gl.SetForbidden(13)
	e.run()
	e.idle()

	for _, k := range forbidden {
		assert.Equal(t, 1, e.sb.Count("suspend", k.ActorName()), "a forbidden issue's actor is suspended")
		assert.Equal(t, 0, e.sb.Count("delete-actor", k.ActorName()), "a forbidden issue's actor is kept")
	}

	for _, k := range others {
		assert.Equal(t, 1, e.sb.Count("suspend", k.ActorName()), "the other actors are recovered")
	}

	assert.Equal(t, dispatcher.RecoveryConcurrency, strings.Count(e.logs.String(),
		"rule=issue-unavailable"))
	assert.Equal(t, 0, e.clk.count(time.Second), "a forbidden read is not retried")
}

func TestRecoveredActivationsDoNotHoldRecovery(t *testing.T) {
	e := newEnvWith(t, func(c *dispatcher.Config) { c.TurnTimeout = 1000 * time.Hour })
	keys := waitingIssues(e, 2*dispatcher.RecoveryConcurrency, ateapi.ActorState_ACTOR_STATE_RUNNING)

	var (
		mu    sync.Mutex
		polls = map[models.IssueKey]int{}
	)

	e.gl.OnLoad(func(k models.IssueKey) {
		mu.Lock()
		polls[k]++
		mu.Unlock()
	})
	e.run()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		for _, k := range keys {
			if polls[k] < 2 {
				return false
			}
		}

		return true
	}, 5*time.Second, time.Millisecond, "every recovered activation polls while the others run")

	for _, k := range keys {
		e.gl.AddNote(k, models.Note{Author: bot, Body: "<!-- krci-agent-turn n=1 exit=0 last=0 -->"})
	}

	e.idle()

	for _, k := range keys {
		assert.Equal(t, 1, e.sb.Count("suspend", k.ActorName()))
	}
}

// --- GitLab reads of an activation

func TestActivationPollsWithoutFullReads(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	polls := 0

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		if polls++; polls == 30 {
			e.reply()
		}
	})

	before := e.gl.LoadCalls()

	e.handle(intake.Activate, dev)
	e.idle()

	assert.LessOrEqual(t, e.gl.LoadCalls()-before, 5, "full reads do not grow with the number of polls")
	assert.GreaterOrEqual(t, e.gl.RefreshCalls(), 28)
	assert.Equal(t, 0, e.gl.BlindWrites(), "every write names the view it follows")
	e.assertState(models.Waiting, "")
}

func TestPollDecisionTakenOnFullRead(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)

	var (
		polls   int
		deleted int64
	)

	e.clk.setOnSleep(func(d time.Duration) {
		if d != 10*time.Second {
			return
		}

		switch polls++; polls {
		case 2:
			deleted = e.gl.AddNote(key, models.Note{Author: reporter, Body: "never mind"})
		case 4:
			e.gl.DeleteNote(key, deleted)
		case 5:
			e.reply()
		}
	})
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Less(t, polls, 10, "a comment deleted before the reply does not keep the actor resumed")
	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	e.assertState(models.Waiting, "")
}

// --- clear error and recovery creates

func TestClearErrorMissingActorCreates(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Error, Reason: models.ReasonActorMissing}, "agent")
	e.agentRepliesOnResume()
	e.run()
	e.handle(intake.ClearError, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("create-actor", actor))
	assert.Equal(t, 0, e.notesWith(models.StartRefusedMarker))
	e.assertState(models.Waiting, "")
}

func TestClearErrorMissingActorRefusedForReporter(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Error, Reason: models.ReasonActorMissing}, "agent")
	e.run()
	e.handle(intake.ClearError, reporter)
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Equal(t, 1, e.notesWith(models.StartRefusedMarker))
	e.assertState(models.Error, models.ReasonActorMissing)
}

func TestClearErrorLookupErrorNotRefused(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Error, Reason: models.ReasonActorMissing}, "agent")
	e.gl.FailCanTrigger(errRP)
	e.run()
	e.handle(intake.ClearError, dev)
	e.idle()

	assert.Empty(t, e.sb.Ops())
	assert.Equal(t, 0, e.notesWith(models.StartRefusedMarker))
	assert.Equal(t, []string{models.StateLabel(models.Error)}, e.stateLabels())
	assert.Equal(t, models.Record{State: models.Error, Reason: models.ReasonActorMissing}, e.record())
	assert.Contains(t, e.logs.String(), "Cannot check whether the request author can trigger the agent")
}

func TestClearErrorAtTurnLimitKeepsError(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Error, Reason: models.ReasonTurnLimit}, "agent")
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)

	for range 5 {
		e.comment(dev)
		e.reply()
	}

	e.handle(intake.ClearError, dev)
	e.run()
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor))
	assert.Contains(t, e.logs.String(),
		`request=clear-error rule=clear-error-turn-limit plan="write(record=error/turn-limit,label=error)!log"`)
	e.assertState(models.Error, models.ReasonTurnLimit)

	e.comment(dev)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 0, e.sb.Count("resume", actor))
	e.assertState(models.Error, models.ReasonTurnLimit)
}

func TestRecoveryCompletesPartialCreate(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, nil, "agent")
	e.sb.AddPartial(actor, true)
	e.agentRepliesOnResume()
	e.run()
	e.idle()

	assert.Equal(t, 1, e.sb.Count("create-policy", actor))
	assert.Equal(t, 1, e.sb.Count("resume", actor))
	assert.Equal(t, 0, e.gl.CanTriggerCalls(), "a recover request has no author")
	e.assertState(models.Waiting, "")
}

// --- sweep

const sweepEvery = 7 * time.Minute

// sweepEnv is a waiting issue with a suspended actor, the sweep on, Run started and restart recovery done.
func sweepEnv(t *testing.T, mod func(*dispatcher.Config)) (*env, *sweepGate) {
	t.Helper()

	e := newEnvWith(t, func(c *dispatcher.Config) {
		c.SweepInterval = sweepEvery
		mod(c)
	})
	e.issue(models.IssueOpened, waiting(), "agent", models.StateLabel(models.Waiting))
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	e.comment(dev)
	e.reply()

	g := e.sweepGate()
	e.run()
	g.started()
	e.idle()

	return e, g
}

// sweepGate holds the sweep loop in its sleep; sweep runs one sweep and waits for its end.
type sweepGate struct {
	t                  *testing.T
	parked, tick, stop chan struct{}
}

// sweepGate installs the gate. Call it before run, so no sweep runs before the gate.
func (e *env) sweepGate() *sweepGate {
	g := &sweepGate{t: e.t, parked: make(chan struct{}), tick: make(chan struct{}), stop: make(chan struct{})}

	e.clk.setOnSleep(func(d time.Duration) {
		if d != sweepEvery {
			return
		}

		select {
		case g.parked <- struct{}{}:
		case <-g.stop:
			return
		}

		select {
		case <-g.tick:
		case <-g.stop:
		}
	})

	return g
}

// started waits for the loop's first sleep. Call it after run: its cleanup releases the loop before Run stops.
func (g *sweepGate) started() {
	g.t.Cleanup(func() { close(g.stop) })
	g.wait("start")
}

func (g *sweepGate) sweep() {
	g.tick <- struct{}{}

	g.wait("finish a sweep")
}

func (g *sweepGate) wait(step string) {
	select {
	case <-g.parked:
	case <-time.After(5 * time.Second):
		g.t.Fatalf("sweep loop did not %s", step)
	}
}

func noChange(*dispatcher.Config) {}

func TestSweepRecoversRunningActorWithoutLane(t *testing.T) {
	e, g := sweepEnv(t, noChange)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)

	g.sweep()
	assert.NotContains(t, e.logs.String(), "Sweep: actor without an issue lane", "one sweep is not enough")

	g.sweep()
	e.idle()

	assert.Contains(t, e.logs.String(), "Sweep: actor without an issue lane; recover requested")
	assert.Equal(t, 1, e.sb.Count("suspend", actor), "the recovered activation ends with a suspend")
}

func TestSweepNeedsTwoSweepsInARow(t *testing.T) {
	e, g := sweepEnv(t, noChange)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)

	g.sweep()
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)
	g.sweep()
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	g.sweep()
	e.idle()

	assert.NotContains(t, e.logs.String(), "Sweep: actor without an issue lane")
	assert.Equal(t, 0, e.sb.Count("suspend", actor))
}

func TestSweepSkipsActorWithLane(t *testing.T) {
	e, g := sweepEnv(t, func(c *dispatcher.Config) { c.TurnTimeout = 1000 * time.Hour })
	e.comment(dev)
	e.handle(intake.Activate, dev)
	require.Eventually(t, func() bool { return e.sb.Count("resume", actor) == 1 }, 5*time.Second, time.Millisecond)

	g.sweep()
	g.sweep()
	g.sweep()

	require.False(t, e.d.Idle(), "the activation still runs on its lane")
	assert.NotContains(t, e.logs.String(), "Sweep: actor without an issue lane", "an activation in progress has a lane")
}

func TestSweepSkipsSuspendedAndCrashedActors(t *testing.T) {
	e, g := sweepEnv(t, noChange)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_CRASHED)

	g.sweep()
	g.sweep()
	e.idle()

	assert.NotContains(t, e.logs.String(), "Sweep: actor without an issue lane", "a crashed actor holds no worker")
}

func TestSweepListFailureKeepsPreviousSweep(t *testing.T) {
	e, g := sweepEnv(t, noChange)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)

	g.sweep()
	e.sb.Fail("list", errRP)
	g.sweep()
	assert.Contains(t, e.logs.String(), "Sweep: cannot list actors")

	g.sweep()
	e.idle()

	assert.Contains(t, e.logs.String(), "Sweep: actor without an issue lane; recover requested")
}

func TestSweepRequestsAgainWhileActorStaysWithoutLane(t *testing.T) {
	e, g := sweepEnv(t, noChange)
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.sb.Fail("suspend", failing(errRP, 100)...)

	g.sweep()
	g.sweep()
	e.idle()
	g.sweep()
	e.idle()

	assert.Equal(t, 2, strings.Count(e.logs.String(), "Sweep: actor without an issue lane"),
		"an actor that stays without a lane is requested again at the next sweep")
}

func TestRecoveryStopsRunningActorOfDoneIssue(t *testing.T) {
	e := newEnv(t)
	e.issue(models.IssueOpened, &models.Record{State: models.Done}, "agent", models.StateLabel(models.Done))
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.run()
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor), "a done issue keeps no running actor")
	assert.Contains(t, e.logs.String(), "rule=done-running-stop")
	e.assertState(models.Done, "")
}

func TestUnavailableIssueKeepsRunningActorSuspended(t *testing.T) {
	for name, fail := range map[string]func(e *env){
		"forbidden": func(e *env) { e.gl.SetForbidden(key.ProjectID) },
		"failing":   func(e *env) { e.gl.FailLoad(failing(errRP, 4)...) },
	} {
		t.Run(name, func(t *testing.T) {
			e := waitingIssue(t)
			e.run()
			e.idle()
			e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
			fail(e)
			e.handle(intake.Activate, dev)
			e.idle()

			assert.Equal(t, 1, e.sb.Count("suspend", actor), "an unavailable issue keeps its actor, suspended")
			assert.Equal(t, 0, e.sb.Count("delete-actor", actor))
			assert.Contains(t, e.logs.String(), "rule=issue-unavailable")
		})
	}
}

func TestGoneIssueDiscardsActor(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.gl.DeleteIssue(key)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("delete-actor", actor))
	assert.Contains(t, e.logs.String(), "rule=issue-gone")
	assert.Contains(t, e.logs.String(), "rule=issue-gone plan=delete!abort")
}

func TestTransientIssueReadIsRetried(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.sb.SetState(actor, ateapi.ActorState_ACTOR_STATE_RUNNING)
	e.gl.FailLoad(errRP)
	e.handle(intake.Activate, dev)
	e.idle()

	assert.NotContains(t, e.logs.String(), "rule=issue-unavailable", "one failed read is retried before it counts")
	assert.Contains(t, e.logs.String(), "rule=running-continue")
}

func TestUnknownActorSkipsTriggerLookups(t *testing.T) {
	e := waitingIssue(t)
	e.run()
	e.idle()
	e.comment(dev)
	e.sb.Fail("inspect", errRP, errRP, errRP, errRP)

	calls := e.gl.CanTriggerCalls()

	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, calls, e.gl.CanTriggerCalls(), "an unknown actor needs no trigger lookup")
	assert.Contains(t, e.logs.String(), "rule=actor-unknown-started")
}
