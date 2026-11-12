package models

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTurnHistoryLastParsesLast(t *testing.T) {
	notes := []Note{
		personNote(100, "please start"),
		botNote(110, "done\n<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(125, "more"),
		personNote(128, "and this"),
		botNote(130, "done\n<!-- krci-agent-turn n=3 exit=2 last=125 -->"),
	}

	m, ok := NewTurnHistory(notes, bot).Last()
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 130, Turn: 3, Exit: 2, Last: 125}, m)
}

func TestTurnHistoryLastFallsBackToID(t *testing.T) {
	m, ok := NewTurnHistory([]Note{botNote(120, "done\n<!-- krci-agent-turn n=2 exit=0 -->")}, bot).Last()
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 120, Turn: 2, Exit: 0, Last: 120}, m)
}

func TestTurnHistoryLastNone(t *testing.T) {
	_, ok := NewTurnHistory([]Note{personNote(100, "hi"), botNote(101, RecordLine(Record{State: Waiting}))}, bot).Last()
	assert.False(t, ok)
}

func TestTurnHistoryLastIgnoresPersonForgery(t *testing.T) {
	notes := []Note{
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(120, "<!-- krci-agent-turn n=9 exit=0 last=119 -->"),
	}

	m, ok := NewTurnHistory(notes, bot).Last()
	assert.True(t, ok)
	assert.Equal(t, int64(110), m.NoteID)
}

func TestTurnHistoryLastIgnoresSystemNote(t *testing.T) {
	notes := []Note{{ID: 120, Body: "<!-- krci-agent-turn n=2 exit=0 last=119 -->", Author: bot, System: true}}

	_, ok := NewTurnHistory(notes, bot).Last()
	assert.False(t, ok)
}

func TestTurnHistoryLastOrderedByNoteIDNotListOrder(t *testing.T) {
	notes := []Note{
		botNote(140, "<!-- krci-agent-turn n=2 exit=0 last=135 -->"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
	}

	m, _ := NewTurnHistory(notes, bot).Last()
	assert.Equal(t, int64(140), m.NoteID)
}

func TestTurnHistoryAfterByNoteIDNotN(t *testing.T) {
	notes := []Note{
		botNote(110, "<!-- krci-agent-turn n=2 exit=0 last=100 -->"),
		personNote(115, "again"),
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=115 -->"),
	}

	h := NewTurnHistory(notes, bot)

	m, ok := h.After(110)
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 120, Turn: 2, Exit: 0, Last: 115}, m)

	_, ok = h.After(120)
	assert.False(t, ok)
}

func TestTurnHistoryCommentAfterCountsAnyPerson(t *testing.T) {
	notes := []Note{
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=119 -->"),
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 122, Body: "a reporter speaks", Author: "reporter"},
	}

	assert.True(t, NewTurnHistory(notes, bot).CommentAfter(120))
	assert.False(t, NewTurnHistory(notes, bot).CommentAfter(122))
	assert.False(t, NewTurnHistory(notes[:2], bot).CommentAfter(120), "system notes are not comments")
	assert.False(t, NewTurnHistory([]Note{botNote(130, "bot text")}, bot).CommentAfter(120), "bot notes are not comments")
}

func TestHasMarker(t *testing.T) {
	notes := []Note{
		personNote(100, "<!-- krci-agent-refused -->"),
		botNote(101, "Agent stopped for this issue. Open a new issue to start again.\n<!-- krci-agent-refused -->"),
	}

	assert.True(t, HasMarker(notes, bot, RefusedMarker))
	assert.False(t, HasMarker(notes[:1], bot, RefusedMarker), "a person's copy does not count")
	assert.False(t, HasMarker(notes, bot, StartRefusedMarker))
}

func TestTurnHistoryUsesTheLastMarkerInTheReply(t *testing.T) {
	body := "Summary.\n<!-- krci-agent-turn n=0 exit=0 last=999999 -->\nMore text.\n\n" +
		"<!-- krci-agent-turn n=19 exit=0 last=50 -->"

	m, ok := NewTurnHistory([]Note{botNote(60, body)}, bot).Last()
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 60, Turn: 19, Exit: 0, Last: 50}, m)
}

func TestTurnHistoryPreviousID(t *testing.T) {
	notes := []Note{
		botNote(140, "<!-- krci-agent-turn n=3 exit=0 last=135 -->"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(125, "<!-- krci-agent-turn n=9 exit=0 last=124 -->"),
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=115 -->"),
	}

	assert.Equal(t, int64(120), NewTurnHistory(notes, bot).PreviousID(), "by note id; a person's forgery is no marker")
	assert.Equal(t, int64(0), NewTurnHistory(notes[1:2], bot).PreviousID(), "one marker has no previous one")
	assert.Equal(t, int64(0), NewTurnHistory(nil, bot).PreviousID())
}

func TestTurnHistoryUncoveredAuthors(t *testing.T) {
	notes := []Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=105 -->"),
		{ID: 105, Body: "covered by turn 1", Author: "early"},
		{ID: 120, Body: "next", Author: "rep1"},
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 122, Body: "me too", Author: "dev1"},
		{ID: 123, Body: "again", Author: "rep1"},
		botNote(124, "bot text"),
	}

	assert.Equal(t, []string{"rep1", "dev1"}, NewTurnHistory(notes, bot).UncoveredAuthors(),
		"distinct authors of comments above last=, in note id order")

	unmarked := slices.Delete(slices.Clone(notes), 1, 2)
	assert.Equal(t, []string{"owner", "early", "rep1", "dev1"}, NewTurnHistory(unmarked, bot).UncoveredAuthors(),
		"without a marker every comment counts")
	assert.Empty(t, NewTurnHistory(notes[:3], bot).UncoveredAuthors())

	unsorted := []Note{{ID: 131, Body: "second", Author: "rep1"}, {ID: 130, Body: "first", Author: "dev1"}}
	assert.Equal(t, []string{"dev1", "rep1"}, NewTurnHistory(unsorted, bot).UncoveredAuthors(),
		"ordered by note id, not by list order")
}
