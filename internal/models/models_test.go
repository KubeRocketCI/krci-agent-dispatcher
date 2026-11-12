package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const bot = "krci-agent"

var agentLabels AgentLabels

func botNote(id int64, body string) Note { return Note{ID: id, Body: body, Author: bot} }

func personNote(id int64, body string) Note { return Note{ID: id, Body: body, Author: "owner"} }

func TestIssueKey(t *testing.T) {
	k := IssueKey{ProjectID: 12, IID: 7}

	assert.Equal(t, "12/7", k.String())
	assert.Equal(t, "issue-12-7", k.ActorName())
}

func TestAgentLabelsDefaults(t *testing.T) {
	assert.Equal(t, "krci-agent", agentLabels.Trigger())
	assert.Equal(t, "krci::agent::working", agentLabels.State(Working))
	assert.Equal(t, "krci::agent::done", agentLabels.State(Done))
	assert.Equal(t,
		[]string{"krci::agent::working", "krci::agent::waiting", "krci::agent::done", "krci::agent::error"},
		agentLabels.States())
}

func TestAgentLabelsStateFrom(t *testing.T) {
	s, ok := agentLabels.StateFrom([]string{agentLabels.Trigger(), agentLabels.State(Error), "bug"})
	assert.True(t, ok)
	assert.Equal(t, Error, s)

	_, ok = agentLabels.StateFrom([]string{agentLabels.Trigger(), "bug"})
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
	assert.True(t, HasLabel([]string{"bug", agentLabels.Trigger()}, agentLabels.Trigger()))
	assert.False(t, HasLabel([]string{agentLabels.State(Waiting)}, agentLabels.Trigger()))
}

func TestIssueBranch(t *testing.T) {
	assert.Equal(t, "agent/issue-7", IssueKey{ProjectID: 12, IID: 7}.IssueBranch())
}

func TestMergeNotes(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	notes := []Note{
		{ID: 1, Body: "a", Author: "owner", UpdatedAt: t0},
		{ID: 2, Body: "b", Author: bot, UpdatedAt: t0.Add(time.Second)},
	}
	changed := []Note{
		{ID: 3, Body: "c", Author: "owner", UpdatedAt: t0.Add(3 * time.Second)},
		{ID: 1, Body: "a edited", Author: "owner", UpdatedAt: t0.Add(2 * time.Second)},
	}

	got := MergeNotes(notes, changed)

	assert.Equal(t, []Note{changed[1], notes[1], changed[0]}, got)
	assert.Equal(t, "a", notes[0].Body, "the input is not changed")
	assert.Equal(t, t0.Add(3*time.Second), LastUpdate(got))
	assert.True(t, LastUpdate(nil).IsZero())
}

func TestNewIssueView(t *testing.T) {
	r := Record{State: Error, Reason: ReasonTurnLimit}
	notes := []Note{
		personNote(9, "later"),
		botNote(4, StatusBody(r, StatusView{State: Error, Session: "s1"})),
		personNote(2, RecordLine(Record{State: Done})),
	}

	v := NewIssueView(Issue{Key: IssueKey{ProjectID: 12, IID: 7}}, notes, bot)

	assert.Equal(t, []int64{2, 4, 9}, []int64{v.Notes[0].ID, v.Notes[1].ID, v.Notes[2].ID})
	assert.True(t, v.HasRecord)
	assert.Equal(t, r, v.Record)
	assert.Equal(t, "s1", v.Status.Session)
	assert.False(t, NewIssueView(Issue{}, notes[:1], bot).HasRecord)
}

func TestIssueViewEnabled(t *testing.T) {
	view := func(state string, labels ...string) IssueView {
		return IssueView{Issue: Issue{IssueState: state, Labels: labels}}
	}

	assert.True(t, view(IssueOpened, agentLabels.Trigger()).Enabled(agentLabels))
	assert.False(t, view(IssueOpened, "other").Enabled(agentLabels), "no trigger label")
	assert.False(t, view(IssueClosed, agentLabels.Trigger()).Enabled(agentLabels), "closed")
}
