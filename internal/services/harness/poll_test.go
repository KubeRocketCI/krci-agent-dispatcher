package harness_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness/harnesstest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state/statetest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

const (
	bot     = "krci-agent"
	person  = "dev1"
	session = "session-1"
)

var key = models.IssueKey{ProjectID: 12, IID: 7}

// gitlabSpy counts the DefaultBranch calls of the GitLab fake. With onBlockDefaultBranch set, DefaultBranch calls it,
// then harnesstest.Await; defaultBranchNotCut reports harnesstest.ErrNotCut. onBlockMR and mrNotCut do the same for
// EnsureMergeRequest; mrErr fails it.
type gitlabSpy struct {
	*gitlabtest.Fake
	defaultBranchCalls   atomic.Int32
	onBlockDefaultBranch func()
	defaultBranchNotCut  atomic.Bool
	onBlockMR            func()
	mrNotCut             atomic.Bool
	mrErr                error

	mu           sync.Mutex
	postErr      error
	postDeadline time.Time
	postBounded  bool
}

func (n *gitlabSpy) PostReply(ctx context.Context, k models.IssueKey, body string) (models.Note, error) {
	n.mu.Lock()
	n.postErr = ctx.Err()
	n.postDeadline, n.postBounded = ctx.Deadline()
	n.mu.Unlock()

	return n.Fake.PostReply(ctx, k, body)
}

func (n *gitlabSpy) DefaultBranch(ctx context.Context, projectID int64) (string, error) {
	n.defaultBranchCalls.Add(1)

	if n.onBlockDefaultBranch != nil {
		n.onBlockDefaultBranch()

		err := harnesstest.Await(ctx)
		n.defaultBranchNotCut.Store(errors.Is(err, harnesstest.ErrNotCut))

		return "", err
	}

	return n.Fake.DefaultBranch(ctx, projectID)
}

func (n *gitlabSpy) EnsureMergeRequest(ctx context.Context, projectID int64, mr models.NewMergeRequest) error {
	if n.onBlockMR != nil {
		n.onBlockMR()

		err := harnesstest.Await(ctx)
		n.mrNotCut.Store(errors.Is(err, harnesstest.ErrNotCut))

		return err
	}

	if n.mrErr != nil {
		return n.mrErr
	}

	return n.Fake.EnsureMergeRequest(ctx, projectID, mr)
}

type rig struct {
	t     *testing.T
	gl    *gitlabtest.Fake
	spy   *gitlabSpy
	clk   *systemtest.Clock
	store *statetest.Fake
	agent *harnesstest.Agent
	ws    *harnesstest.Workspace
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
		agent: &harnesstest.Agent{},
		ws:    &harnesstest.Workspace{},
		logs:  &bytes.Buffer{},
		env:   validEnv(),
	}
	r.spy = &gitlabSpy{Fake: r.gl}
	r.env["BOT_USERNAME"], r.env["SESSION_ID"] = bot, session
	r.env["PROJECT_ID"], r.env["ISSUE_IID"] = "12", "7"
	r.gl.AddIssue(key, "group/project", "opened")
	r.gl.SetDefaultBranch(key.ProjectID, "main")

	return r
}

// start builds the harness; call it after the env is final.
func (r *rig) start() *rig {
	r.t.Helper()

	cfg, err := harness.Handshake(getenv(r.env))
	require.NoError(r.t, err)

	r.h = harness.New(cfg, harness.Ports{
		GitLab: r.spy, Agent: r.agent, Workspace: r.ws, Clock: r.clk, State: r.store,
		Log: slog.New(slog.NewTextHandler(r.logs, nil)),
	})

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

func TestPollRunsTheFirstTurnAndReplies(t *testing.T) {
	r := newRig(t)
	r.gl.SetDefaultBranch(key.ProjectID, "trunk")
	r.gl.SetIssueText(key, "Add a README", "")
	id := r.comment("please add a README")
	r.start().h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "agent run 1")

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "please add a README")
	assert.Equal(t, []string{"prepare fresh=true", "new-commits check", "verify", "push"}, r.ws.Calls())

	mrs := r.gl.MergeRequests(key.ProjectID)
	require.Len(t, mrs, 1)
	assert.Equal(t, "agent/issue-7", mrs[0].Source)
	assert.Equal(t, "trunk", mrs[0].Target, "the merge request targets the project's default branch")
	assert.Equal(t, `Draft: Resolve "Add a README"`, mrs[0].Title)
	assert.Equal(t, "Closes #7", mrs[0].Description)
}

func TestPollMergeRequestTitleQuotesTheRawIssueTitle(t *testing.T) {
	r := newRig(t)
	r.gl.SetIssueText(key, `Fix "x" in C:\tmp`, "")
	r.comment("do it")
	r.start().h.Poll(context.Background())

	mrs := r.gl.MergeRequests(key.ProjectID)
	require.Len(t, mrs, 1)
	assert.Equal(t, `Draft: Resolve "Fix "x" in C:\tmp"`, mrs[0].Title)
}

func TestPollWithoutNewCommitsSkipsVerifyAndPublish(t *testing.T) {
	r := newRig(t)
	id := r.comment("what does the build do?")
	r.ws.NoNewCommits = true
	r.start().h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, "agent run 1\n"+marker(1, 0, id), replies[0].Body, "an answer without commits is a done turn")
	assert.Equal(t, []string{"prepare fresh=true", "new-commits check"}, r.ws.Calls())
	assert.Empty(t, r.gl.MergeRequests(key.ProjectID))
	assert.Zero(t, r.spy.defaultBranchCalls.Load(), "no default branch read without a branch to open a merge request for")
}

// A merge request the person closed stays closed while the agent pushes nothing new.
func TestPollWithoutNewCommitsLeavesAClosedMergeRequestClosed(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.start().h.Poll(context.Background())

	mrs := r.gl.MergeRequests(key.ProjectID)
	require.Len(t, mrs, 1)
	r.gl.SetMergeRequestState(key.ProjectID, mrs[0].IID, "closed")

	r.ws.NoNewCommits = true
	r.comment("why did you change the handler?")
	r.h.Poll(context.Background())

	require.Len(t, r.replies(), 2)
	mrs = r.gl.MergeRequests(key.ProjectID)
	require.Len(t, mrs, 1, "no new merge request")
	assert.Equal(t, "closed", mrs[0].State)
}

// A merge request that failed after the push opens on the next turn, which has no new commits, also after a restart
// of the harness.
func TestPollRetriesAFailedMergeRequestOnTheNextTurn(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.spy.mrErr = errors.New("gitlab POST merge_requests: status 502")
	r.start().h.Poll(context.Background())

	require.Len(t, r.replies(), 1)
	assert.Equal(t, marker(1, 1, id), lastLine(r.replies()[0].Body))
	require.Empty(t, r.gl.MergeRequests(key.ProjectID))

	r.spy.mrErr = nil
	r.ws.NoNewCommits = true
	next := r.comment("and now?")
	r.start().h.Poll(context.Background())

	require.Len(t, r.replies(), 2)
	assert.Equal(t, marker(2, 0, next), lastLine(r.replies()[1].Body))
	assert.Len(t, r.gl.MergeRequests(key.ProjectID), 1)

	r.comment("thanks")
	r.h.Poll(context.Background())
	assert.Len(t, r.gl.MergeRequests(key.ProjectID), 1, "an opened merge request is not ensured again")
	assert.Equal(t, int32(2), r.spy.defaultBranchCalls.Load(), "the default branch is read in turns 1 and 2 only")
}

// The first prompt of a new conversation has the issue and every comment, also when turn 1 never reached the agent.
func TestPollNewConversationAfterAFailedFirstTurnGetsTheIssue(t *testing.T) {
	r := newRig(t)
	r.gl.SetIssueText(key, "Add a README", "Describe the build steps.")
	r.comment("please start")
	r.ws.PrepareErr = errors.New("clone failed")
	r.start().h.Poll(context.Background())
	require.Empty(t, r.agent.Prompts(), "turn 1 never reached the agent")

	r.ws.PrepareErr = nil
	r.comment("please retry")
	r.h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)

	for _, want := range []string{"Add a README", "Describe the build steps.", "please start", "please retry"} {
		assert.Contains(t, prompts[0], want)
	}

	r.comment("and the license")
	r.h.Poll(context.Background())

	prompts = r.agent.Prompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "and the license")
	assert.NotContains(t, prompts[1], "Add a README", "a resumed conversation gets only the new comments")
}

func TestPollTellsTheAgentAboutSetAsideCommits(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.ws.Prepared = harness.Prepared{SetAside: "agent/issue-7-unpushed-1"}
	r.start().h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "agent/issue-7-unpushed-1")
}

func TestPollTellsTheAgentAboutAbortedOperationsAndStashedChanges(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.ws.Prepared = harness.Prepared{Aborted: []string{"cherry-pick"}, Stashed: "0123abcd"}
	r.start().h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "The unfinished cherry-pick in the checkout was aborted.")
	assert.Contains(t, prompts[0], "git stash apply 0123abcd")
}

func TestPollTellsTheAgentAboutUntrackedPaths(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.ws.Untracked = []string{"a", "b", "c", "d", "e", "f", "g"}
	r.start().h.Poll(context.Background())

	r.comment("publish failed, please push your work")
	r.h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 2)
	assert.NotContains(t, prompts[0], "Untracked paths", "a fresh clone names no untracked paths")
	assert.Contains(t, prompts[1], "Untracked paths in the checkout: a, b, c, d, e and 2 more.")
	assert.Contains(t, prompts[1], "Publish fails on untracked paths while the issue branch has unpushed commits")
}

func TestPollPromptWithoutPrepareNotes(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.start().h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)
	assert.NotContains(t, prompts[0], "Harness:")
}

func TestPollBranchFailureNamesPublish(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.ws.NewCommitsCheckErr = errors.New("new-commits check: tracked files changed")

	reply := failedTurn(t, r, id)
	assert.Equal(t, "agent run 1\n\nTurn failed at publish, exit 1.\n"+marker(1, 1, id), reply.Body)
	assert.Equal(t, []string{"prepare fresh=true", "new-commits check"}, r.ws.Calls(), "no verify, no push")
}

func TestPollLogsTheErrorOfAFailedTurn(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.ws.VerifyErr = errors.New("verify: exit status 1: FAIL TestHandler")
	r.start().h.Poll(context.Background())

	assert.Contains(t, r.logs.String(), "Turn failed")
	assert.Contains(t, r.logs.String(), "phase=verify")
	assert.Contains(t, r.logs.String(), "verify: exit status 1: FAIL TestHandler")
	assert.NotContains(t, r.replies()[0].Body, "FAIL TestHandler", "the error stays out of the reply")
	assert.Empty(t, logLines(r.logs.String(), "Turn done"), "a failed turn is not done")
}

func TestPollLogsTheEndOfADoneTurn(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.start().h.Poll(context.Background())

	done := logLines(r.logs.String(), "Turn done")
	require.Len(t, done, 1)
	assertAttrs(t, done[0], "level=INFO", "turn=1")
	assert.Empty(t, logLines(r.logs.String(), "Turn failed"))
}

// logLines returns the log lines whose message is msg.
func logLines(logs, msg string) []string {
	var out []string

	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, ` msg="`+msg+`"`) {
			out = append(out, line)
		}
	}

	return out
}

// assertAttrs asserts that the log line holds each key=value attribute.
func assertAttrs(t *testing.T, line string, attrs ...string) {
	t.Helper()

	fields := strings.Fields(line)
	for _, a := range attrs {
		assert.Contains(t, fields, a, "line %q", line)
	}
}

func TestPollLogsTheStartOfEachTurnAndWhetherTheAgentRunStartsTheSession(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.start().h.Poll(context.Background())

	r.comment("and the tests")
	r.h.Poll(context.Background())

	started := logLines(r.logs.String(), "Turn started")
	require.Len(t, started, 2)
	assertAttrs(t, started[0], "level=INFO", "turn=1")
	assertAttrs(t, started[1], "level=INFO", "turn=2")

	runs := logLines(r.logs.String(), "Agent run started")
	require.Len(t, runs, 2)
	assertAttrs(t, runs[0], "level=INFO", "turn=1", "new_session=true")
	assertAttrs(t, runs[1], "level=INFO", "turn=2", "new_session=false")
}

func TestPollStartsTheSessionAgainAfterARunThatEndedBeforeIt(t *testing.T) {
	r := newRig(t)
	r.gl.SetIssueText(key, "Add a README", "The README names the project.")
	r.comment("please add a README")
	r.agent.NoInit = func(call int) bool { return call == 1 }
	r.agent.Result = func(_ context.Context, call int) (string, int, error) {
		if call == 1 {
			return "", 1, nil
		}

		return "done", 0, nil
	}
	r.start().h.Poll(context.Background())

	r.comment("try again")
	r.h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "Issue: Add a README")
	assert.Contains(t, prompts[1], "The README names the project.")
	assert.Contains(t, prompts[1], "please add a README", "a new session gets every comment")
	assert.Contains(t, prompts[1], "try again")

	runs := logLines(r.logs.String(), "Agent run started")
	require.Len(t, runs, 2)
	assertAttrs(t, runs[1], "turn=2", "new_session=true")
}

func TestPollLogsNoAgentRunForATurnThatFailsInPrepare(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.ws.PrepareErr = errors.New("clone: exit status 128")
	r.start().h.Poll(context.Background())

	logs := r.logs.String()
	assert.Len(t, logLines(logs, "Turn started"), 1)
	assert.Empty(t, logLines(logs, "Agent run started"))
	assert.Len(t, logLines(logs, "Turn failed"), 1)
}

func TestPollLogsNoEndOfATurnCutByShutdown(t *testing.T) {
	r := newRig(t)
	r.comment("do it")

	ctx, cancel := context.WithCancel(context.Background())
	r.agent.Result = func(ctx context.Context, _ int) (string, int, error) {
		cancel()
		<-ctx.Done()

		return "", 0, ctx.Err()
	}
	r.start().h.Poll(ctx)

	logs := r.logs.String()
	assert.Len(t, logLines(logs, "Turn started"), 1)
	assert.Empty(t, logLines(logs, "Turn done"))
	assert.Empty(t, logLines(logs, "Turn failed"))
}

// GitLab refuses a merge request title above 255 characters.
func TestPollMergeRequestTitleFitsGitLab(t *testing.T) {
	r := newRig(t)
	r.gl.SetIssueText(key, strings.Repeat("é", 300), "")
	r.comment("do it")
	r.start().h.Poll(context.Background())

	mrs := r.gl.MergeRequests(key.ProjectID)
	require.Len(t, mrs, 1)
	assert.Equal(t, 255, utf8.RuneCountInString(mrs[0].Title))
	assert.True(t, strings.HasPrefix(mrs[0].Title, `Draft: Resolve "éé`), mrs[0].Title)
	assert.True(t, strings.HasSuffix(mrs[0].Title, `é…"`), mrs[0].Title)
}

func TestPollMergeRequestFailureNamesPublish(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.spy.mrErr = errors.New("gitlab POST merge_requests: status 500")

	reply := failedTurn(t, r, id)
	assert.Equal(t, "agent run 1\n\nTurn failed at publish, exit 1.\n"+marker(1, 1, id), reply.Body)
}

func TestPollFirstPromptHasTheIssueTitleAndDescription(t *testing.T) {
	r := newRig(t)
	r.gl.SetIssueText(key, "Add a README", "Describe the build steps.")
	r.comment("please start")
	r.start().h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "Add a README")
	assert.Contains(t, prompts[0], "Describe the build steps.")
	assert.Contains(t, prompts[0], "please start")

	r.comment("and the license")
	r.h.Poll(context.Background())

	prompts = r.agent.Prompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "and the license")
	assert.NotContains(t, prompts[1], "Add a README")
	assert.NotContains(t, prompts[1], "Describe the build steps.")
	assert.NotContains(t, prompts[1], "please start")
}

func TestPollFirstPromptWithoutCommentsHasTheIssueTitleAndDescription(t *testing.T) {
	r := newRig(t)
	r.gl.SetIssueText(key, "Add a README", "Describe the build steps.")
	r.start().h.Poll(context.Background())

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "Add a README")
	assert.Contains(t, prompts[0], "Describe the build steps.")
}

func TestPollAgentExitCodeIsTheTurnExit(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.agent.Result = func(context.Context, int) (string, int, error) { return "partial work", 7, nil }
	r.start().h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 7, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "partial work")
	assert.Contains(t, replies[0].Body, "Turn failed at agent, exit 7.")
	assert.Equal(t, []string{"prepare fresh=true"}, r.ws.Calls(), "no verify, no publish")
	assert.Zero(t, r.spy.defaultBranchCalls.Load())
}

// failedTurn runs two polls over one comment and returns the only reply: a failed turn is never retried as work.
func failedTurn(t *testing.T, r *rig, id int64) models.Note {
	t.Helper()

	r.start()
	r.h.Poll(context.Background())
	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 1, id), lastLine(replies[0].Body))

	return replies[0]
}

func TestPollAgentInvocationErrorIsExitOne(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.agent.Result = func(context.Context, int) (string, int, error) { return "ignored", 0, errors.New("spawn failed") }

	reply := failedTurn(t, r, id)
	assert.Contains(t, reply.Body, "Turn failed at agent, exit 1.")
	assert.NotContains(t, reply.Body, "ignored")
	assert.Len(t, r.agent.Prompts(), 1)
}

func TestPollWorkspaceFailureIsExitOne(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.ws.PrepareErr = errors.New("clone failed")

	reply := failedTurn(t, r, id)
	assert.Contains(t, reply.Body, "Turn failed at prepare, exit 1.")
	assert.Empty(t, r.agent.Prompts(), "the agent does not run without a workspace")
}

func TestPollVerifyFailureIsExitOne(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.ws.VerifyErr = errors.New("tests failed")

	reply := failedTurn(t, r, id)
	assert.Equal(t, "agent run 1\n\nTurn failed at verify, exit 1.\n"+marker(1, 1, id), reply.Body,
		"the agent text, the status line, the marker")
	assert.NotContains(t, r.ws.Calls(), "push")
	assert.Empty(t, r.gl.MergeRequests(key.ProjectID))
}

func TestPollPublishFailureKeepsTheAgentText(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.ws.PushErr = errors.New("push rejected")

	reply := failedTurn(t, r, id)
	assert.Equal(t, "agent run 1\n\nTurn failed at publish, exit 1.\n"+marker(1, 1, id), reply.Body)
	assert.Empty(t, r.gl.MergeRequests(key.ProjectID), "no merge request without the push")
}

func TestPollDefaultBranchFailureNamesPublish(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.gl.SetDefaultBranch(key.ProjectID, "")

	reply := failedTurn(t, r, id)
	assert.Equal(t, "agent run 1\n\nTurn failed at publish, exit 1.\n"+marker(1, 1, id), reply.Body,
		"the agent text, the status line, the marker")
	assert.Equal(t, []string{"prepare fresh=true", "new-commits check", "verify", "push"}, r.ws.Calls(),
		"the default branch is read for the merge request, after the push")
	assert.Len(t, r.agent.Prompts(), 1)
}

func TestPollWatchdogCutsTheTurnAtTheLimitFromThePoll(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	poll := r.clk.Now()
	limit := 830 * time.Second // 900 s turn timeout - 10 s poll - 60 s

	deadline := make(chan time.Time, 1)
	r.agent.Result = func(ctx context.Context, _ int) (string, int, error) {
		d, _ := ctx.Deadline()
		deadline <- d

		<-ctx.Done()

		return "never read", 0, ctx.Err()
	}

	r.start()

	done := make(chan struct{})

	go func() {
		defer close(done)

		r.h.Poll(context.Background())
	}()

	assert.Equal(t, poll.Add(limit), <-deadline)

	r.clk.Advance(limit)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not end after the watchdog limit")
	}

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 124, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "Turn failed at agent, exit 124.")

	r.h.Poll(context.Background())
	assert.Len(t, r.agent.Prompts(), 1, "no new turn")
	assert.Len(t, r.replies(), 1)
}

func TestPollWatchdogCutsEveryPhase(t *testing.T) {
	cases := []struct {
		name    string
		phase   string
		block   func(*rig, func())
		prompts int
	}{
		{"prepare", "prepare", func(r *rig, at func()) { r.ws.BlockPrepare, r.ws.OnBlock = true, at }, 0},
		{"default branch read", "publish", func(r *rig, at func()) { r.spy.onBlockDefaultBranch = at }, 1},
		{"new-commits check", "publish", func(r *rig, at func()) { r.ws.BlockNewCommitsCheck, r.ws.OnBlock = true, at }, 1},
		{"verify", "verify", func(r *rig, at func()) { r.ws.BlockVerify, r.ws.OnBlock = true, at }, 1},
		{"push", "publish", func(r *rig, at func()) { r.ws.BlockPush, r.ws.OnBlock = true, at }, 1},
		{"merge request", "publish", func(r *rig, at func()) { r.spy.onBlockMR = at }, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			id := r.comment("do it")
			c.block(r, func() { r.clk.Advance(830 * time.Second) })
			r.start().h.Poll(context.Background())

			replies := r.replies()
			require.Len(t, replies, 1)
			assert.Equal(t, marker(1, 124, id), lastLine(replies[0].Body))
			assert.Contains(t, replies[0].Body, "Turn failed at "+c.phase+", exit 124.")
			assert.Len(t, r.agent.Prompts(), c.prompts)
			assert.False(t, r.spy.defaultBranchNotCut.Load(), "the watchdog cuts the default branch read")
			assert.False(t, r.spy.mrNotCut.Load(), "the watchdog cuts the merge request call")
			assert.False(t, r.ws.NotCut(), "the watchdog cuts the workspace call")

			r.h.Poll(context.Background())
			assert.Len(t, r.replies(), 1, "no new turn")
			assert.Len(t, r.agent.Prompts(), c.prompts)
		})
	}
}

// An adapter cut at TERM returns before the deadline with an error that wraps context.DeadlineExceeded; an adapter
// cut at KILL may return any error once the deadline passed. Both are exit 124. A plain error before the deadline is
// exit 1.
func TestPollWatchdogMapsCutCallsTo124(t *testing.T) {
	cases := []struct {
		name  string
		after time.Duration // from the poll, before the call returns
		err   error
		exit  int
	}{
		{"cut at TERM, wraps DeadlineExceeded", 800 * time.Second,
			fmt.Errorf("agent terminated: %w", context.DeadlineExceeded), 124},
		{"cut at KILL, any error after the deadline", 830 * time.Second, errors.New("signal: killed"), 124},
		{"plain error before the deadline", 800 * time.Second, errors.New("agent crashed"), 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			id := r.comment("do it")
			r.agent.Result = func(context.Context, int) (string, int, error) {
				r.clk.Advance(c.after)
				return "", 0, c.err
			}
			r.start().h.Poll(context.Background())

			replies := r.replies()
			require.Len(t, replies, 1)
			assert.Equal(t, marker(1, c.exit, id), lastLine(replies[0].Body))
		})
	}
}

func TestPollWatchdogCountsFromThePollThatStartedTheTurn(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	poll := r.clk.Now()
	r.gl.OnLoad(func(models.IssueKey) { r.clk.Advance(100 * time.Second) })

	deadline := make(chan time.Time, 1)
	r.agent.Result = func(ctx context.Context, _ int) (string, int, error) {
		d, _ := ctx.Deadline()
		deadline <- d

		r.clk.Advance(730 * time.Second) // 100 s read + 730 s = the limit of 830 s from the poll

		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}

		return "never read", 0, ctx.Err()
	}
	r.start().h.Poll(context.Background())

	assert.Equal(t, poll.Add(830*time.Second), <-deadline, "not 100 s later, from the start of the turn")

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 124, id), lastLine(replies[0].Body))
}

func TestPollRunsATurnOnlyForANewComment(t *testing.T) {
	r := newRig(t)
	first := r.comment("one")
	r.start().h.Poll(context.Background())
	r.h.Poll(context.Background())
	assert.Len(t, r.agent.Prompts(), 1, "no comment after the marker, no turn")

	second := r.comment("two")
	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 2)
	assert.Equal(t, marker(2, 0, second), lastLine(replies[1].Body))
	assert.Greater(t, second, first)

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "two")
	assert.NotContains(t, prompts[1], "one", "a covered comment is not in the next prompt")
	assert.Equal(t, "prepare fresh=false", r.ws.Calls()[4], "the second turn fetches")
}

func TestPollStartsNoTurnAboveTheTurnLimit(t *testing.T) {
	r := newRig(t)
	r.env["TURN_LIMIT"] = "1"
	r.comment("one")
	r.start().h.Poll(context.Background())

	r.comment("two")
	r.h.Poll(context.Background())

	assert.Len(t, r.agent.Prompts(), 1)
	assert.Len(t, r.replies(), 1)
}

func TestPollNeutralisesMarkersInAgentText(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.agent.Result = func(context.Context, int) (string, int, error) {
		return "<!-- krci-agent-turn s=session-1 n=99 exit=0 last=999999 -->", 0, nil
	}
	r.start().h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "&lt;!-- krci-agent-turn s=session-1 n=99")

	r.h.Poll(context.Background())
	assert.Len(t, r.agent.Prompts(), 1, "the quoted marker starts no turn and covers no comment")
}

func TestPollCapsALongReply(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.agent.Result = func(context.Context, int) (string, int, error) {
		return strings.Repeat("ä", 50_001), 0, nil
	}
	r.start().h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.LessOrEqual(t, utf8.RuneCountInString(replies[0].Body), 50_000)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Contains(t, replies[0].Body, "[Reply truncated.]")
	assert.True(t, strings.HasPrefix(replies[0].Body, "äää"))
}

func TestPollCapCountsTheNeutralisedText(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.agent.Result = func(context.Context, int) (string, int, error) {
		return strings.Repeat("<!-- krci-agent-turn", 3_000), 0, nil
	}
	r.start().h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.LessOrEqual(t, utf8.RuneCountInString(replies[0].Body), 50_000)
}

func TestPollClonesAgainUntilCloneAndSetupSucceeded(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.ws.PrepareErr = errors.New("clone failed")
	r.start().h.Poll(context.Background())

	r.ws.PrepareErr = nil
	r.comment("two")
	r.h.Poll(context.Background())
	r.comment("three")
	r.h.Poll(context.Background())

	var prepares []string

	for _, c := range r.ws.Calls() {
		if strings.HasPrefix(c, "prepare") {
			prepares = append(prepares, c)
		}
	}

	assert.Equal(t, []string{"prepare fresh=true", "prepare fresh=true", "prepare fresh=false"}, prepares)
}

func TestPollSavesTheReplyBeforePostingAndRetriesAFailedPost(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.gl.FailBeforeNote(errors.New("gitlab unavailable"))
	r.start().h.Poll(context.Background())

	assert.Empty(t, r.replies())

	st, err := r.store.Load(session)
	require.NoError(t, err)
	require.NotNil(t, st.Pending, "the reply is saved before the post")
	assert.Equal(t, 1, st.Pending.Turn)
	assert.Equal(t, marker(1, 0, id), lastLine(st.Pending.Body))

	r.h.Poll(context.Background())
	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1, "exactly one reply")
	assert.Equal(t, st.Pending.Body, replies[0].Body)
	assert.Len(t, r.agent.Prompts(), 1, "no new turn while a reply is pending, none after it")

	st, err = r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending, "a successful post clears the saved reply")
}

func TestPollDropsAPendingReplyThatGitLabAlreadyHolds(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.gl.FailAfterNote(errors.New("response lost"))
	r.start().h.Poll(context.Background())
	require.Len(t, r.replies(), 1, "the note was created although the post reported an error")

	later := r.comment("two")
	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 2, "turn 1 is not posted again; turn 2 runs in the same poll")
	assert.Equal(t, marker(2, 0, later), lastLine(replies[1].Body))

	st, err := r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending)
}

func TestPollDoesNotPostWhenTheReplyCannotBeSaved(t *testing.T) {
	r := newRig(t)
	id := r.comment("do it")
	r.store.FailSave(nil, errors.New("disk full")) // the first save is the session-started flag
	r.start().h.Poll(context.Background())

	assert.Empty(t, r.replies(), "no post without a saved reply")

	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Len(t, r.agent.Prompts(), 1, "the turn does not run again")
}

// botNote adds a reply of the bot user to the issue.
func (r *rig) botNote(body string) {
	r.gl.AddNote(key, models.Note{Author: bot, Body: body})
}

func TestPollRecoversAPendingReplyFromTheStore(t *testing.T) {
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

	r.start().h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 3, "turn 1 is not posted again, turn 3 runs in the same poll")
	assert.Equal(t, marker(3, 0, c), lastLine(replies[2].Body))
	assert.Equal(t, "prepare fresh=false", r.ws.Calls()[0], "the session-started flag comes from the store")
}

func TestPollPostsOneReplyPerSessionAndTurnWhenAnEarlierTurnIsPending(t *testing.T) {
	r := newRig(t)
	a := r.comment("one")
	r.botNote("first\n" + marker(1, 0, a))
	b := r.comment("two")
	r.botNote("second\n" + marker(2, 0, b))
	c := r.comment("three")

	require.NoError(t, r.store.Save(session, models.HarnessState{
		Pending: &models.PendingReply{Turn: 1, Body: "stale pending reply\n" + marker(1, 0, a)}, Started: true,
	}))

	r.start().h.Poll(context.Background())

	perTurn := map[string]int{}

	for _, n := range r.replies() {
		perTurn[lastLine(n.Body)]++

		assert.NotContains(t, n.Body, "stale pending reply")
	}

	assert.Equal(t, map[string]int{marker(1, 0, a): 1, marker(2, 0, b): 1, marker(3, 0, c): 1}, perTurn)

	prompts := r.agent.Prompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "three", "turn 3 covers the new comment")

	st, err := r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending)
}

func TestPollPostsAPendingReplyFromTheStoreOnce(t *testing.T) {
	r := newRig(t)
	a := r.comment("one")
	body := "done\n" + marker(1, 0, a)
	require.NoError(t, r.store.Save(session, models.HarnessState{Pending: &models.PendingReply{Turn: 1, Body: body}}))

	r.start().h.Poll(context.Background())
	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, body, replies[0].Body)
	assert.Empty(t, r.agent.Prompts(), "no new turn while a reply is pending")
}

func TestPollSavesTheSessionStartedFlagAfterCloneAndSetup(t *testing.T) {
	r := newRig(t)
	r.comment("one")

	var during models.HarnessState

	r.agent.Result = func(context.Context, int) (string, int, error) {
		during, _ = r.store.Load(session)

		return "text", 0, nil
	}
	r.start().h.Poll(context.Background())

	assert.True(t, during.Started, "saved before the agent runs")
}

func TestPollClonesAgainWhenTheSessionStartedFlagCannotBeSaved(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.store.FailSave(errors.New("disk full"))
	r.start().h.Poll(context.Background())

	r.h.Poll(context.Background())
	r.comment("two")
	r.h.Poll(context.Background())

	assert.Equal(t, "prepare fresh=true", r.ws.Calls()[4], "the second turn clones and sets up again")
}

func TestPollRunsNoTurnWhileTheStateCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.store.FailLoad(errors.New("io error"))
	r.start().h.Poll(context.Background())

	assert.Empty(t, r.agent.Prompts())

	r.h.Poll(context.Background())
	assert.Len(t, r.replies(), 1, "the next poll runs the turn")
}

func TestPollStartsFromACorruptState(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.store.Corrupt()
	r.start().h.Poll(context.Background())

	assert.Len(t, r.replies(), 1)
}

func TestPollLogsACorruptStateOnce(t *testing.T) {
	r := newRig(t)
	a := r.comment("one")
	r.botNote("first\n" + marker(1, 0, a))
	r.store.Corrupt()
	r.start()

	for range 3 {
		r.h.Poll(context.Background())
	}

	assert.Equal(t, 1, strings.Count(r.logs.String(), "level=ERROR"), "one error line")
	assert.Contains(t, r.logs.String(), "Harness state is corrupt")
	assert.Empty(t, r.agent.Prompts())

	b := r.comment("two")
	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 2)
	assert.Equal(t, marker(2, 0, b), lastLine(replies[1].Body))
	assert.Equal(t, "prepare fresh=true", r.ws.Calls()[0], "the session-started flag is lost with the state")
	assert.Equal(t, 1, strings.Count(r.logs.String(), "level=ERROR"))
}

func TestPollReadsWithRefreshAndConfirmsWithLoad(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.start().h.Poll(context.Background())
	assert.Equal(t, 1, r.gl.LoadCalls(), "the first read is a full one")
	assert.Zero(t, r.gl.RefreshCalls())

	r.h.Poll(context.Background())
	assert.Equal(t, 1, r.gl.RefreshCalls())
	assert.Equal(t, 1, r.gl.LoadCalls(), "nothing actionable: no full read")

	r.comment("two")
	r.h.Poll(context.Background())
	assert.Equal(t, 2, r.gl.RefreshCalls())
	assert.Equal(t, 2, r.gl.LoadCalls(), "an actionable result is confirmed with a full read")
	assert.Len(t, r.replies(), 2)
}

func TestPollReadsInFullOnceWhileAReplyIsPending(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.gl.FailBeforeNote(errors.New("gitlab unavailable"))
	r.start().h.Poll(context.Background())
	require.Empty(t, r.replies())
	assert.Equal(t, 1, r.gl.LoadCalls())

	r.h.Poll(context.Background())

	assert.Equal(t, 2, r.gl.LoadCalls(), "one full read")
	assert.Zero(t, r.gl.RefreshCalls(), "no refresh before the full read")
	assert.Len(t, r.replies(), 1)
}

func TestPollStartsNoTurnForACommentDeletedAfterRefresh(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.start().h.Poll(context.Background())

	gone := r.comment("two")

	r.gl.OnLoad(func(models.IssueKey) { r.gl.DeleteNote(key, gone) })
	r.h.Poll(context.Background())

	assert.Len(t, r.agent.Prompts(), 1)
	assert.Len(t, r.replies(), 1)
}

func TestPollDropsAPendingReplyWhenClearingItFailed(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	// saves: session-started flag, pending reply; the third, the clear after the post, fails
	r.store.FailSave(nil, nil, errors.New("disk full"))
	r.start().h.Poll(context.Background())
	require.Len(t, r.replies(), 1)

	st, err := r.store.Load(session)
	require.NoError(t, err)
	require.NotNil(t, st.Pending, "the stored reply survives the failed clear")

	r.comment("two")
	r.h.Poll(context.Background())

	assert.Len(t, r.replies(), 2, "the stored reply is dropped, not posted again; turn 2 runs")

	st, err = r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending)
}

func TestPollPostsUnderItsOwnBoundedContextAfterAWatchdogCut(t *testing.T) {
	r := newRig(t)
	r.comment("do it")
	r.agent.Result = func(ctx context.Context, _ int) (string, int, error) {
		r.clk.Advance(time.Hour)
		<-ctx.Done()

		return "", 0, ctx.Err()
	}
	r.start().h.Poll(context.Background())

	require.Len(t, r.replies(), 1)

	r.spy.mu.Lock()
	defer r.spy.mu.Unlock()

	require.NoError(t, r.spy.postErr, "the watchdog context has ended; the post has its own")
	require.True(t, r.spy.postBounded)
	assert.Equal(t, r.clk.Now().Add(30*time.Second), r.spy.postDeadline)
}

func TestPollPostsNoReplyForATurnCutByShutdown(t *testing.T) {
	r := newRig(t)
	r.comment("do it")

	ctx, cancel := context.WithCancel(context.Background())
	r.agent.Result = func(ctx context.Context, _ int) (string, int, error) {
		cancel()
		<-ctx.Done()

		return "", 0, ctx.Err()
	}
	r.start().h.Poll(ctx)

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

func TestPollPostsAgainAReplyWhoseMarkerAPersonDeleted(t *testing.T) {
	r := newRig(t)
	id := r.comment("one")
	r.store.FailSave(nil, nil, errors.New("disk full")) // the clear after the post fails
	r.start().h.Poll(context.Background())

	posted := r.replies()
	require.Len(t, posted, 1)
	r.gl.DeleteNote(key, posted[0].ID)

	r.h.Poll(context.Background())

	replies := r.replies()
	require.Len(t, replies, 1)
	assert.Equal(t, marker(1, 0, id), lastLine(replies[0].Body))
	assert.Len(t, r.agent.Prompts(), 1, "the stored reply is posted again, the turn does not run again")
}
