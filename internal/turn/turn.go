// Package turn is the turn protocol: turn markers in the bot user's replies, comments, the turn history and the
// next turn.
package turn

import (
	"cmp"
	"errors"
	"regexp"
	"slices"
	"strconv"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// Marker is a parsed turn marker: <!-- krci-agent-turn n=<n> exit=<rc> last=<note-id> -->.
// Last is the highest note id the turn covered; NoteID when the reply has no last= field.
type Marker struct {
	NoteID int64
	Turn   int
	Exit   int
	Last   int64
}

var markerRe = regexp.MustCompile(`<!-- krci-agent-turn n=(\d+) exit=(-?\d+)(?: last=(\d+))? -->`)

// parseMarker reads the last turn marker of a reply. The agent loop appends the real marker after the agent's
// text, so an earlier marker-like line in that text is ignored. A last marker with a number out of range is no
// marker.
func parseMarker(n models.Note) (Marker, bool) {
	all := markerRe.FindAllStringSubmatch(n.Body, -1)
	if len(all) == 0 {
		return Marker{}, false
	}

	sub := all[len(all)-1]

	turn, errTurn := strconv.Atoi(sub[1])
	exit, errExit := strconv.Atoi(sub[2])
	last, errLast := n.ID, error(nil)

	if sub[3] != "" {
		last, errLast = strconv.ParseInt(sub[3], 10, 64)
	}

	if errors.Join(errTurn, errExit, errLast) != nil {
		return Marker{}, false
	}

	return Marker{NoteID: n.ID, Turn: turn, Exit: exit, Last: last}, true
}

func isComment(n models.Note, bot string) bool { return !n.System && n.Author != bot }

func isBotNote(n models.Note, bot string) bool { return !n.System && n.Author == bot }

// History is the turn markers and the comments of an issue, ordered by note id, never by turn number: a turn
// repeated after a revert has the same n.
type History struct {
	markers  []Marker
	comments []models.Note
}

// NewHistory reads the turn history from notes: markers from the bot user's notes, comments from the notes of
// persons.
func NewHistory(notes []models.Note, bot string) History {
	var h History

	for _, n := range notes {
		switch {
		case isComment(n, bot):
			h.comments = append(h.comments, n)
		case isBotNote(n, bot):
			if m, ok := parseMarker(n); ok {
				h.markers = append(h.markers, m)
			}
		}
	}

	slices.SortStableFunc(h.markers, func(a, b Marker) int { return cmp.Compare(a.NoteID, b.NoteID) })
	slices.SortStableFunc(h.comments, func(a, b models.Note) int { return cmp.Compare(a.ID, b.ID) })

	return h
}

// last returns the marker with the highest note id.
func (h History) last() (Marker, bool) {
	if len(h.markers) == 0 {
		return Marker{}, false
	}

	return h.markers[len(h.markers)-1], true
}

// MarkerAfter returns the marker with the highest note id when that id is above since.
func (h History) MarkerAfter(since int64) (Marker, bool) {
	m, ok := h.last()
	if !ok || m.NoteID <= since {
		return Marker{}, false
	}

	return m, true
}

// PreviousID returns the note id of the marker before the last one; 0 when there is none.
func (h History) PreviousID() int64 {
	if len(h.markers) < 2 {
		return 0
	}

	return h.markers[len(h.markers)-2].NoteID
}

// AtLimit reports whether the last marker's turn number is at or above limit; false without a marker.
func (h History) AtLimit(limit int) bool {
	m, ok := h.last()

	return ok && m.Turn >= limit
}

// Turn is the next turn of a history. Comments are the comments no turn covered, in note id order; Last is the
// highest note id among them, 0 when there is none.
type Turn struct {
	N        int
	Comments []models.Note
	Last     int64
	Start    bool
}

// Next returns the next turn: after the last marker, turn n + 1 over the comments above its last=, started when there
// is one; without a marker, turn 1 over every comment, started with or without a comment.
func (h History) Next() Turn {
	m, marked := h.last()
	t := Turn{N: m.Turn + 1}

	for _, n := range h.comments {
		if n.ID > m.Last {
			t.Comments = append(t.Comments, n)
			t.Last = n.ID
		}
	}

	t.Start = !marked || len(t.Comments) > 0

	return t
}

// Authors returns the distinct authors of t's comments in note id order of their first comment.
func (t Turn) Authors() []string {
	var out []string

	for _, n := range t.Comments {
		if !slices.Contains(out, n.Author) {
			out = append(out, n.Author)
		}
	}

	return out
}
