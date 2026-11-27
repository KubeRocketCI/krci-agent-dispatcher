package protocoltest_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// tokenUser is the user of a GitLab project access token; its display name is krci-agent.
const tokenUser = "project_1_bot_x"

// tokenUserNoteHook is a Note Hook payload of a note by tokenUser on the issue.
const tokenUserNoteHook = `{"object_kind": "note", "user": {"name": "krci-agent", "username": "` + tokenUser + `"},
  "project": {"id": 12}, "object_attributes": {"noteable_type": "Issue"},
  "issue": {"iid": 7, "labels": [{"title": "krci-agent"}, {"title": "krci::agent::working"}]}}`

func TestTokenUserReplyEndsTheTurn(t *testing.T) {
	e := newEnvFor(t, tokenUser)
	first := e.comment("add a README")
	e.run()
	e.handle(intake.Start)
	e.idle()

	var replies []models.Note

	statusNotes := 0

	for _, n := range e.gl.Notes(key) {
		if n.Author != tokenUser {
			continue
		}

		if strings.Contains(n.Body, "krci-agent-turn") {
			replies = append(replies, n)
		}

		if _, ok := models.ParseRecord(n.Body); ok {
			statusNotes++
		}
	}

	require.Len(t, replies, 1, "the reply starts no turn")
	assert.Contains(t, replies[0].Body, marker("session-1", 1, first))
	assert.Equal(t, 1, statusNotes)
	assert.Len(t, e.agent.Prompts(), 1)
	assert.Equal(t, models.Suspended, e.actorState())
	e.assertState(models.Waiting)

	r, err := intake.Parse(intake.NoteHook, []byte(tokenUserNoteHook), e.d.Bot(), agentLabels)
	require.NoError(t, err)
	assert.Equal(t, intake.Ignore, r.Kind, "a note of the token user is no comment")
}
