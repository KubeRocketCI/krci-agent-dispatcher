package models

import (
	"cmp"
	"errors"
	"regexp"
	"slices"
	"strconv"
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
func parseMarker(n Note) (Marker, bool) {
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

// TurnHistory is the turn markers and the comments of an issue, ordered by note id, never by turn number: a turn
// repeated after a revert has the same n.
type TurnHistory struct {
	markers  []Marker
	comments []Note
}

// NewTurnHistory reads the turn history from notes: markers from the bot user's notes, comments from the notes of
// persons.
func NewTurnHistory(notes []Note, bot string) TurnHistory {
	var h TurnHistory

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
	slices.SortStableFunc(h.comments, func(a, b Note) int { return cmp.Compare(a.ID, b.ID) })

	return h
}

// Last returns the marker with the highest note id.
func (h TurnHistory) Last() (Marker, bool) { return h.After(0) }

// After returns the marker with the highest note id above since.
func (h TurnHistory) After(since int64) (Marker, bool) {
	if len(h.markers) == 0 || h.markers[len(h.markers)-1].NoteID <= since {
		return Marker{}, false
	}

	return h.markers[len(h.markers)-1], true
}

// PreviousID returns the note id of the marker before the last one; 0 when there is none.
func (h TurnHistory) PreviousID() int64 {
	if len(h.markers) < 2 {
		return 0
	}

	return h.markers[len(h.markers)-2].NoteID
}

// CommentAfter reports whether a comment has a note id above id.
func (h TurnHistory) CommentAfter(id int64) bool {
	return slices.ContainsFunc(h.comments, func(n Note) bool { return n.ID > id })
}

// UncoveredAuthors returns the distinct authors of the comments no turn covered: above the last marker's last=, or
// every comment when there is no marker. Authors are in note id order of their first such comment.
func (h TurnHistory) UncoveredAuthors() []string {
	var last int64
	if m, ok := h.Last(); ok {
		last = m.Last
	}

	var out []string

	for _, n := range h.comments {
		if n.ID > last && !slices.Contains(out, n.Author) {
			out = append(out, n.Author)
		}
	}

	return out
}
