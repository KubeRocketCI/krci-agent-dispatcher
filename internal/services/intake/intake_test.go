package intake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const bot = "krci-bot"

var (
	agentLabels models.AgentLabels
	label       = agentLabels.Trigger()
)

var key = models.IssueKey{ProjectID: 12, IID: 7}

func parse(t *testing.T, event, fixture string) Request {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	require.NoError(t, err)

	r, err := Parse(event, body, bot, agentLabels)
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

func TestParseConfidentialEventsIgnored(t *testing.T) {
	assert.Equal(t, Request{}, parse(t, "Confidential Issue Hook", "issue-open.json"))
	assert.Equal(t, Request{}, parse(t, "Confidential Issue Hook", "issue-close.json"))
	assert.Equal(t, Request{}, parse(t, "Confidential Note Hook", "note-issue.json"))
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
		_, err := Parse(IssueHook, []byte(body), bot, agentLabels)
		require.Error(t, err, body)
	}
}

func TestParseMissingKeyIgnored(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"object_attributes": {"action": "open", "iid": 7}, "labels": [{"title": "krci-agent"}]}`,
		`{"project": {"id": 12}, "object_attributes": {"action": "open"}, "labels": [{"title": "krci-agent"}]}`,
	} {
		r, err := Parse(IssueHook, []byte(body), bot, agentLabels)
		require.NoError(t, err)
		assert.Equal(t, Ignore, r.Kind, body)
	}

	noIssue := `{"project": {"id": 12}, "object_attributes": {"noteable_type": "Issue"}, "user": {"username": "dev1"}}`
	r, err := Parse(NoteHook, []byte(noIssue), bot, agentLabels)
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
	assert.Equal(t, Request{Key: key, Kind: Ignore, Author: "krci-bot"}, parse(t, NoteHook, "note-bot.json"))
	mr := Request{Key: models.IssueKey{ProjectID: 12}, Kind: Ignore, Author: "dev1"}
	assert.Equal(t, mr, parse(t, NoteHook, "note-mr.json"))
}

func labelList(titles []string) []map[string]string {
	out := make([]map[string]string, 0, len(titles))
	for _, title := range titles {
		out = append(out, map[string]string{"title": title})
	}

	return out
}

// issueEvent is an Issue Hook payload. A nil labels omits the labels key; a nil previous omits changes.labels.
type issueEvent struct {
	user, action      string
	labels            []string
	previous, current []string
}

func (e issueEvent) kind(t *testing.T) Kind {
	t.Helper()

	p := map[string]any{
		"user":              map[string]string{"username": e.user},
		"project":           map[string]int{"id": 12},
		"object_attributes": map[string]any{"iid": 7, "action": e.action},
		"changes":           map[string]any{},
	}
	if e.labels != nil {
		p["labels"] = labelList(e.labels)
	}

	if e.previous != nil {
		p["changes"] = map[string]any{
			"labels": map[string]any{"previous": labelList(e.previous), "current": labelList(e.current)},
		}
	}

	body, err := json.Marshal(p)
	require.NoError(t, err)

	r, err := Parse(IssueHook, body, bot, agentLabels)
	require.NoError(t, err)

	return r.Kind
}

// noteKind parses a Note Hook payload on issue 7. A nil labels omits the issue.labels key.
func noteKind(t *testing.T, labels []string) Kind {
	t.Helper()

	issue := map[string]any{"iid": 7}
	if labels != nil {
		issue["labels"] = labelList(labels)
	}

	body, err := json.Marshal(map[string]any{
		"user":              map[string]string{"username": "dev1"},
		"project":           map[string]int{"id": 12},
		"object_attributes": map[string]string{"noteable_type": "Issue"},
		"issue":             issue,
	})
	require.NoError(t, err)

	r, err := Parse(NoteHook, body, bot, agentLabels)
	require.NoError(t, err)

	return r.Kind
}

func TestParseBotIssueEvents(t *testing.T) {
	none, waiting := []string{}, []string{label, agentLabels.State(models.Waiting)}

	for name, c := range map[string]struct {
		e    issueEvent
		want Kind
	}{
		"state label set": {
			issueEvent{action: "update", labels: []string{label, agentLabels.State(models.Working)}, previous: waiting,
				current: []string{label, agentLabels.State(models.Working)}}, Ignore,
		},
		"error label replaced": {
			issueEvent{
				action: "update", labels: waiting, previous: []string{label, agentLabels.State(models.Error)},
				current: waiting,
			},
			Ignore,
		},
		"field update":          {issueEvent{action: "update", labels: waiting}, Ignore},
		"trigger label added":   {issueEvent{action: "update", labels: waiting, previous: none, current: waiting}, Ignore},
		"open":                  {issueEvent{action: "open", labels: []string{label}}, Ignore},
		"reopen":                {issueEvent{action: "reopen", labels: []string{label}}, Ignore},
		"close":                 {issueEvent{action: "close", labels: waiting}, Stop},
		"trigger label removed": {issueEvent{action: "update", labels: none, previous: waiting, current: none}, Stop},
	} {
		c.e.user = bot
		assert.Equal(t, c.want, c.e.kind(t), name)
	}
}

func TestParseIssueEventsLabelFilter(t *testing.T) {
	none, bug := []string{}, []string{"bug"}

	for name, c := range map[string]struct {
		e    issueEvent
		want Kind
	}{
		"update, other label only":   {issueEvent{action: "update", labels: bug}, Ignore},
		"update, no label":           {issueEvent{action: "update", labels: none}, Ignore},
		"update, other label change": {issueEvent{action: "update", labels: bug, previous: none, current: bug}, Ignore},
		"update, labels key absent":  {issueEvent{action: "update"}, Activate},
		"update, trigger label":      {issueEvent{action: "update", labels: []string{label}}, Activate},
		"update, state label only": {
			issueEvent{action: "update", labels: []string{agentLabels.State(models.Done)}}, Activate,
		},
		"update, current from changes": {issueEvent{action: "update", previous: []string{"bug", "x"}, current: bug}, Ignore},
		"update, changes carry trigger": {
			issueEvent{action: "update", previous: []string{label}, current: []string{label, "bug"}}, Activate,
		},
		"state label removed": {
			issueEvent{
				action: "update", labels: bug, previous: []string{"bug", agentLabels.State(models.Waiting)}, current: bug,
			},
			Activate,
		},
		"trigger label removed": {
			issueEvent{action: "update", labels: bug, previous: []string{"bug", label}, current: bug}, Stop,
		},
		"error label removed": {
			issueEvent{action: "update", labels: bug, previous: []string{"bug", agentLabels.State(models.Error)}, current: bug},
			ClearError,
		},
		"close, other label only": {issueEvent{action: "close", labels: bug}, Stop},
	} {
		c.e.user = "dev1"
		assert.Equal(t, c.want, c.e.kind(t), name)
	}
}

func TestParseNoteLabelFilter(t *testing.T) {
	for name, c := range map[string]struct {
		labels []string
		want   Kind
	}{
		"no label":          {[]string{}, Ignore},
		"other labels only": {[]string{"bug", "krci-agent-like"}, Ignore},
		"trigger label":     {[]string{"bug", label}, Activate},
		"state label only":  {[]string{agentLabels.State(models.Error)}, Activate},
		"labels key absent": {nil, Activate},
	} {
		assert.Equal(t, c.want, noteKind(t, c.labels), name)
	}
}
