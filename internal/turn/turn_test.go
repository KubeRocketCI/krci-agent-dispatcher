package turn_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

const bot = "krci-agent"

func botNote(id int64, body string) models.Note { return models.Note{ID: id, Body: body, Author: bot} }

func personNote(id int64, body string) models.Note {
	return models.Note{ID: id, Body: body, Author: "owner"}
}

func latest(notes []models.Note) (turn.Marker, bool) {
	return turn.NewHistory(notes, bot).MarkerAfter(0)
}

func TestMarkerParsesLast(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "done\n<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(125, "more"),
		personNote(128, "and this"),
		botNote(130, "done\n<!-- krci-agent-turn n=3 exit=2 last=125 -->"),
	}

	m, ok := latest(notes)
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 130, Turn: 3, Exit: 2, Last: 125}, m)
}

func TestMarkerLastFallsBackToID(t *testing.T) {
	m, ok := latest([]models.Note{botNote(120, "done\n<!-- krci-agent-turn n=2 exit=0 -->")})
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 120, Turn: 2, Exit: 0, Last: 120}, m)
}

func TestMarkerNone(t *testing.T) {
	status := botNote(101, models.RecordLine(models.Record{State: models.Waiting}))

	_, ok := latest([]models.Note{personNote(100, "hi"), status})
	assert.False(t, ok)
}

func TestMarkerIgnoresPersonForgery(t *testing.T) {
	notes := []models.Note{
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(120, "<!-- krci-agent-turn n=9 exit=0 last=119 -->"),
	}

	m, ok := latest(notes)
	assert.True(t, ok)
	assert.Equal(t, int64(110), m.NoteID)
}

func TestMarkerIgnoresSystemNote(t *testing.T) {
	notes := []models.Note{{ID: 120, Body: "<!-- krci-agent-turn n=2 exit=0 last=119 -->", Author: bot, System: true}}

	_, ok := latest(notes)
	assert.False(t, ok)
}

func TestMarkerOrderedByNoteIDNotListOrder(t *testing.T) {
	notes := []models.Note{
		botNote(140, "<!-- krci-agent-turn n=2 exit=0 last=135 -->"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
	}

	m, _ := latest(notes)
	assert.Equal(t, int64(140), m.NoteID)
}

func TestMarkerUsesTheLastMarkerInTheReply(t *testing.T) {
	body := "Summary.\n<!-- krci-agent-turn n=0 exit=0 last=999999 -->\nMore text.\n\n" +
		"<!-- krci-agent-turn n=19 exit=0 last=50 -->"

	m, ok := latest([]models.Note{botNote(60, body)})
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 60, Turn: 19, Exit: 0, Last: 50}, m)
}

func TestMarkerRejectsOutOfRange(t *testing.T) {
	const big = "99999999999999999999"

	cases := map[string]string{
		"n":             "<!-- krci-agent-turn n=" + big + " exit=0 last=50 -->",
		"exit":          "<!-- krci-agent-turn n=1 exit=" + big + " last=50 -->",
		"negative exit": "<!-- krci-agent-turn n=1 exit=-" + big + " last=50 -->",
		"last":          "<!-- krci-agent-turn n=1 exit=0 last=" + big + " -->",
		"after a valid marker in the reply": "<!-- krci-agent-turn n=1 exit=0 last=50 -->\n" +
			"<!-- krci-agent-turn n=" + big + " exit=0 last=50 -->",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok := latest([]models.Note{botNote(60, body)})
			assert.False(t, ok)
		})
	}
}

func TestHistoryMarkerAfterByNoteIDNotN(t *testing.T) {
	notes := []models.Note{
		botNote(110, "<!-- krci-agent-turn n=2 exit=0 last=100 -->"),
		personNote(115, "again"),
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=115 -->"),
	}

	h := turn.NewHistory(notes, bot)

	m, ok := h.MarkerAfter(110)
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 120, Turn: 2, Exit: 0, Last: 115}, m)

	_, ok = h.MarkerAfter(120)
	assert.False(t, ok)
}

func TestHistoryPreviousID(t *testing.T) {
	notes := []models.Note{
		botNote(140, "<!-- krci-agent-turn n=3 exit=0 last=135 -->"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
		personNote(125, "<!-- krci-agent-turn n=9 exit=0 last=124 -->"),
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=115 -->"),
	}

	assert.Equal(t, int64(120), turn.NewHistory(notes, bot).PreviousID(), "by note id; a person's forgery is no marker")
	assert.Equal(t, int64(0), turn.NewHistory(notes[1:2], bot).PreviousID(), "one marker has no previous one")
	assert.Equal(t, int64(0), turn.NewHistory(nil, bot).PreviousID())
}

func TestHistoryNextAfterMarker(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn n=2 exit=0 last=100 -->"),
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 124, Body: "second", Author: "rep1"},
		botNote(123, "bot text"),
		{ID: 122, Body: "first", Author: "dev1"},
	}

	got := turn.NewHistory(notes, bot).Next()

	assert.Equal(t, 3, got.N, "n + 1 of the last marker")
	assert.Equal(t, []int64{122, 124}, ids(got.Comments), "comments above last=, in note id order")
	assert.Equal(t, int64(124), got.Last)
	assert.True(t, got.Start)
}

func TestHistoryNextStartCountsAnyPerson(t *testing.T) {
	notes := []models.Note{
		botNote(120, "<!-- krci-agent-turn n=2 exit=0 last=119 -->"),
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 122, Body: "a reporter speaks", Author: "reporter"},
	}

	assert.True(t, turn.NewHistory(notes, bot).Next().Start)
	assert.False(t, turn.NewHistory(notes[:2], bot).Next().Start, "system notes are not comments")
	assert.False(t, turn.NewHistory([]models.Note{notes[0], botNote(130, "bot text")}, bot).Next().Start,
		"bot notes are not comments")

	covered := []models.Note{botNote(130, "<!-- krci-agent-turn n=3 exit=0 last=122 -->"), notes[2]}
	assert.False(t, turn.NewHistory(covered, bot).Next().Start, "a comment at last= is covered")
}

func TestHistoryNextCoveredByMarker(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=100 -->"),
	}

	got := turn.NewHistory(notes, bot).Next()

	assert.Equal(t, turn.Turn{N: 2}, got)
}

func TestHistoryNextWithoutMarker(t *testing.T) {
	notes := []models.Note{personNote(101, "more"), personNote(100, "please start")}

	got := turn.NewHistory(notes, bot).Next()

	assert.Equal(t, 1, got.N)
	assert.Equal(t, []int64{100, 101}, ids(got.Comments), "every comment")
	assert.Equal(t, int64(101), got.Last)
	assert.True(t, got.Start)

	assert.Equal(t, turn.Turn{N: 1, Start: true}, turn.NewHistory(nil, bot).Next(), "starts with no comment")
}

func TestTurnAuthors(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn n=1 exit=0 last=105 -->"),
		{ID: 105, Body: "covered by turn 1", Author: "early"},
		{ID: 120, Body: "next", Author: "rep1"},
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 122, Body: "me too", Author: "dev1"},
		{ID: 123, Body: "again", Author: "rep1"},
		botNote(124, "bot text"),
	}

	assert.Equal(t, []string{"rep1", "dev1"}, turn.NewHistory(notes, bot).Next().Authors(),
		"distinct authors of comments above last=, in note id order")

	unmarked := slices.Delete(slices.Clone(notes), 1, 2)
	assert.Equal(t, []string{"owner", "early", "rep1", "dev1"}, turn.NewHistory(unmarked, bot).Next().Authors(),
		"without a marker every comment counts")
	assert.Empty(t, turn.NewHistory(notes[:3], bot).Next().Authors())

	unsorted := []models.Note{{ID: 131, Body: "second", Author: "rep1"}, {ID: 130, Body: "first", Author: "dev1"}}
	assert.Equal(t, []string{"dev1", "rep1"}, turn.NewHistory(unsorted, bot).Next().Authors(),
		"ordered by note id, not by list order")
}

func TestHistoryAtLimit(t *testing.T) {
	notes := []models.Note{
		botNote(110, "<!-- krci-agent-turn n=5 exit=0 last=100 -->"),
		botNote(120, "<!-- krci-agent-turn n=3 exit=0 last=115 -->"),
	}

	h := turn.NewHistory(notes, bot)

	assert.True(t, h.AtLimit(3), "the last marker by note id, at the limit")
	assert.True(t, h.AtLimit(2), "above the limit")
	assert.False(t, h.AtLimit(4), "a higher n of an earlier marker does not count")
	assert.False(t, turn.NewHistory(nil, bot).AtLimit(0), "no marker is never at the limit")
}

func ids(notes []models.Note) []int64 {
	out := make([]int64, 0, len(notes))

	for _, n := range notes {
		out = append(out, n.ID)
	}

	return out
}
