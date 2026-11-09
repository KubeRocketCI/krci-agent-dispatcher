package models

import (
	"regexp"
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
// text, so an earlier marker-like line in that text is ignored.
func parseMarker(n Note) (Marker, bool) {
	all := markerRe.FindAllStringSubmatch(n.Body, -1)
	if len(all) == 0 {
		return Marker{}, false
	}

	sub := all[len(all)-1]

	m := Marker{NoteID: n.ID, Last: n.ID}
	m.Turn, _ = strconv.Atoi(sub[1])
	m.Exit, _ = strconv.Atoi(sub[2])

	if sub[3] != "" {
		m.Last, _ = strconv.ParseInt(sub[3], 10, 64)
	}

	return m, true
}

// LastMarker returns the turn marker with the highest note id among the bot user's notes.
func LastMarker(notes []Note, bot string) (Marker, bool) { return MarkerAfter(notes, bot, 0) }

// MarkerAfter returns the turn marker with the highest note id above afterNoteID among the bot user's notes.
// Markers are ordered by note id, never by turn number: a turn repeated after a revert has the same n.
func MarkerAfter(notes []Note, bot string, afterNoteID int64) (Marker, bool) {
	var (
		best  Marker
		found bool
	)

	for _, n := range notes {
		if !isBotNote(n, bot) || n.ID <= afterNoteID {
			continue
		}

		m, ok := parseMarker(n)
		if ok && (!found || m.NoteID > best.NoteID) {
			best, found = m, true
		}
	}

	return best, found
}
