package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordRoundTrip(t *testing.T) {
	for _, r := range []Record{
		{State: Waiting},
		{State: Working},
		{State: Done},
		{State: Error, Reason: ReasonTurnLimit},
		{State: Error, Reason: ReasonSubstrateError},
	} {
		got, ok := ParseRecord(RecordLine(r))
		assert.True(t, ok, r)
		assert.Equal(t, r, got)
	}
}

func TestRecordLineFormat(t *testing.T) {
	assert.Equal(t, "<!-- krci-agent-status state=error reason=turn timeout -->",
		RecordLine(Record{State: Error, Reason: ReasonTurnTimeout}))
	assert.Equal(t, "<!-- krci-agent-status state=waiting reason= -->", RecordLine(Record{State: Waiting}))
}

func TestParseRecordRejects(t *testing.T) {
	for _, body := range []string{
		"",
		"<!-- krci-agent-status -->",
		"<!-- krci-agent-status state=sleeping reason= -->",
		"text\n<!-- krci-agent-status state=done reason= -->",
	} {
		_, ok := ParseRecord(body)
		assert.False(t, ok, body)
	}
}

func TestRecordIgnoredInPersonNote(t *testing.T) {
	notes := []Note{personNote(100, StatusBody(Record{State: Done}, StatusView{State: Done}))}

	_, ok := StatusNote(notes, bot)
	assert.False(t, ok)
}

func TestStatusBodyRecordFirst(t *testing.T) {
	body := StatusBody(Record{State: Waiting}, StatusView{
		State: Waiting, Actor: "issue-12-7", ActorUID: "u1", Pod: "pod-a", Session: "s1", Updated: "2026-11-09T10:00:00Z",
	})

	first, rest, _ := strings.Cut(body, "\n")
	assert.Equal(t, "<!-- krci-agent-status state=waiting reason= -->", first)
	assert.Contains(t, rest, "| State | `waiting` |")
	assert.Contains(t, rest, "| Actor | `issue-12-7` |")
	assert.Contains(t, rest, "| Actor UID | `u1` |")
	assert.Contains(t, rest, "| Worker pod | `pod-a` |")
	assert.Contains(t, rest, "| Session | `s1` |")
	assert.Contains(t, rest, "| Updated (UTC) | 2026-11-09T10:00:00Z |")
	assert.NotContains(t, rest, "Reason")

	r, ok := ParseRecord(body)
	assert.True(t, ok)
	assert.Equal(t, Record{State: Waiting}, r)
}

func TestStatusBodyReasonRow(t *testing.T) {
	r := Record{State: Error, Reason: ReasonActorCrashed}
	body := StatusBody(r, StatusView{State: Error, Reason: ReasonActorCrashed})

	assert.Contains(t, body, "| Reason | actor crashed |")
}

func TestStatusViewRoundTrip(t *testing.T) {
	v := StatusView{State: Error, Actor: "issue-12-7", ActorUID: "u1", Pod: "pod-a", Session: "s1",
		Updated: "2026-11-09T10:00:00Z", Reason: ReasonTurnLimit}

	got, ok := ParseStatusView(StatusBody(Record{State: Error, Reason: ReasonTurnLimit}, v))
	assert.True(t, ok)
	assert.Equal(t, v, got)

	_, ok = ParseStatusView("just text")
	assert.False(t, ok)
}

func TestMergeStatus(t *testing.T) {
	prev := StatusView{State: Working, Actor: "issue-12-7", Session: "s1", Updated: "t1"}

	got := MergeStatus(prev, Record{State: Error, Reason: ReasonTurnTimeout}, nil)
	want := StatusView{State: Error, Actor: "issue-12-7", Session: "s1", Updated: "t1", Reason: ReasonTurnTimeout}
	assert.Equal(t, want, got)

	next := &StatusView{State: Working, Actor: "issue-12-7", Session: "s2", Updated: "t2", Reason: "stale"}
	got = MergeStatus(prev, Record{State: Waiting}, next)
	assert.Equal(t, StatusView{State: Waiting, Actor: "issue-12-7", Session: "s2", Updated: "t2"}, got)
}
