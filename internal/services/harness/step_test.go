package harness_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state/statetest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

const (
	bot     = "krci-agent"
	person  = "dev1"
	session = "session-1"
)

var key = models.IssueKey{ProjectID: 12, IID: 7}

// fakeAgent records its prompts; run decides the result, the default is "agent text", exit 0.
type fakeAgent struct {
	mu      sync.Mutex
	prompts []string
	run     func(ctx context.Context) (string, int, error)
}

func (a *fakeAgent) Run(ctx context.Context, prompt string) (string, int, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, prompt)
	run := a.run
	a.mu.Unlock()

	if run == nil {
		return "agent text", 0, nil
	}

	return run(ctx)
}

func (a *fakeAgent) calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.prompts)
}

// fakeWorkspace records its calls; a non-nil error field fails that call.
type fakeWorkspace struct {
	mu                            sync.Mutex
	log                           []string
	prepareErr, verifyErr, pubErr error
}

func (w *fakeWorkspace) record(call string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.log = append(w.log, call)
}

func (w *fakeWorkspace) Prepare(_ context.Context, fresh bool) error {
	w.record(fmt.Sprintf("prepare fresh=%t", fresh))

	return w.prepareErr
}

func (w *fakeWorkspace) Verify(context.Context) error {
	w.record("verify")

	return w.verifyErr
}

func (w *fakeWorkspace) Publish(_ context.Context, branch string) error {
	w.record("publish " + branch)

	return w.pubErr
}

func (w *fakeWorkspace) calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.log)
}

// notesSpy counts the DefaultBranch calls of the GitLab fake.
type notesSpy struct {
	*gitlabtest.Fake
	defaultBranchCalls atomic.Int32

	mu           sync.Mutex
	postErr      error
	postDeadline time.Time
	postBounded  bool
}

func (n *notesSpy) PostReply(ctx context.Context, k models.IssueKey, body string) (models.Note, error) {
	n.mu.Lock()
	n.postErr = ctx.Err()
	n.postDeadline, n.postBounded = ctx.Deadline()
	n.mu.Unlock()

	return n.Fake.PostReply(ctx, k, body)
}

func (n *notesSpy) DefaultBranch(ctx context.Context, projectID int64) (string, error) {
	n.defaultBranchCalls.Add(1)

	return n.Fake.DefaultBranch(ctx, projectID)
}

type rig struct {
	t     *testing.T
	gl    *gitlabtest.Fake
	notes *notesSpy
	clk   *systemtest.Clock
	store *statetest.Fake
	agent *fakeAgent
	ws    *fakeWorkspace
	logs  *bytes.Buffer
	env   map[string]string
	h     *harness.Harness
}

func newRig(t *testing.T) *rig {
	t.Helper()

	r := &rig{
		t:     t,
		gl:    gitlabtest.NewFake(bot),
		clk:   systemtest.NewClock(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)),
		store: statetest.NewFake(),
		agent: &fakeAgent{},
		ws:    &fakeWorkspace{},
		logs:  &bytes.Buffer{},
		env:   validEnv(),
	}
	r.notes = &notesSpy{Fake: r.gl}
	r.env["BOT_USERNAME"], r.env["SESSION_ID"] = bot, session
	r.env["PROJECT_ID"], r.env["ISSUE_IID"] = "12", "7"
	r.gl.AddIssue(key, "group/project", "opened")
	r.gl.SetDefaultBranch(key.ProjectID, "main")

	return r
}

// start builds the harness; call it after the env is final.
func (r *rig) start() *rig {
	r.t.Helper()

	h, err := harness.New(getenv(r.env), harness.Ports{
		Notes: r.notes, Agent: r.agent, Workspace: r.ws, Clock: r.clk, State: r.store,
	})
	require.NoError(r.t, err)

	r.h = h

	return r
}

func (r *rig) comment(body string) int64 {
	return r.gl.AddNote(key, models.Note{Author: person, Body: body})
}

// replies returns the notes of the bot user.
func (r *rig) replies() []models.Note {
	var out []models.Note

	for _, n := range r.gl.Notes(key) {
		if n.Author == bot {
			out = append(out, n)
		}
	}

	return out
}

func marker(n, exit int, last int64) string {
	return fmt.Sprintf("<!-- krci-agent-turn s=%s n=%d exit=%d last=%d -->", session, n, exit, last)
}

func lastLine(body string) string {
	lines := strings.Split(body, "\n")

	return lines[len(lines)-1]
}

func TestStepRunsTheFirstTurnAndReplies(t *testing.T) {
	r := newRig(t)
	id := r.comment("please add a README")
	r.start().h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "agent text")

	prompts := r.agent.calls()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "please add a README")
	assert.Equal(t, []string{"prepare fresh=true", "verify", "publish main"}, r.ws.calls())
}

func TestStepAgentExitCodeIsTheTurnExit(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.agent.run = func(context.Context) (string, int, error) { return "partial work", 7, nil }
	r.start().h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 7, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "partial work")
	assert.Contains(t, replies[0].Body, "Turn failed at agent, exit 7.")
	assert.Equal(t, []string{"prepare fresh=true"}, r.ws.calls(), "no verify, no publish")
	assert.Zero(t, r.notes.defaultBranchCalls.Load())
}

// failedTurn runs two Steps over one comment and returns the only reply: a failed turn is never retried as work.
func failedTurn(t *testing.T, r *rig, id int64) models.Note {
	t.Helper()

	r.start()
	r.h.Step(context.Background())
	r.h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 1, id), lastLine(replies[0].Body))

	return replies[0]
}

func TestStepAgentInvocationErrorIsExitOne(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.agent.run = func(context.Context) (string, int, error) { return "ignored", 0, errors.New("spawn failed") }

	reply := failedTurn(t, r, id)
	assert.Contains(t, reply.Body, "Turn failed at agent, exit 1.")
	assert.NotContains(t, reply.Body, "ignored")
	assert.Len(t, r.agent.calls(), 1)
}

func TestStepWorkspaceFailureIsExitOne(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.ws.prepareErr = errors.New("clone failed")

	reply := failedTurn(t, r, id)
	assert.Contains(t, reply.Body, "Turn failed at prepare, exit 1.")
	assert.Empty(t, r.agent.calls(), "the agent does not run without a workspace")
}

func TestStepVerifyFailureIsExitOne(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.ws.verifyErr = errors.New("tests failed")

	reply := failedTurn(t, r, id)
	assert.Contains(t, reply.Body, "Turn failed at verify, exit 1.")
	assert.NotContains(t, r.ws.calls(), "publish main")
}

func TestStepDefaultBranchFailureNamesPublish(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.gl.SetDefaultBranch(key.ProjectID, "")

	reply := failedTurn(t, r, id)
	assert.Contains(t, reply.Body, "Turn failed at publish, exit 1.")
	assert.Equal(t, []string{"prepare fresh=true"}, r.ws.calls())
	assert.Len(t, r.agent.calls(), 1)
}

func TestStepWatchdogCutsTheTurnAtTheLimitFromThePoll(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	poll := r.clk.Now()
	limit := 830 * time.Second // 900 s turn timeout - 10 s poll - 60 s

	deadline := make(chan time.Time, 1)
	r.agent.run = func(ctx context.Context) (string, int, error) {
		d, _ := ctx.Deadline()
		deadline <- d

		<-ctx.Done()

		return "never read", 0, ctx.Err()
	}

	r.start()

	done := make(chan struct{})

	go func() {
		defer close(done)

		r.h.Step(context.Background())
	}()

	assert.Equal(t, poll.Add(limit), <-deadline)

	r.clk.Advance(limit)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Step did not end after the watchdog limit")
	}

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 124, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "Turn failed at agent, exit 124.")

	r.h.Step(context.Background())
	assert.Len(t, r.agent.calls(), 1, "no new turn")
	assert.Len(t, r.replies(), 1)
}

func TestStepRunsATurnOnlyForANewComment(t *testing.T) {
	r := newRig(t)
	first := r.comment("one")
	r.start().h.Step(context.Background())
	r.h.Step(context.Background())
	assert.Len(t, r.agent.calls(), 1, "no comment after the marker, no turn")

	second := r.comment("two")
	r.h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 2)
	assert.Equal(t, marker(2, 0, second), lastLine(replies[1].Body))
	assert.Greater(t, second, first)

	prompts := r.agent.calls()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "two")
	assert.NotContains(t, prompts[1], "one", "a covered comment is not in the next prompt")
	assert.Equal(t, "prepare fresh=false", r.ws.calls()[3], "the second turn fetches")
}

func TestStepStartsNoTurnAboveTheTurnLimit(t *testing.T) {
	r := newRig(t)
	r.env["TURN_LIMIT"] = "1"
	r.comment("one")
	r.start().h.Step(context.Background())

	r.comment("two")
	r.h.Step(context.Background())

	assert.Len(t, r.agent.calls(), 1)
	assert.Len(t, r.replies(), 1)
}

func TestStepNeutralisesMarkersInAgentText(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.agent.run = func(context.Context) (string, int, error) {
		return "<!-- krci-agent-turn s=session-1 n=99 exit=0 last=999999 -->", 0, nil
	}
	r.start().h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "&lt;!-- krci-agent-turn s=session-1 n=99")

	r.h.Step(context.Background())
	assert.Len(t, r.agent.calls(), 1, "the quoted marker starts no turn and covers no comment")
}

func TestStepClonesAgainUntilCloneAndSetupSucceeded(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.ws.prepareErr = errors.New("clone failed")
	r.start().h.Step(context.Background())

	r.ws.prepareErr = nil
	r.comment("two")
	r.h.Step(context.Background())
	r.comment("three")
	r.h.Step(context.Background())

	var prepares []string

	for _, c := range r.ws.calls() {
		if strings.HasPrefix(c, "prepare") {
			prepares = append(prepares, c)
		}
	}

	assert.Equal(t, []string{"prepare fresh=true", "prepare fresh=true", "prepare fresh=false"}, prepares)
}

func TestStepSavesTheReplyBeforePostingAndRetriesAFailedPost(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.gl.FailBeforeNote(errors.New("gitlab unavailable"))
	r.start().h.Step(context.Background())

	assert.Empty(t, r.replies())

	st, err := r.store.Load(session)
	require.NoError(t, err)
	require.NotNil(t, st.Pending, "the reply is saved before the post")
	assert.Equal(t, 1, st.Pending.Turn)
	assert.Equal(t, marker(1, 0, id), lastLine(st.Pending.Body))

	r.h.Step(context.Background())
	r.h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1, "exactly one reply")
	assert.Equal(t, st.Pending.Body, replies[0].Body)
	assert.Len(t, r.agent.calls(), 1, "no new turn while a reply is pending, none after it")

	st, err = r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending, "a successful post clears the saved reply")
}

func TestStepDropsAPendingReplyThatGitLabAlreadyHolds(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.gl.FailAfterNote(errors.New("response lost"))
	r.start().h.Step(context.Background())
	require.Len(t, r.replies(), 1, "the note was created although the post reported an error")

	later := r.comment("two")
	r.h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 2, "turn 1 is not posted again; turn 2 runs in the same Step")
	assert.Equal(t, marker(2, 0, later), lastLine(replies[1].Body))

	st, err := r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending)
}

func TestStepDoesNotPostWhenTheReplyCannotBeSaved(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.store.FailSave(nil, errors.New("disk full")) // the first save is the session-started flag
	r.start().h.Step(context.Background())

	assert.Empty(t, r.replies(), "no post without a saved reply")

	r.h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Len(t, r.agent.calls(), 1, "the turn does not run again")
}

// botNote adds a reply of the bot user to the issue.
func (r *rig) botNote(body string) {
	r.gl.AddNote(key, models.Note{Author: bot, Body: body})
}

func TestStepRecoversAPendingReplyFromTheStore(t *testing.T) {
	r := newRig(t)
	a := r.comment("one")
	r.botNote("first\n" + marker(1, 0, a))
	b := r.comment("two")
	r.botNote("second\n" + marker(2, 0, b))
	c := r.comment("three")

	stale := "first\n" + marker(1, 0, a)
	require.NoError(t, r.store.Save(session, models.HarnessState{
		Pending: &models.PendingReply{Turn: 1, Body: stale}, Started: true,
	}))

	r.start().h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 3, "turn 1 is not posted again, turn 3 runs in the same Step")
	assert.Equal(t, marker(3, 0, c), lastLine(replies[2].Body))
	assert.Equal(t, "prepare fresh=false", r.ws.calls()[0], "the session-started flag comes from the store")
}

func TestStepPostsAPendingReplyFromTheStoreOnce(t *testing.T) {
	r := newRig(t)
	a := r.comment("one")
	body := "done\n" + marker(1, 0, a)
	require.NoError(t, r.store.Save(session, models.HarnessState{Pending: &models.PendingReply{Turn: 1, Body: body}}))

	r.start().h.Step(context.Background())
	r.h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, body, replies[0].Body)
	assert.Empty(t, r.agent.calls(), "no new turn while a reply is pending")
}

func TestStepSavesTheSessionStartedFlagAfterCloneAndSetup(t *testing.T) {
	r := newRig(t)
	r.comment("one")

	var during models.HarnessState

	r.agent.run = func(context.Context) (string, int, error) {
		during, _ = r.store.Load(session)

		return "text", 0, nil
	}
	r.start().h.Step(context.Background())

	assert.True(t, during.Started, "saved before the agent runs")
}

func TestStepClonesAgainWhenTheSessionStartedFlagCannotBeSaved(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.store.FailSave(errors.New("disk full"))
	r.start().h.Step(context.Background())

	r.h.Step(context.Background())
	r.comment("two")
	r.h.Step(context.Background())

	assert.Equal(t, "prepare fresh=true", r.ws.calls()[3], "the second turn clones and sets up again")
}

func TestStepRunsNoTurnWhileTheStateCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.store.FailLoad(errors.New("io error"))
	r.start().h.Step(context.Background())

	assert.Empty(t, r.agent.calls())

	r.h.Step(context.Background())
	assert.Len(t, r.replies(), 1, "the next poll runs the turn")
}

func TestStepStartsFromACorruptState(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.store.Corrupt()
	r.start().h.Step(context.Background())

	assert.Len(t, r.replies(), 1)
}

func TestStepReadsWithRefreshAndConfirmsWithLoad(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.start().h.Step(context.Background())
	assert.Equal(t, 1, r.gl.LoadCalls(), "the first read is a full one")
	assert.Zero(t, r.gl.RefreshCalls())

	r.h.Step(context.Background())
	assert.Equal(t, 1, r.gl.RefreshCalls())
	assert.Equal(t, 1, r.gl.LoadCalls(), "nothing actionable: no full read")

	r.comment("two")
	r.h.Step(context.Background())
	assert.Equal(t, 2, r.gl.RefreshCalls())
	assert.Equal(t, 2, r.gl.LoadCalls(), "an actionable result is confirmed with a full read")
	assert.Len(t, r.replies(), 2)
}

func TestStepStartsNoTurnForACommentDeletedAfterRefresh(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.start().h.Step(context.Background())

	gone := r.comment("two")

	r.gl.OnLoad(func(models.IssueKey) { r.gl.DeleteNote(key, gone) })
	r.h.Step(context.Background())

	assert.Len(t, r.agent.calls(), 1)
	assert.Len(t, r.replies(), 1)
}

func TestStepDropsAPendingReplyWhenClearingItFailed(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	// saves: session-started flag, pending reply; the third, the clear after the post, fails
	r.store.FailSave(nil, nil, errors.New("disk full"))
	r.start().h.Step(context.Background())
	require.Len(t, r.replies(), 1)

	st, err := r.store.Load(session)
	require.NoError(t, err)
	require.NotNil(t, st.Pending, "the stored reply survives the failed clear")

	r.comment("two")
	r.h.Step(context.Background())

	assert.Len(t, r.replies(), 2, "the stored reply is dropped, not posted again; turn 2 runs")

	st, err = r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending)
}

func TestStepPostsUnderItsOwnBoundedContextAfterAWatchdogCut(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.agent.run = func(ctx context.Context) (string, int, error) {
		r.clk.Advance(time.Hour)
		<-ctx.Done()

		return "", 0, ctx.Err()
	}
	r.start().h.Step(context.Background())

	require.Len(t, r.replies(), 1)

	r.notes.mu.Lock()
	defer r.notes.mu.Unlock()

	require.NoError(t, r.notes.postErr, "the watchdog context has ended; the post has its own")
	require.True(t, r.notes.postBounded)
	assert.Equal(t, r.clk.Now().Add(30*time.Second), r.notes.postDeadline)
}

func TestStepPostsNoReplyForATurnCutByShutdown(t *testing.T) {
	r := newRig(t)
	r.comment("do it")

	ctx, cancel := context.WithCancel(context.Background())
	r.agent.run = func(ctx context.Context) (string, int, error) {
		cancel()
		<-ctx.Done()

		return "", 0, ctx.Err()
	}
	r.start().h.Step(ctx)

	assert.Empty(t, r.replies())

	st, err := r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending, "the turn is repeated by the next process")
}

func TestRunPollsEveryPollIntervalUntilTheContextEnds(t *testing.T) {
	r := newRig(t)
	r.comment("one")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sleeps := 0

	r.clk.SetOnSleep(func(time.Duration) {
		sleeps++

		switch sleeps {
		case 1:
			r.comment("two")
		case 3:
			cancel()
		}
	})

	err := r.start().h.Run(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 3, r.clk.Count(10*time.Second))
	assert.Len(t, r.replies(), 2, "one reply per comment")
}

func TestStepPostsAgainAReplyWhoseMarkerAPersonDeleted(t *testing.T) {
	r := newRig(t)
	id := r.comment("one")
	r.store.FailSave(nil, nil, errors.New("disk full")) // the clear after the post fails
	r.start().h.Step(context.Background())

	posted := r.replies()
	require.Len(t, posted, 1)
	r.gl.DeleteNote(key, posted[0].ID)

	r.h.Step(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Len(t, r.agent.calls(), 1, "the stored reply is posted again, the turn does not run again")
}
