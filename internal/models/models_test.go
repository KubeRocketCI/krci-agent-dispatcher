package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const bot = "krci-agent"

func botNote(id int64, body string) Note { return Note{ID: id, Body: body, Author: bot} }

func personNote(id int64, body string) Note { return Note{ID: id, Body: body, Author: "owner"} }

func TestIssueKey(t *testing.T) {
	k := IssueKey{ProjectID: 12, IID: 7}

	assert.Equal(t, "12/7", k.String())
	assert.Equal(t, "issue-12-7", k.ActorName())
}

func TestStateLabels(t *testing.T) {
	assert.Equal(t, "agent::working", StateLabel(Working))
	assert.Equal(t, "agent::done", StateLabel(Done))
	assert.Equal(t, []string{"agent::working", "agent::waiting", "agent::done", "agent::error"}, StateLabels())
}

func TestStateFromLabels(t *testing.T) {
	s, ok := StateFromLabels([]string{"agent", "agent::error", "bug"})
	assert.True(t, ok)
	assert.Equal(t, Error, s)

	_, ok = StateFromLabels([]string{"agent", "bug"})
	assert.False(t, ok)
}

func TestStatusNoteFindsBotRecord(t *testing.T) {
	notes := []Note{
		personNote(100, RecordLine(Record{State: Done})),
		botNote(101, StatusBody(Record{State: Waiting}, StatusView{State: Waiting})),
		botNote(102, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
	}

	n, ok := StatusNote(notes, bot)
	assert.True(t, ok)
	assert.Equal(t, int64(101), n.ID)

	_, ok = StatusNote(notes[2:], bot)
	assert.False(t, ok)
}

func TestTriggerAndHasLabel(t *testing.T) {
	assert.True(t, HasLabel([]string{"bug", "agent"}, "agent"))
	assert.False(t, HasLabel([]string{"agent::waiting"}, "agent"))
}

func TestAgentBranch(t *testing.T) {
	assert.Equal(t, "agent/issue-7", IssueKey{ProjectID: 12, IID: 7}.AgentBranch())
}
