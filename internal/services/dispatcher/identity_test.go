package dispatcher_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/profiles/profilestest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/dispatcher"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/substratetest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/templates"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

// tokenUser is the user of a GitLab project access token: a generated username, not its display name.
const tokenUser = "project_1_bot_x"

// newTokenUserEnv is a dispatcher over a GitLab whose token user is tokenUser, with no bot user set in its config.
func newTokenUserEnv(t *testing.T, turnLimit int) *env {
	t.Helper()

	e := &env{
		t:    t,
		logs: &syncBuffer{},
		gl:   gitlabtest.NewFake(tokenUser),
		sb:   substratetest.NewFake("krci-agents"),
		pr:   profilestest.NewFake(templates.Deployment{}),
		clk:  systemtest.NewClock(time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)),
	}
	e.gl.SetMember(key.ProjectID, dev, 30)
	e.pr.Set(goProfiles)

	e.d = e.newDispatcher(e.gl, dispatcher.Config{
		TurnLimit: turnLimit, TurnTimeout: 15 * time.Minute, PollInterval: 10 * time.Second,
		NewSessionID: func() string { return "session-1" },
		Logger:       slog.New(slog.NewTextHandler(e.logs, nil)),
	})

	return e
}

func TestNewReadsTheBotUserFromTheToken(t *testing.T) {
	d, err := dispatcher.New(t.Context(), gitlabtest.NewFake(tokenUser), substratetest.NewFake("krci-agents"),
		profilestest.NewFake(templates.Deployment{}), systemtest.NewClock(time.Now()), dispatcher.Config{})
	require.NoError(t, err)
	assert.Equal(t, tokenUser, d.Bot())
}

// A token GitLab rejects, or a token user without a username, stops New at once: no retry.
func TestNewFailsAtOnceWhenTheTokenIsRejected(t *testing.T) {
	for name, err := range map[string]error{
		"unauthorized":   fmt.Errorf("gitlab GET /user: status 401 Unauthorized: %w", models.ErrUnauthorized),
		"forbidden":      fmt.Errorf("gitlab GET /user: %w", models.ErrForbidden),
		"no username":    fmt.Errorf("gitlab GET /user: %w", models.ErrNoIdentity),
		"identity moved": fmt.Errorf("gitlab: %w", models.ErrIdentityChanged),
	} {
		t.Run(name, func(t *testing.T) {
			gl := gitlabtest.NewFake(tokenUser)
			gl.FailCurrentUser(err)

			clk := systemtest.NewClock(time.Now())

			d, got := dispatcher.New(t.Context(), gl, substratetest.NewFake("krci-agents"),
				profilestest.NewFake(templates.Deployment{}), clk, dispatcher.Config{})
			require.ErrorIs(t, got, err)
			assert.Nil(t, d)
			assert.Zero(t, clk.Count(time.Second), "no retry")
		})
	}
}

// A failed read of the token user, a 502 or a timeout, is retried with backoff until it succeeds.
func TestNewRetriesAFailedTokenUserRead(t *testing.T) {
	gl := gitlabtest.NewFake(tokenUser)
	gl.FailCurrentUser(errors.New("gitlab GET /user: status 502 Bad Gateway"),
		errors.New("gitlab GET /user: context deadline exceeded"), errors.New("gitlab GET /user: connection refused"))

	clk := systemtest.NewClock(time.Now())
	logs := &syncBuffer{}

	d, err := dispatcher.New(t.Context(), gl, substratetest.NewFake("krci-agents"),
		profilestest.NewFake(templates.Deployment{}), clk,
		dispatcher.Config{Logger: slog.New(slog.NewTextHandler(logs, nil))})
	require.NoError(t, err)
	assert.Equal(t, tokenUser, d.Bot())

	for _, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		assert.Equal(t, 1, clk.Count(wait), "one pause of %s", wait)
	}

	assert.Contains(t, logs.String(), "Cannot read the bot user, the user of the GitLab token; retrying")
}

// A ctx that ends while the read is retried ends New with the error of ctx.
func TestNewEndsWhenCtxEndsDuringTheRetries(t *testing.T) {
	gl := gitlabtest.NewFake(tokenUser)
	gl.FailCurrentUser(errors.New("gitlab GET /user: status 502 Bad Gateway"),
		errors.New("gitlab GET /user: status 502 Bad Gateway"))

	ctx, cancel := context.WithCancel(t.Context())
	clk := systemtest.NewClock(time.Now())
	clk.SetOnSleep(func(time.Duration) { cancel() })

	d, err := dispatcher.New(ctx, gl, substratetest.NewFake("krci-agents"),
		profilestest.NewFake(templates.Deployment{}), clk, dispatcher.Config{})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, d)
}

// emptyUser is a GitLab port whose token user has no username.
type emptyUser struct{ *gitlabtest.Fake }

func (emptyUser) CurrentUser(context.Context) (string, error) { return "", nil }

func TestNewFailsOnAnEmptyTokenUser(t *testing.T) {
	_, err := dispatcher.New(t.Context(), emptyUser{gitlabtest.NewFake(tokenUser)},
		substratetest.NewFake("krci-agents"), profilestest.NewFake(templates.Deployment{}),
		systemtest.NewClock(time.Now()), dispatcher.Config{})
	require.ErrorIs(t, err, models.ErrNoIdentity)
}

// A Note Hook of a note by the token user is no comment: it maps to Ignore with the bot user the dispatcher read.
func TestTokenUserNoteIsNoActivation(t *testing.T) {
	e := newTokenUserEnv(t, 5)
	body := `{"user": {"name": "krci-agent", "username": "` + tokenUser + `"}, "project": {"id": 12},
		"object_attributes": {"noteable_type": "Issue"},
		"issue": {"iid": 7, "labels": [{"title": "krci-agent"}, {"title": "krci::agent::working"}]}}`

	r, err := intake.Parse(intake.NoteHook, []byte(body), e.d.Bot(), agentLabels)
	require.NoError(t, err)
	assert.Equal(t, intake.Ignore, r.Kind)
}

// waitingTokenUserIssue adds a waiting issue: the status note of tokenUser, a suspended actor and turn n of
// tokenUser over a comment of dev, for each n up to turns.
func (e *env) waitingTokenUserIssue(turns int) {
	e.gl.AddIssue(key, "krci/test-go-app", models.IssueOpened, agentLabels.Trigger(), agentLabels.State(models.Waiting))
	e.gl.AddNote(key, models.Note{Author: tokenUser, Body: models.StatusBody(*waiting(),
		models.StatusView{State: models.Waiting, Actor: actor, Session: session0})})
	e.sb.AddComplete(actor, ateapi.ActorState_ACTOR_STATE_SUSPENDED)

	for n := 1; n <= turns; n++ {
		e.tokenUserReply(n, e.gl.AddNote(key, models.Note{Author: dev, Body: "please continue"}))
	}
}

func (e *env) tokenUserReply(n int, last int64) {
	e.gl.AddNote(key, models.Note{Author: tokenUser, Body: "done\n" + turnMarker(session0, n, last)})
}

// statusNotes counts the notes of tokenUser that hold a record.
func (e *env) statusNotes() int {
	n := 0

	for _, nt := range e.gl.Notes(key) {
		if _, ok := models.ParseRecord(nt.Body); ok && nt.Author == tokenUser {
			n++
		}
	}

	return n
}

func TestTokenUserNotesAreBotNotes(t *testing.T) {
	e := newTokenUserEnv(t, 5)
	e.waitingTokenUserIssue(1)
	second := e.gl.AddNote(key, models.Note{Author: dev, Body: "and a LICENSE"})
	e.sb.OnResume(func(string) { e.tokenUserReply(2, second) })
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Equal(t, 1, e.sb.Count("resume", actor), "the comment after turn 1 resumes the actor once")
	assert.Equal(t, 1, e.sb.Count("suspend", actor), "the marker of turn 2 suspends the actor")
	assert.Equal(t, 1, e.statusNotes(), "the status note of the token user is the status note")
	assert.Equal(t, models.Record{State: models.Waiting}, recordOf(e, tokenUser))
}

func TestTokenUserMarkersCountTowardTheTurnLimit(t *testing.T) {
	e := newTokenUserEnv(t, 2)
	e.waitingTokenUserIssue(2)
	e.gl.AddNote(key, models.Note{Author: dev, Body: "one more"})
	e.run()
	e.handle(intake.Activate, dev)
	e.idle()

	assert.Zero(t, e.sb.Count("resume", actor), "two turns of the session reach the turn limit of two")
	assert.Equal(t, models.Record{State: models.Error, Reason: models.ReasonTurnLimit}, recordOf(e, tokenUser))
	assert.Equal(t, 1, e.statusNotes())
}

func recordOf(e *env, author string) models.Record {
	n, ok := models.StatusNote(e.gl.Notes(key), author)
	if !ok {
		e.t.Fatalf("no status note of %s", author)
	}

	r, _ := models.ParseRecord(n.Body)

	return r
}
