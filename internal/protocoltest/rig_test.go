package protocoltest_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/dispatcher"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state/statetest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/substratetest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

const (
	bot         = "krci-agent"
	dev         = "dev1"
	token       = "token"
	projectPath = "krci/test-go-app"
	poll        = 10 * time.Second
	turnTimeout = 15 * time.Minute
)

var (
	key         = models.IssueKey{ProjectID: 12, IID: 7}
	actor       = key.ActorName()
	agentLabels models.AgentLabels
)

// fakeAgent records its prompts. onTurn runs inside the agent run with the number of the call.
type fakeAgent struct {
	mu     sync.Mutex
	log    []string
	onTurn func(call int)
}

func (a *fakeAgent) Run(_ context.Context, prompt string) (string, int, error) {
	a.mu.Lock()
	a.log = append(a.log, prompt)
	call, onTurn := len(a.log), a.onTurn
	a.mu.Unlock()

	if onTurn != nil {
		onTurn(call)
	}

	return fmt.Sprintf("agent run %d", call), 0, nil
}

func (a *fakeAgent) prompts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.log)
}

// fakeWorkspace records its calls.
type fakeWorkspace struct {
	mu  sync.Mutex
	log []string
}

func (w *fakeWorkspace) record(call string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.log = append(w.log, call)
}

func (w *fakeWorkspace) Prepare(_ context.Context, fresh bool) error {
	w.record(fmt.Sprintf("prepare fresh=%t", fresh))

	return nil
}

func (w *fakeWorkspace) Verify(context.Context) error {
	w.record("verify")

	return nil
}

func (w *fakeWorkspace) Publish(_ context.Context, branch string) error {
	w.record("publish " + branch)

	return nil
}

func (w *fakeWorkspace) calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.log)
}

// env is the dispatcher and the harness of the actor over one GitLab fake, one Substrate fake and one clock. The
// harness is built from the spec the Substrate fake holds, on each resume; its state store lives as long as the
// session of the actor.
type env struct {
	t     *testing.T
	gl    *gitlabtest.Fake
	sb    *substratetest.Fake
	clk   *systemtest.Clock
	d     *dispatcher.Dispatcher
	rest  *gitlab.REST
	agent *fakeAgent
	ws    *fakeWorkspace

	mu           sync.Mutex
	h            *harness.Harness
	store        *statetest.Fake
	storeSession string
}

func newEnv(t *testing.T) *env {
	t.Helper()

	e := &env{
		t:     t,
		gl:    gitlabtest.NewFake(bot),
		sb:    substratetest.NewFake("krci-agents"),
		clk:   systemtest.NewClock(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)),
		agent: &fakeAgent{},
		ws:    &fakeWorkspace{},
	}
	e.gl.SetMember(key.ProjectID, dev, 30)
	e.gl.SetDefaultBranch(key.ProjectID, "main")
	e.gl.AddIssue(key, projectPath, models.IssueOpened, agentLabels.Trigger())

	srv := httptest.NewServer(e.gl.Handler(token))
	t.Cleanup(srv.Close)

	var err error

	e.rest, err = gitlab.NewREST(srv.URL, token, "", bot, models.Developer,
		gitlab.WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, err)

	var sessions int

	e.d = dispatcher.New(e.gl, e.sb, e.clk, dispatcher.Config{
		Bot: bot, TurnLimit: 5, TurnTimeout: turnTimeout, PollInterval: poll,
		NewSessionID: func() string { sessions++; return fmt.Sprintf("session-%d", sessions) },
		Logger:       slog.New(slog.DiscardHandler),
	})

	e.sb.OnResume(func(name string) { e.boot(name) })
	e.clk.SetOnSleep(e.onSleep)

	return e
}

// boot builds the harness of the actor from its spec, as the actor runtime would at its start.
func (e *env) boot(name string) {
	spec, ok := e.sb.Spec(name)
	require.True(e.t, ok, "actor has no spec")

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.storeSession != spec.SessionID {
		e.store, e.storeSession = statetest.NewFake(), spec.SessionID
	}

	env := map[string]string{
		"GITLAB_URL":             "https://gitlab.example.com",
		"PROJECT_ID":             strconv.FormatInt(key.ProjectID, 10),
		"ISSUE_IID":              strconv.FormatInt(spec.IID, 10),
		"PROJECT_PATH":           spec.ProjectPath,
		"BOT_USERNAME":           spec.BotUsername,
		"SESSION_ID":             spec.SessionID,
		"AGENT_CONTRACT_VERSION": "1",
		"TURN_TIMEOUT_SECONDS":   strconv.Itoa(int(turnTimeout.Seconds())),
		"POLL_SECONDS":           strconv.Itoa(int(poll.Seconds())),
		"TURN_LIMIT":             "5",
		"AGENT_STEPS":            "40",
		"ANTHROPIC_MODEL":        "model-x",
	}

	h, err := harness.New(func(k string) string { return env[k] }, harness.Ports{
		Notes: e.rest, Agent: e.agent, Workspace: e.ws, Clock: e.clk, State: e.store,
		Log: slog.New(slog.DiscardHandler),
	})
	require.NoError(e.t, err)

	e.h = h
}

// onSleep gives the harness one poll after each dispatcher poll while the actor runs.
func (e *env) onSleep(d time.Duration) {
	if d != poll {
		return
	}

	v, err := e.sb.Inspect(context.Background(), actor)
	if err != nil || v.State != models.Running {
		return
	}

	e.mu.Lock()
	h := e.h
	e.mu.Unlock()

	if h != nil {
		h.Step(context.Background())
	}
}

func (e *env) run() {
	ctx, cancel := context.WithCancel(context.Background())
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

func (e *env) handle(k intake.Kind) {
	e.d.Handle(intake.Request{Key: key, Kind: k, Author: dev})
}

// comment adds a comment of the developer and returns its note id.
func (e *env) comment(body string) int64 {
	return e.gl.AddNote(key, models.Note{Author: dev, Body: body})
}

// replies returns the turn replies of the bot, oldest first.
func (e *env) replies() []models.Note {
	var out []models.Note

	for _, n := range e.gl.Notes(key) {
		if n.Author == bot && strings.Contains(n.Body, "krci-agent-turn") {
			out = append(out, n)
		}
	}

	return out
}

// marker returns the turn marker text of a reply.
func marker(session string, n int, last int64) string {
	return fmt.Sprintf("<!-- krci-agent-turn s=%s n=%d exit=0 last=%d -->", session, n, last)
}

func (e *env) actorState() models.ActorState {
	v, err := e.sb.Inspect(context.Background(), actor)
	require.NoError(e.t, err)

	return v.State
}

func (e *env) assertState(s models.AgentState) {
	e.t.Helper()

	r, ok := e.gl.Record(key)
	require.True(e.t, ok, "no record")
	assert.Equal(e.t, models.Record{State: s}, r)

	var states []string

	for _, l := range e.gl.Labels(key) {
		if agentLabels.IsState(l) {
			states = append(states, l)
		}
	}

	assert.Equal(e.t, []string{agentLabels.State(s)}, states)
}
