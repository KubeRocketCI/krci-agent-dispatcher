package harness_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
)

// tokenUser is the user of a GitLab project access token: a generated username, not its display name.
const tokenUser = "project_1_bot_x"

// prepares counts the Prepare calls of the workspace.
func (r *rig) prepares() int {
	n := 0

	for _, c := range r.ws.Calls() {
		if strings.HasPrefix(c, "prepare") {
			n++
		}
	}

	return n
}

// tokenUserNotes counts the notes of tokenUser.
func (r *rig) tokenUserNotes() int {
	n := 0

	for _, nt := range r.gl.Notes(key) {
		if nt.Author == tokenUser {
			n++
		}
	}

	return n
}

// Every turn counts toward TURN_LIMIT, a turn that fails at prepare too; the replies of the token user are no
// comments.
func TestPollRepliesOfTheTokenUserCountTowardTheTurnLimit(t *testing.T) {
	r := newRigFor(t, tokenUser)
	r.env["BOT_USERNAME"], r.env["TURN_LIMIT"] = tokenUser, "2"
	r.ws.PrepareErr = errors.New("clone failed")
	r.comment("one")
	r.start()

	for range 3 {
		r.h.Poll(context.Background())
	}

	assert.Equal(t, 1, r.tokenUserNotes(), "the reply starts no turn")
	assert.Equal(t, 1, r.prepares())

	r.comment("two")
	r.comment("three")

	for range 3 {
		r.h.Poll(context.Background())
		r.comment("more")
	}

	assert.Equal(t, 2, r.tokenUserNotes(), "no turn above the turn limit")
	assert.Equal(t, 2, r.prepares())
}

// A reply whose author is not BOT_USERNAME ends the turn loop: no turn starts after it.
func TestPollStopsTurnsAfterAReplyOfAnotherAuthor(t *testing.T) {
	r := newRigFor(t, tokenUser)
	r.ws.PrepareErr = errors.New("clone failed")
	r.comment("one")
	r.start()

	for range 3 {
		r.h.Poll(context.Background())
	}

	assert.Equal(t, 1, r.tokenUserNotes(), "one reply, then no turn")
	assert.Equal(t, 1, r.prepares())
}

// runUntilEnd runs Run and returns its error; a Run that does not end within 2 s fails the test.
func (r *rig) runUntilEnd() error {
	r.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- r.h.Run(ctx) }()

	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		cancel()

		err := <-done
		r.t.Fatalf("Run did not end; after cancel it returned %v", err)

		return nil
	}
}

// handshakeError returns err as a *harness.HandshakeError on BOT_USERNAME.
func handshakeError(t *testing.T, err error) *harness.HandshakeError {
	t.Helper()

	var he *harness.HandshakeError

	require.ErrorAs(t, err, &he)
	assert.Equal(t, "BOT_USERNAME", he.Item)

	return he
}

// A token user other than BOT_USERNAME ends Run before any turn.
func TestRunEndsBeforeAnyTurnWhenTheTokenUserIsNotTheBotUser(t *testing.T) {
	r := newRigFor(t, tokenUser)
	r.comment("one")
	r.start()

	he := handshakeError(t, r.runUntilEnd())
	assert.Equal(t, "BOT_USERNAME: is not the user of the GitLab token", he.Error())
	assert.NotContains(t, he.Error(), tokenUser)
	assert.NotContains(t, he.Error(), bot, "the error names the item, not its value")
	assert.Zero(t, r.prepares(), "no turn")
	assert.Zero(t, r.tokenUserNotes(), "no reply")
}

// A token the egress gateway or GitLab rejects ends Run before any turn, with no retry.
func TestRunEndsBeforeAnyTurnWhenTheTokenIsRejected(t *testing.T) {
	for name, cause := range map[string]error{
		"unauthorized": fmt.Errorf("gitlab GET /user: status 401 Unauthorized: %w", models.ErrUnauthorized),
		"forbidden":    fmt.Errorf("gitlab GET /user: %w", models.ErrForbidden),
		"no username":  fmt.Errorf("gitlab GET /user: %w", models.ErrNoIdentity),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.gl.FailCurrentUser(cause)
			r.comment("one")
			r.start()

			he := handshakeError(t, r.runUntilEnd())
			require.ErrorIs(t, he, cause)
			assert.NotContains(t, he.Error(), "401", "the cause is not part of the handshake line")
			assert.Zero(t, r.prepares())
			assert.NotContains(t, r.logs.String(), "retrying")
		})
	}
}

// A failed read of the token user, a gateway not ready or a GitLab 502, is retried; the turns start after it.
func TestRunRetriesAFailedTokenUserRead(t *testing.T) {
	r := newRig(t)
	r.gl.FailCurrentUser(errors.New("gitlab GET /user: connection refused"),
		errors.New("gitlab GET /user: status 502 Bad Gateway"))
	r.comment("one")
	r.start()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.agent.Result = func(context.Context, int) (string, int, error) {
		cancel()

		return "done", 0, nil
	}

	require.ErrorIs(t, r.h.Run(ctx), context.Canceled)
	assert.Equal(t, 1, r.prepares(), "the turn runs after the retries")
	assert.Equal(t, 2, strings.Count(r.logs.String(), "Cannot read the GitLab token user; retrying"))
}

// A token user read that keeps failing for 5 min ends Run before any turn.
func TestRunEndsWhenTheTokenUserReadKeepsFailing(t *testing.T) {
	r := newRig(t)

	errs := make([]error, 100)
	for i := range errs {
		errs[i] = errors.New("gitlab GET /user: status 502 Bad Gateway")
	}

	r.gl.FailCurrentUser(errs...)
	r.comment("one")
	r.start()

	start := r.clk.Now()
	he := handshakeError(t, r.runUntilEnd())
	assert.Equal(t, "BOT_USERNAME: cannot be checked: the GitLab token user cannot be read", he.Error())
	assert.ErrorContains(t, he.Cause, "502")
	assert.Zero(t, r.prepares())

	waited := r.clk.Now().Sub(start)
	assert.LessOrEqual(t, waited, 5*time.Minute)
	assert.Greater(t, waited, 4*time.Minute)
}

// A token that moves to another user under a running harness ends Run after the first reply of that user.
func TestRunEndsAfterAReplyOfAnotherAuthor(t *testing.T) {
	r := newRig(t)
	r.comment("one")
	r.agent.Result = func(context.Context, int) (string, int, error) {
		r.gl.SetTokenUser(tokenUser)
		r.comment("two")

		return "done", 0, nil
	}
	r.start()

	he := handshakeError(t, r.runUntilEnd())
	assert.Equal(t, "BOT_USERNAME: is not the author of the posted reply", he.Error())
	assert.Equal(t, 1, r.tokenUserNotes(), "one reply of the other user, then no turn")
	assert.Len(t, r.agent.Prompts(), 1)
	assert.Contains(t, r.logs.String(), "Reply posted by another user than the bot user")

	st, err := r.store.Load(session)
	require.NoError(t, err)
	assert.Nil(t, st.Pending, "the posted reply is not posted again")
}
