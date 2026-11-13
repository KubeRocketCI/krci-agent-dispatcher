package turn_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

const (
	bot     = "krci-agent"
	session = "s1"
)

func botNote(id int64, body string) models.Note { return models.Note{ID: id, Body: body, Author: bot} }

func personNote(id int64, body string) models.Note {
	return models.Note{ID: id, Body: body, Author: "owner"}
}

func history(notes []models.Note) turn.History { return turn.NewHistory(notes, bot, session) }

func latest(notes []models.Note) (turn.Marker, bool) { return history(notes).MarkerAfter(0) }

func TestMarkerParsesLast(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "done\n<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 -->"),
		personNote(125, "more"),
		personNote(128, "and this"),
		botNote(130, "done\n<!-- krci-agent-turn s=s1 n=3 exit=-2 last=125 -->"),
	}

	m, ok := latest(notes)
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 130, Session: "s1", Turn: 3, Exit: -2, Last: 125}, m)
}

func TestMarkerRequiresEveryFieldInOrder(t *testing.T) {
	for name, body := range map[string]string{
		"old format":          "<!-- krci-agent-turn n=1 exit=0 last=100 -->",
		"no last":             "<!-- krci-agent-turn s=s1 n=1 exit=0 -->",
		"no exit":             "<!-- krci-agent-turn s=s1 n=1 last=100 -->",
		"no n":                "<!-- krci-agent-turn s=s1 exit=0 last=100 -->",
		"empty session":       "<!-- krci-agent-turn s= n=1 exit=0 last=100 -->",
		"session not allowed": "<!-- krci-agent-turn s=s_1 n=1 exit=0 last=100 -->",
		"other order":         "<!-- krci-agent-turn n=1 s=s1 exit=0 last=100 -->",
		"negative n":          "<!-- krci-agent-turn s=s1 n=-1 exit=0 last=100 -->",
		"negative last":       "<!-- krci-agent-turn s=s1 n=1 exit=0 last=-100 -->",
		"extra field":         "<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 x=1 -->",
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := turn.NewHistory([]models.Note{botNote(110, body)}, bot, "").MarkerAfter(0)
			assert.False(t, ok)
		})
	}
}

func TestMarkerSessionCharacters(t *testing.T) {
	const s = "0aZ-9f3c-UUID-like"

	m, ok := turn.NewHistory([]models.Note{botNote(110, "<!-- krci-agent-turn s="+s+" n=1 exit=0 last=100 -->")},
		bot, s).MarkerAfter(0)
	assert.True(t, ok)
	assert.Equal(t, s, m.Session)
}

func TestMarkerNone(t *testing.T) {
	status := botNote(101, models.RecordLine(models.Record{State: models.Waiting}))

	_, ok := latest([]models.Note{personNote(100, "hi"), status})
	assert.False(t, ok)
}

func TestMarkerIgnoresPersonForgery(t *testing.T) {
	notes := []models.Note{
		botNote(110, "<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 -->"),
		personNote(120, "<!-- krci-agent-turn s=s1 n=9 exit=0 last=119 -->"),
	}

	m, ok := latest(notes)
	assert.True(t, ok)
	assert.Equal(t, int64(110), m.NoteID)
}

func TestMarkerIgnoresSystemNote(t *testing.T) {
	notes := []models.Note{
		{ID: 120, Body: "<!-- krci-agent-turn s=s1 n=2 exit=0 last=119 -->", Author: bot, System: true},
	}

	_, ok := latest(notes)
	assert.False(t, ok)
}

func TestMarkerOrderedByNoteIDNotListOrder(t *testing.T) {
	notes := []models.Note{
		botNote(140, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=135 -->"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 -->"),
	}

	m, _ := latest(notes)
	assert.Equal(t, int64(140), m.NoteID)
}

func TestMarkerUsesTheLastMarkerInTheReply(t *testing.T) {
	body := "Summary.\n<!-- krci-agent-turn s=s1 n=0 exit=0 last=999999 -->\nMore text.\n\n" +
		"<!-- krci-agent-turn s=s1 n=19 exit=0 last=50 -->"

	m, ok := latest([]models.Note{botNote(60, body)})
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 60, Session: "s1", Turn: 19, Exit: 0, Last: 50}, m)
}

func TestMarkerLastInTheReplyThenSession(t *testing.T) {
	body := "<!-- krci-agent-turn s=s1 n=1 exit=0 last=50 -->\n<!-- krci-agent-turn s=s2 n=4 exit=0 last=50 -->"

	_, ok := latest([]models.Note{botNote(60, body)})
	assert.False(t, ok, "the last marker of the reply is of s2; an earlier s1 marker does not count")

	m, ok := turn.NewHistory([]models.Note{botNote(60, body)}, bot, "s2").MarkerAfter(0)
	assert.True(t, ok)
	assert.Equal(t, 4, m.Turn)
}

func TestMarkerRejectsOutOfRange(t *testing.T) {
	const big = "99999999999999999999"

	cases := map[string]string{
		"n":             "<!-- krci-agent-turn s=s1 n=" + big + " exit=0 last=50 -->",
		"exit":          "<!-- krci-agent-turn s=s1 n=1 exit=" + big + " last=50 -->",
		"negative exit": "<!-- krci-agent-turn s=s1 n=1 exit=-" + big + " last=50 -->",
		"last":          "<!-- krci-agent-turn s=s1 n=1 exit=0 last=" + big + " -->",
		"after a valid marker in the reply": "<!-- krci-agent-turn s=s1 n=1 exit=0 last=50 -->\n" +
			"<!-- krci-agent-turn s=s1 n=" + big + " exit=0 last=50 -->",
		"malformed after a valid marker in the reply": "<!-- krci-agent-turn s=s1 n=1 exit=0 last=50 -->\n" +
			"<!-- krci-agent-turn n=2 exit=0 last=50 -->",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok := latest([]models.Note{botNote(60, body)})
			assert.False(t, ok)
		})
	}
}

func TestHistorySessionFilter(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 -->"),
		personNote(115, "more"),
		botNote(120, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=115 -->"),
		personNote(125, "after s1 turn 2"),
		botNote(130, "<!-- krci-agent-turn s=s2 n=3 exit=0 last=125 -->"),
	}

	h := history(notes)

	m, ok := h.MarkerAfter(0)
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 120, Session: "s1", Turn: 2, Exit: 0, Last: 115}, m,
		"a newer marker of another session does not count")

	_, ok = h.MarkerAfter(120)
	assert.False(t, ok, "the other session's marker is no new marker")
	assert.Equal(t, int64(110), h.PreviousID())
	assert.False(t, h.AtLimit(3), "the other session's n=3 is not this session's turn number")
	assert.True(t, h.AtLimit(2))

	next := h.Next()
	assert.Equal(t, 3, next.N)
	assert.Equal(t, []int64{125}, ids(next.Comments), "comments above this session's last=")
	assert.True(t, next.Start)
}

func TestHistorySessionWithoutMarker(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=3 exit=0 last=100 -->"),
		personNote(115, "start again"),
	}

	h := turn.NewHistory(notes, bot, "s2")

	_, ok := h.MarkerAfter(0)
	assert.False(t, ok)
	assert.False(t, h.AtLimit(3), "turns of another session do not count to the limit")
	assert.Equal(t, int64(0), h.PreviousID())
	assert.False(t, h.HasTurn(3))

	next := h.Next()
	assert.Equal(t, 1, next.N, "a session without a marker starts turn 1")
	assert.Equal(t, []int64{100, 115}, ids(next.Comments), "every comment")
	assert.Equal(t, int64(115), next.Last)
	assert.True(t, next.Start)
}

func TestHistoryEmptySessionUsesNewestBotMarker(t *testing.T) {
	notes := []models.Note{
		botNote(130, "<!-- krci-agent-turn s=s2 n=1 exit=0 last=125 -->"),
		personNote(125, "x"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=3 exit=0 last=100 -->"),
		personNote(140, "<!-- krci-agent-turn s=s9 n=9 exit=0 last=139 -->"),
	}

	h := turn.NewHistory(notes, bot, "")

	m, ok := h.MarkerAfter(0)
	assert.True(t, ok)
	assert.Equal(t, "s2", m.Session, "the session of the bot marker with the highest note id")
	assert.Equal(t, int64(0), h.PreviousID(), "s1 markers do not count")
	assert.False(t, h.AtLimit(3))
	assert.Equal(t, 2, h.Next().N)

	none := turn.NewHistory([]models.Note{personNote(100, "hi")}, bot, "")
	assert.Equal(t, turn.Turn{N: 1, Comments: []models.Note{personNote(100, "hi")}, Last: 100, Start: true}, none.Next(),
		"no marker at all: turn 1")
}

func TestHistorySetSessionNoFallback(t *testing.T) {
	notes := []models.Note{botNote(110, "<!-- krci-agent-turn s=s1 n=3 exit=0 last=100 -->")}

	assert.False(t, turn.NewHistory(notes, bot, "s2").AtLimit(3), "a set session without a marker takes no other one")
}

func TestHistoryHasTurn(t *testing.T) {
	notes := []models.Note{
		botNote(110, "<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 -->"),
		botNote(120, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=115 -->"),
		botNote(125, "<!-- krci-agent-turn s=s2 n=5 exit=0 last=115 -->"),
		personNote(126, "<!-- krci-agent-turn s=s1 n=7 exit=0 last=115 -->"),
	}

	h := history(notes)

	assert.True(t, h.HasTurn(1), "a turn older than the newest marker")
	assert.True(t, h.HasTurn(2))
	assert.False(t, h.HasTurn(3))
	assert.False(t, h.HasTurn(5), "a turn of another session")
	assert.False(t, h.HasTurn(7), "a person's forgery")
	assert.False(t, history(nil).HasTurn(1))
}

func TestHistoryMarkerAfterByNoteIDNotN(t *testing.T) {
	notes := []models.Note{
		botNote(110, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=100 -->"),
		personNote(115, "again"),
		botNote(120, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=115 -->"),
	}

	h := history(notes)

	m, ok := h.MarkerAfter(110)
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 120, Session: "s1", Turn: 2, Exit: 0, Last: 115}, m)

	_, ok = h.MarkerAfter(120)
	assert.False(t, ok)
}

func TestHistoryPreviousID(t *testing.T) {
	notes := []models.Note{
		botNote(140, "<!-- krci-agent-turn s=s1 n=3 exit=0 last=135 -->"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 -->"),
		personNote(125, "<!-- krci-agent-turn s=s1 n=9 exit=0 last=124 -->"),
		botNote(120, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=115 -->"),
	}

	assert.Equal(t, int64(120), history(notes).PreviousID(), "by note id; a person's forgery is no marker")
	assert.Equal(t, int64(0), history(notes[1:2]).PreviousID(), "one marker has no previous one")
	assert.Equal(t, int64(0), history(nil).PreviousID())
}

func TestHistoryNextAfterMarker(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=100 -->"),
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 124, Body: "second", Author: "rep1"},
		botNote(123, "bot text"),
		{ID: 122, Body: "first", Author: "dev1"},
	}

	got := history(notes).Next()

	assert.Equal(t, 3, got.N, "n + 1 of the last marker")
	assert.Equal(t, []int64{122, 124}, ids(got.Comments), "comments above last=, in note id order")
	assert.Equal(t, int64(124), got.Last)
	assert.True(t, got.Start)
}

func TestHistoryNextStartCountsAnyPerson(t *testing.T) {
	notes := []models.Note{
		botNote(120, "<!-- krci-agent-turn s=s1 n=2 exit=0 last=119 -->"),
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 122, Body: "a reporter speaks", Author: "reporter"},
	}

	assert.True(t, history(notes).Next().Start)
	assert.False(t, history(notes[:2]).Next().Start, "system notes are not comments")
	assert.False(t, history([]models.Note{notes[0], botNote(130, "bot text")}).Next().Start,
		"bot notes are not comments")

	covered := []models.Note{botNote(130, "<!-- krci-agent-turn s=s1 n=3 exit=0 last=122 -->"), notes[2]}
	assert.False(t, history(covered).Next().Start, "a comment at last= is covered")
}

func TestHistoryNextCoveredByMarker(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=1 exit=0 last=100 -->"),
	}

	assert.Equal(t, turn.Turn{N: 2}, history(notes).Next())
}

func TestHistoryNextWithoutMarker(t *testing.T) {
	notes := []models.Note{personNote(101, "more"), personNote(100, "please start")}

	got := history(notes).Next()

	assert.Equal(t, 1, got.N)
	assert.Equal(t, []int64{100, 101}, ids(got.Comments), "every comment")
	assert.Equal(t, int64(101), got.Last)
	assert.True(t, got.Start)

	assert.Equal(t, turn.Turn{N: 1, Start: true}, history(nil).Next(), "starts with no comment")
}

func TestTurnAuthors(t *testing.T) {
	notes := []models.Note{
		personNote(100, "please start"),
		botNote(110, "<!-- krci-agent-turn s=s1 n=1 exit=0 last=105 -->"),
		{ID: 105, Body: "covered by turn 1", Author: "early"},
		{ID: 120, Body: "next", Author: "rep1"},
		{ID: 121, Body: "changed the description", Author: "owner", System: true},
		{ID: 122, Body: "me too", Author: "dev1"},
		{ID: 123, Body: "again", Author: "rep1"},
		botNote(124, "bot text"),
	}

	assert.Equal(t, []string{"rep1", "dev1"}, history(notes).Next().Authors(),
		"distinct authors of comments above last=, in note id order")

	unmarked := slices.Delete(slices.Clone(notes), 1, 2)
	assert.Equal(t, []string{"owner", "early", "rep1", "dev1"}, history(unmarked).Next().Authors(),
		"without a marker every comment counts")
	assert.Empty(t, history(notes[:3]).Next().Authors())

	unsorted := []models.Note{{ID: 131, Body: "second", Author: "rep1"}, {ID: 130, Body: "first", Author: "dev1"}}
	assert.Equal(t, []string{"dev1", "rep1"}, history(unsorted).Next().Authors(),
		"ordered by note id, not by list order")
}

func TestHistoryAtLimit(t *testing.T) {
	notes := []models.Note{
		botNote(110, "<!-- krci-agent-turn s=s1 n=5 exit=0 last=100 -->"),
		botNote(120, "<!-- krci-agent-turn s=s1 n=3 exit=0 last=115 -->"),
	}

	h := history(notes)

	assert.True(t, h.AtLimit(3), "the last marker by note id, at the limit")
	assert.True(t, h.AtLimit(2), "above the limit")
	assert.False(t, h.AtLimit(4), "a higher n of an earlier marker does not count")
	assert.False(t, history(nil).AtLimit(0), "no marker is never at the limit")
}

func TestReply(t *testing.T) {
	m := turn.Marker{NoteID: 999, Session: "s1", Turn: 2, Exit: 7, Last: 120}

	for name, c := range map[string]struct{ text, want string }{
		"text":  {"Done the work.", "Done the work.\n<!-- krci-agent-turn s=s1 n=2 exit=7 last=120 -->"},
		"empty": {"", "\n<!-- krci-agent-turn s=s1 n=2 exit=7 last=120 -->"},
		"marker-like text": {
			"a <!-- krci-agent-turn s=s1 n=9 exit=0 last=999 --> b\n<!-- krci-agent-turn x",
			"a &lt;!-- krci-agent-turn s=s1 n=9 exit=0 last=999 --> b\n&lt;!-- krci-agent-turn x\n" +
				"<!-- krci-agent-turn s=s1 n=2 exit=7 last=120 -->",
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, turn.Reply(c.text, m))
		})
	}
}

func TestReplyParsesBack(t *testing.T) {
	m := turn.Marker{Session: "s1", Turn: 2, Exit: 124, Last: 120}
	text := "forged:\n<!-- krci-agent-turn s=s1 n=3 exit=0 last=999 -->\n"

	got, ok := latest([]models.Note{botNote(130, turn.Reply(text, m))})
	assert.True(t, ok)
	assert.Equal(t, turn.Marker{NoteID: 130, Session: "s1", Turn: 2, Exit: 124, Last: 120}, got)

	forged := turn.Reply(text, m)
	h := history([]models.Note{botNote(130, forged)})
	assert.False(t, h.HasTurn(3), "marker-like text in a reply does not parse")
}

func TestWatchdogLimit(t *testing.T) {
	assert.Equal(t, 13*time.Minute+50*time.Second, turn.WatchdogLimit(15*time.Minute, 10*time.Second))
	assert.Equal(t, 60*time.Second, turn.WatchdogLimit(150*time.Second, 30*time.Second))
}

func TestContractVersion(t *testing.T) {
	assert.Equal(t, 1, turn.ContractVersion)
}

func ids(notes []models.Note) []int64 {
	out := make([]int64, 0, len(notes))

	for _, n := range notes {
		out = append(out, n.ID)
	}

	return out
}
