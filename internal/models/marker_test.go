package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLastMarkerParsesLast(t *testing.T) {
	notes := []Note{
		personNote(100, "please start"),
		botNote(110, "done\n<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(125, "more"),
		personNote(128, "and this"),
		botNote(130, "done\n<!-- krci-agent-turn n=3 exit=2 last=125 -->"),
	}

	m, ok := LastMarker(notes, bot)
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 130, Turn: 3, Exit: 2, Last: 125}, m)
}

func TestLastMarkerFallsBackToID(t *testing.T) {
	m, ok := LastMarker([]Note{botNote(120, "done\n<!-- krci-agent-turn n=2 exit=0 -->")}, bot)
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 120, Turn: 2, Exit: 0, Last: 120}, m)
}

func TestLastMarkerNone(t *testing.T) {
	_, ok := LastMarker([]Note{personNote(100, "hi"), botNote(101, RecordLine(Record{State: Waiting}))}, bot)
	assert.False(t, ok)
}

func TestLastMarkerIgnoresPersonForgery(t *testing.T) {
	notes := []Note{
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(120, "<!-- krci-agent-turn n=9 exit=0 last=119 -->"),
	}

	m, ok := LastMarker(notes, bot)
	assert.True(t, ok)
	assert.Equal(t, int64(110), m.NoteID)
}

func TestLastMarkerIgnoresSystemNote(t *testing.T) {
	notes := []Note{{ID: 120, Body: "<!-- krci-agent-turn n=2 exit=0 last=119 -->", Author: bot, System: true}}

	_, ok := LastMarker(notes, bot)
	assert.False(t, ok)
}

func TestLastMarkerOrderedByNoteIDNotListOrder(t *testing.T) {
	notes := []Note{
		botNote(140, "<!-- krci-agent-turn n=2 exit=0 last=135 -->"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
	}

	m, _ := LastMarker(notes, bot)
	assert.Equal(t, int64(140), m.NoteID)
}

func TestMarkerAfterByNoteIDNotN(t *testing.T) {
	notes := []Note{
		botNote(110, "<!-- krci-agent-turn n=2 exit=0 last=100 -->"),
		personNote(115, "again"),
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=115 -->"),
	}

	m, ok := MarkerAfter(notes, bot, 110)
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 120, Turn: 2, Exit: 0, Last: 115}, m)

	_, ok = MarkerAfter(notes, bot, 120)
	assert.False(t, ok)
}

func TestCommentAfterCountsAnyPerson(t *testing.T) {
	notes := []Note{
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=119 -->"),
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 122, Body: "a reporter speaks", Author: "reporter"},
	}

	assert.True(t, CommentAfter(notes, bot, 120))
	assert.False(t, CommentAfter(notes, bot, 122))
	assert.False(t, CommentAfter(notes[:2], bot, 120), "system notes are not comments")
	assert.False(t, CommentAfter([]Note{botNote(130, "bot text")}, bot, 120), "bot notes are not comments")
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

func TestMarkerUsesTheLastMarkerInTheReply(t *testing.T) {
	body := "Summary.\n<!-- krci-agent-turn n=0 exit=0 last=999999 -->\nMore text.\n\n" +
		"<!-- krci-agent-turn n=19 exit=0 last=50 -->"

	m, ok := LastMarker([]Note{botNote(60, body)}, bot)
	assert.True(t, ok)
	assert.Equal(t, Marker{NoteID: 60, Turn: 19, Exit: 0, Last: 50}, m)
}
