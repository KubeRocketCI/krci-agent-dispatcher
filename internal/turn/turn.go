// Package turn is the turn protocol: turn markers in the bot user's replies, comments, the turn history and the
// next turn.
package turn

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// ContractVersion is the version of the dispatcher-harness contract; the actor env AGENT_CONTRACT_VERSION
// carries it.
const ContractVersion = 1

// watchdogMargin is the part of the turn timeout after the watchdog limit and one harness poll.
const watchdogMargin = 60 * time.Second

// WatchdogLimit returns the harness watchdog limit of one turn: turnTimeout − poll − 60 s. poll is the harness
// poll interval.
func WatchdogLimit(turnTimeout, poll time.Duration) time.Duration {
	return turnTimeout - poll - watchdogMargin
}

// Marker is a parsed turn marker: <!-- krci-agent-turn s=<session> n=<n> exit=<rc> last=<note-id> -->.
// NoteID is the note that carries it; Last is the highest comment note id the turn covered.
type Marker struct {
	NoteID  int64
	Session string
	Turn    int
	Exit    int
	Last    int64
}

const (
	markerPrefix  = "<!-- krci-agent-turn"
	markerEscaped = "&lt;!-- krci-agent-turn"
)

// markerLikeRe finds every marker-like text; markerRe parses one. A marker-like text that does not parse is no
// marker, and it still hides an earlier marker in the same note.
var (
	markerLikeRe = regexp.MustCompile(regexp.QuoteMeta(markerPrefix) + `[^\n]*?-->`)
	markerRe     = regexp.MustCompile(`^` + regexp.QuoteMeta(markerPrefix) +
		` s=(\S+) n=(\d+) exit=(-?\d+) last=(\d+) -->$`)
)

// render writes m as a turn marker; NoteID is not part of it.
func render(m Marker) string {
	return fmt.Sprintf("%s s=%s n=%d exit=%d last=%d -->", markerPrefix, m.Session, m.Turn, m.Exit, m.Last)
}

// Reply returns the body of a reply: text, then the marker m as its own last line. Every marker prefix in text is
// written as &lt;!-- krci-agent-turn, so text never parses as a marker.
func Reply(text string, m Marker) string {
	return strings.ReplaceAll(text, markerPrefix, markerEscaped) + "\n" + render(m)
}

// parseMarker reads the last marker-like text of a note. The harness appends the real marker after the agent's
// text, so an earlier marker-like line in that text is ignored. A number out of range or a session outside
// [0-9A-Za-z-]+ is no marker.
func parseMarker(n models.Note) (Marker, bool) {
	all := markerLikeRe.FindAllString(n.Body, -1)
	if len(all) == 0 {
		return Marker{}, false
	}

	sub := markerRe.FindStringSubmatch(all[len(all)-1])
	if sub == nil || !models.ValidSessionID(sub[1]) {
		return Marker{}, false
	}

	turn, errTurn := strconv.Atoi(sub[2])
	exit, errExit := strconv.Atoi(sub[3])
	last, errLast := strconv.ParseInt(sub[4], 10, 64)

	if errors.Join(errTurn, errExit, errLast) != nil {
		return Marker{}, false
	}

	return Marker{NoteID: n.ID, Session: sub[1], Turn: turn, Exit: exit, Last: last}, true
}

func isComment(n models.Note, bot string) bool { return !n.System && n.Author != bot }

func isBotNote(n models.Note, bot string) bool { return !n.System && n.Author == bot }

// History is the turn markers of one session and the comments of an issue, ordered by note id, never by turn
// number: a turn repeated after a revert has the same n.
type History struct {
	markers  []Marker
	comments []models.Note
}

// NewHistory reads the turn history of session from notes: markers from the bot user's notes, comments from the
// notes of persons. An empty session is the session of the bot user's marker with the highest note id: the
// recovery rule for an issue whose status note names no session. A set session without a marker takes no other
// session's markers.
func NewHistory(notes []models.Note, bot, session string) History {
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

	h.markers = ofSession(h.markers, session)

	return h
}

// ofSession returns the markers of session; an empty session is the session of the last marker.
func ofSession(markers []Marker, session string) []Marker {
	if session == "" && len(markers) > 0 {
		session = markers[len(markers)-1].Session
	}

	return slices.DeleteFunc(markers, func(m Marker) bool { return m.Session != session })
}

// HasTurn reports whether any marker of the session has turn number n, not only the last one.
func (h History) HasTurn(n int) bool {
	return slices.ContainsFunc(h.markers, func(m Marker) bool { return m.Turn == n })
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
