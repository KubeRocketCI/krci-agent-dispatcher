package intake

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const (
	bot   = "krci-agent"
	label = "agent"
)

var key = models.IssueKey{ProjectID: 12, IID: 7}

func parse(t *testing.T, event, fixture string) Request {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	require.NoError(t, err)

	r, err := Parse(event, body, bot, label)
	require.NoError(t, err)

	return r
}

func TestParse_IssueOpenWithLabel(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Start, Author: "dev1"}, parse(t, IssueHook, "issue-open.json"))
}

func TestParse_IssueOpenWithoutLabel(t *testing.T) {
	assert.Equal(t, Ignore, parse(t, IssueHook, "issue-open-no-label.json").Kind)
}

func TestParse_IssueUpdateLabelAdded(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Start, Author: "dev1"}, parse(t, IssueHook, "issue-label-add.json"))
}

func TestParse_IssueUpdateLabelRemoved(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Stop, Author: "dev1"}, parse(t, IssueHook, "issue-label-remove.json"))
}

func TestParse_IssueUpdateErrorLabelRemoved(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: ClearError, Author: "dev1"}, parse(t, IssueHook, "issue-error-removed.json"))
}

func TestParse_IssueUpdateOther(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Activate, Author: "dev1"}, parse(t, IssueHook, "issue-title-update.json"))
}

func TestParse_IssueClose(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Stop, Author: "dev1"}, parse(t, IssueHook, "issue-close.json"))
}

func TestParse_IssueReopenWithLabel(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Start, Author: "dev1"}, parse(t, IssueHook, "issue-reopen.json"))
}

func TestParse_IssueReopenWithoutLabel(t *testing.T) {
	assert.Equal(t, Ignore, parse(t, IssueHook, "issue-reopen-no-label.json").Kind)
}

func TestParse_IssueNote(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Activate, Author: "dev1"}, parse(t, NoteHook, "note-issue.json"))
}

func TestParseConfidentialEvents(t *testing.T) {
	assert.Equal(t, Start, parse(t, "Confidential Issue Hook", "issue-open.json").Kind)
	assert.Equal(t, Activate, parse(t, "Confidential Note Hook", "note-issue.json").Kind)
}

func TestParseMRNoteIgnored(t *testing.T) {
	assert.Equal(t, Ignore, parse(t, NoteHook, "note-mr.json").Kind)
}

func TestParseBotNoteIgnored(t *testing.T) {
	assert.Equal(t, Ignore, parse(t, NoteHook, "note-bot.json").Kind)
}

func TestParseUnknownEventIgnored(t *testing.T) {
	assert.Equal(t, Ignore, parse(t, "Push Hook", "issue-open.json").Kind)
	assert.Equal(t, Ignore, parse(t, "", "issue-open.json").Kind)
}

func TestParseInvalidJSON(t *testing.T) {
	for _, body := range []string{"", "{", "not json", "[1,2]"} {
		_, err := Parse(IssueHook, []byte(body), bot, label)
		require.Error(t, err, body)
	}
}

func TestParseMissingKeyIgnored(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"object_attributes": {"action": "open", "iid": 7}, "labels": [{"title": "agent"}]}`,
		`{"project": {"id": 12}, "object_attributes": {"action": "open"}, "labels": [{"title": "agent"}]}`,
	} {
		r, err := Parse(IssueHook, []byte(body), bot, label)
		require.NoError(t, err)
		assert.Equal(t, Ignore, r.Kind, body)
	}

	noIssue := `{"project": {"id": 12}, "object_attributes": {"noteable_type": "Issue"}, "user": {"username": "dev1"}}`
	r, err := Parse(NoteHook, []byte(noIssue), bot, label)
	require.NoError(t, err)
	assert.Equal(t, Ignore, r.Kind)
}

func TestKindString(t *testing.T) {
	assert.Equal(t, "start", Start.String())
	assert.Equal(t, "clear-error", ClearError.String())
	assert.Equal(t, "ignore", Ignore.String())
}

func TestParseIgnoreKeepsKey(t *testing.T) {
	assert.Equal(t, Request{Key: key, Kind: Ignore, Author: "dev1"}, parse(t, IssueHook, "issue-open-no-label.json"))
	assert.Equal(t, Request{Key: key, Kind: Ignore, Author: "krci-agent"}, parse(t, NoteHook, "note-bot.json"))
	mr := Request{Key: models.IssueKey{ProjectID: 12}, Kind: Ignore, Author: "dev1"}
	assert.Equal(t, mr, parse(t, NoteHook, "note-mr.json"))
}
