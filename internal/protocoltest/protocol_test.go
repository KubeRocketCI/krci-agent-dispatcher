package protocoltest_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

func TestTurnEndsWithReplyAndSuspend(t *testing.T) {
	e := newEnv(t)
	first := e.comment("add a README")
	e.run()
	e.handle(intake.Start)
	e.idle()

	assert.Equal(t, []string{"create-template " + actor, "create-actor " + actor, "create-policy " + actor,
		"resume " + actor, "suspend " + actor}, e.sb.Ops())
	assert.Equal(t, models.Suspended, e.actorState())

	replies := e.replies()
	require.Len(t, replies, 1)
	assert.Contains(t, replies[0].Body, marker("session-1", 1, first))
	assert.Contains(t, e.agent.Prompts()[0], "add a README")
	assert.Equal(t, []string{"prepare fresh=true", "verify", "publish main"}, e.ws.Calls())
	e.assertState(models.Waiting)
}

func TestCommentDuringTurnRunsNextTurn(t *testing.T) {
	e := newEnv(t)
	first := e.comment("add a README")

	var second int64

	e.agent.OnTurn = func(call int) {
		if call == 1 {
			second = e.comment("and a LICENSE")
		}
	}
	e.run()
	e.handle(intake.Start)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor), "the actor stays resumed between the turns")
	assert.Equal(t, 1, e.sb.Count("suspend", actor))
	assert.Equal(t, models.Suspended, e.actorState())

	replies := e.replies()
	require.Len(t, replies, 2)
	assert.Contains(t, replies[0].Body, marker("session-1", 1, first))
	assert.Contains(t, replies[1].Body, marker("session-1", 2, second))

	prompts := e.agent.Prompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "and a LICENSE")
	assert.NotContains(t, prompts[1], "add a README")
	e.assertState(models.Waiting)
}

func TestReEnableStartsNewSessionOverEveryComment(t *testing.T) {
	e := newEnv(t)
	first := e.comment("add a README")
	e.run()
	e.handle(intake.Start)
	e.idle()

	second := e.comment("and a LICENSE")
	e.handle(intake.Activate)
	e.idle()
	require.Len(t, e.replies(), 2)

	statusBefore, ok := models.StatusNote(e.gl.Notes(key), bot)
	require.True(t, ok)

	e.gl.SetIssue(key, models.IssueOpened, agentLabels.State(models.Waiting))
	e.handle(intake.Stop)
	e.idle()

	assert.Equal(t, models.None, e.actorState())
	e.assertState(models.Done)

	e.gl.SetIssue(key, models.IssueOpened, agentLabels.Trigger(), agentLabels.State(models.Done))
	e.handle(intake.Start)
	e.idle()

	assert.Equal(t, 2, e.sb.Count("create-actor", actor))

	spec, ok := e.sb.Spec(actor)
	require.True(t, ok)
	assert.Equal(t, "session-2", spec.SessionID)

	replies := e.replies()
	require.Len(t, replies, 3, "the old replies are kept")
	assert.Contains(t, replies[0].Body, marker("session-1", 1, first))
	assert.Contains(t, replies[1].Body, marker("session-1", 2, second))
	assert.Contains(t, replies[2].Body, marker("session-2", 1, second), "old session markers do not count")

	prompts := e.agent.Prompts()
	require.Len(t, prompts, 3)
	assert.Contains(t, prompts[2], "add a README")
	assert.Contains(t, prompts[2], "and a LICENSE")
	assert.Equal(t, []string{
		"prepare fresh=true", "verify", "publish main",
		"prepare fresh=false", "verify", "publish main",
		"prepare fresh=true", "verify", "publish main",
	}, e.ws.Calls(), "the new session clones again")

	status, ok := models.StatusNote(e.gl.Notes(key), bot)
	require.True(t, ok)

	assert.Equal(t, statusBefore.ID, status.ID, "the status note is kept")

	view, _ := models.ParseStatusView(status.Body)
	assert.Equal(t, "session-2", view.Session)
	assert.Equal(t, models.Suspended, e.actorState())
	e.assertState(models.Waiting)
}

func TestFailedPostIsDroppedOnResume(t *testing.T) {
	e := newEnv(t)
	first := e.comment("add a README")
	e.agent.OnTurn = func(call int) {
		if call == 1 {
			e.gl.FailAfterNote(errors.New("rpc unavailable"))
		}
	}
	e.run()
	e.handle(intake.Start)
	e.idle()

	require.Len(t, e.replies(), 1, "the reply is saved although the post failed")
	assert.Equal(t, models.Suspended, e.actorState(), "the dispatcher found the marker")
	e.assertState(models.Waiting)

	second := e.comment("and a LICENSE")
	e.handle(intake.Activate)
	e.idle()

	replies := e.replies()
	require.Len(t, replies, 2, "one reply per turn of the session")
	assert.Contains(t, replies[0].Body, marker("session-1", 1, first))
	assert.Contains(t, replies[1].Body, marker("session-1", 2, second))

	prompts := e.agent.Prompts()
	require.Len(t, prompts, 2, "turn 1 does not run again")
	assert.Contains(t, prompts[1], "and a LICENSE")
	assert.NotContains(t, prompts[1], "add a README")
	assert.Equal(t, 2, e.sb.Count("resume", actor))
	assert.Equal(t, models.Suspended, e.actorState())
	e.assertState(models.Waiting)
}

func TestUnpostedReplyIsPostedOnTheNextPoll(t *testing.T) {
	e := newEnv(t)
	first := e.comment("add a README")
	e.agent.OnTurn = func(call int) {
		if call == 1 {
			e.gl.FailBeforeNote(errors.New("rpc unavailable"))
		}
	}

	var (
		mu    sync.Mutex
		saved []int
	)

	e.clk.SetOnSleep(func(d time.Duration) {
		e.onSleep(d)

		if d == poll {
			mu.Lock()
			saved = append(saved, len(e.replies()))
			mu.Unlock()
		}
	})
	e.run()
	e.handle(intake.Start)
	e.idle()

	mu.Lock()
	defer mu.Unlock()

	require.GreaterOrEqual(t, len(saved), 2)
	assert.Equal(t, []int{0, 1}, saved[:2], "no reply after the failed post, the reply on the next poll")

	replies := e.replies()
	require.Len(t, replies, 1, "one reply per session and turn")
	assert.Contains(t, replies[0].Body, marker("session-1", 1, first))
	assert.Len(t, e.agent.Prompts(), 1, "the turn does not run again")
	assert.Equal(t, models.Suspended, e.actorState())
	e.assertState(models.Waiting)
}
