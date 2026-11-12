package decide

import (
	"bytes"
	"flag"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// Note ids of the golden map fixtures. pollSince is PollFacts.Since: the note id of the turn's previous marker.
// markerID is the marker under test, lastID its last=; commentID is above lastID.
const (
	pollSince  = 50
	prevMarker = 40
	markerID   = 100
	lastID     = 60
	commentID  = 80
	person     = "dev1"
)

// maxAxes and maxValues bound a golden map: a row holds one uint16 value set per axis.
const (
	maxAxes   = 12
	maxValues = 16
)

// axis is one input of Decide or Poll: its values in golden order, each with how it sets the facts T.
type axis[T any] struct {
	name   string
	values []axisValue[T]
}

type axisValue[T any] struct {
	name string
	set  func(*T)
}

func (a *axis[T]) add(name string, set func(*T)) {
	a.values = append(a.values, axisValue[T]{name: name, set: set})
}

type actorValue struct {
	name string
	view models.ActorView
}

// actorValues are the actor axis values. Every models.ActorState is the state of one of them.
var actorValues = []actorValue{
	{"none", noActor},
	{"template", models.ActorView{Template: true, State: models.None}},
	{"policy-only", models.ActorView{EgressPolicy: true, State: models.None}},
	{"partial", partial},
	{"partial-running", models.ActorView{Template: true, Actor: true, State: models.Running}},
	{"partial-crashed", models.ActorView{Template: true, Actor: true, State: models.Crashed}},
	{"suspended", suspended},
	{"running", running},
	{"crashed", crashed},
	{"resuming", moving},
	{"suspending", models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Suspending}},
	{"reverting", models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Reverting}},
	{"deleting", models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Deleting}},
	{"unspecified", models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Unspecified}},
	{"unknown", models.ActorView{State: models.Unknown}},
}

func markerBody(turn int, last int64) string {
	return "reply\n<!-- krci-agent-turn n=" + strconv.Itoa(turn) + " exit=0 last=" + strconv.FormatInt(last, 10) + " -->"
}

// markerAxis returns the marker axis: none, turns 1 to the turn limit, and a marker written by a person. Each
// marker note has note id id; extra values come first.
func markerAxis[T any](view func(*T) *models.IssueView, id int64, extra ...axisValue[T]) *axis[T] {
	a := &axis[T]{name: "marker", values: append([]axisValue[T]{{name: "none", set: func(*T) {}}}, extra...)}

	for turn := 1; turn <= limits.TurnLimit; turn++ {
		name := "turn-" + strconv.Itoa(turn)
		if turn == limits.TurnLimit {
			name = "at-limit"
		}

		a.add(name, func(t *T) { *view(t) = issue{*view(t)}.marker(id, turn, lastID).v })
	}

	a.add("by-person", func(t *T) {
		*view(t) = issue{*view(t)}.note(id, person, markerBody(limits.TurnLimit, lastID)).v
	})

	return a
}

// issueAxes returns the issue state, trigger label and actor axes shared by Decide and Poll.
func issueAxes[T any](
	view func(*T) *models.IssueView, actors []actorValue, actor func(*T) *models.ActorView,
) (s, l, a *axis[T]) {
	s = &axis[T]{name: "issue"}
	for _, st := range []string{models.IssueOpened, models.IssueClosed} {
		s.add(st, func(t *T) { view(t).Issue.IssueState = st })
	}

	l = &axis[T]{name: "trigger-label"}
	l.add("yes", func(t *T) { view(t).Issue.Labels = append(view(t).Issue.Labels, limits.TriggerLabel) })
	l.add("no", func(*T) {})

	return s, l, actorAxis(actors, actor)
}

func actorAxis[T any](values []actorValue, actor func(*T) *models.ActorView) *axis[T] {
	a := &axis[T]{name: "actor"}
	for _, v := range values {
		a.add(v.name, func(t *T) { *actor(t) = v.view })
	}

	return a
}

func requestAxis() *axis[Facts] {
	request := &axis[Facts]{name: "request"}

	for _, k := range intake.Kinds() {
		if k != intake.Ignore {
			request.add(k.String(), func(f *Facts) { f.Request = k })
		}
	}

	return request
}

// unreadAxes are the inputs of Decide after a failed issue read. The request axis shows that the request does not
// decide; the actor axis shows that the actor decides for an unavailable issue only.
func unreadAxes() []*axis[Facts] {
	read := &axis[Facts]{name: "issue-read"}

	for _, r := range IssueReads() {
		if r != ReadOK {
			read.add(r.String(), func(f *Facts) { f.Issue = r })
		}
	}

	actor := actorAxis(actorValues, func(f *Facts) *models.ActorView { return &f.Actor })

	return []*axis[Facts]{read, requestAxis(), actor}
}

// decideAxes are the inputs of Decide, outermost first. Request kinds and agent states come from their
// enumerators, so a new value adds a value to the golden map header.
func decideAxes() []*axis[Facts] {
	view := func(f *Facts) *models.IssueView { return &f.View }
	issueState, trigger, actor := issueAxes(view, actorValues, func(f *Facts) *models.ActorView { return &f.Actor })

	record, label := &axis[Facts]{name: "record"}, &axis[Facts]{name: "state-label"}
	record.add("none", func(*Facts) {})
	label.add("none", func(*Facts) {})

	for _, s := range models.AgentStates() {
		record.add(string(s), func(f *Facts) { f.View.Record, f.View.HasRecord = models.Record{State: s}, true })
		label.add(string(s), func(f *Facts) { f.View.Issue.Labels = append(f.View.Issue.Labels, models.StateLabel(s)) })
	}

	for _, pair := range [][2]models.AgentState{{models.Done, models.Error}, {models.Working, models.Waiting}} {
		label.add(string(pair[0])+"+"+string(pair[1]), func(f *Facts) {
			f.View.Issue.Labels = append(f.View.Issue.Labels, models.StateLabel(pair[0]), models.StateLabel(pair[1]))
		})
	}

	request := requestAxis()

	afterLast := &axis[Facts]{name: "trigger-after-last"}
	afterLast.add("no", func(*Facts) {})
	afterLast.add("yes", func(f *Facts) { f.TriggerAfterLast = true })

	author := &axis[Facts]{name: "author"}
	author.add("ok", func(f *Facts) { f.AuthorOK = true })
	author.add("refused", func(*Facts) {})
	author.add("unknown", func(f *Facts) { f.AuthorUnknown = true })

	rechecks := &axis[Facts]{name: "rechecks"}
	for n := range MaxRechecks + 1 {
		rechecks.add(strconv.Itoa(n), func(f *Facts) { f.Rechecks = n })
	}

	return []*axis[Facts]{
		issueState, trigger, record, label, request, actor, markerAxis(view, markerID), afterLast, author, rechecks,
	}
}

// pollAxes are the inputs of Poll, outermost first.
func pollAxes() []*axis[PollFacts] {
	view := func(p *PollFacts) *models.IssueView { return &p.View }
	issueState, trigger, actor := issueAxes(view, actorValues,
		func(p *PollFacts) *models.ActorView { return &p.Actor })

	stop := &axis[PollFacts]{name: "stop-pending"}
	stop.add("no", func(*PollFacts) {})
	stop.add("yes", func(p *PollFacts) { p.StopPending = true })

	marker := markerAxis(view, markerID,
		axisValue[PollFacts]{"before-since", func(p *PollFacts) { p.View = issue{p.View}.marker(prevMarker, 1, 30).v }},
		axisValue[PollFacts]{"at-since", func(p *PollFacts) { p.View = issue{p.View}.marker(pollSince, 1, 30).v }},
	)

	comment := &axis[PollFacts]{name: "comment"}
	comment.add("none", func(*PollFacts) {})
	comment.add("at-last", func(p *PollFacts) { p.View = issue{p.View}.note(lastID, person, "a comment").v })
	comment.add("after-last", func(p *PollFacts) { p.View = issue{p.View}.note(commentID, person, "a comment").v })
	comment.add("by-bot", func(p *PollFacts) { p.View = issue{p.View}.note(commentID, bot, "a bot note").v })

	return []*axis[PollFacts]{issueState, trigger, stop, actor, marker, comment, elapsedAxis()}
}

// pollUnreadAxes are the inputs of Poll after a failed issue read. The actor axis shows that the read outcome and
// the turn timeout alone decide.
func pollUnreadAxes() []*axis[PollFacts] {
	read := &axis[PollFacts]{name: "issue-read"}

	for _, r := range IssueReads() {
		if r != ReadOK {
			read.add(r.String(), func(p *PollFacts) { p.Issue = r })
		}
	}

	return []*axis[PollFacts]{read, actorAxis(actorValues, func(p *PollFacts) *models.ActorView { return &p.Actor }),
		elapsedAxis()}
}

func elapsedAxis() *axis[PollFacts] {
	elapsed := &axis[PollFacts]{name: "elapsed"}
	elapsed.add("below-timeout", func(p *PollFacts) { p.Elapsed = limits.TurnTimeout - time.Second })
	elapsed.add("at-timeout", func(p *PollFacts) { p.Elapsed = limits.TurnTimeout })

	return elapsed
}

// goldenRow is a set of cells with one outcome: per axis, the bit set of the values it covers.
type goldenRow struct {
	sets    [maxAxes]uint16
	outcome string
}

// renderGolden evaluates every combination of axes and reduces the cells to rows of a decision table: rows that
// differ in one axis and share the outcome merge into one row, until no row merges. An axis that covers all its
// values is shown as *, several values as a|b. Every cell is in exactly one row. The header lists every axis
// value, so a new value changes the file even where it merges into *, and the number of cases per rule, so a diff
// starts with how many cases each rule gained or lost.
func renderGolden[T any](axes []*axis[T], zero func() T, outcome func(T) string) []byte {
	if len(axes) > maxAxes {
		panic(fmt.Sprintf("golden map: %d axes, at most %d", len(axes), maxAxes))
	}

	for _, a := range axes {
		if len(a.values) > maxValues {
			panic(fmt.Sprintf("golden map: axis %s has %d values, at most %d", a.name, len(a.values), maxValues))
		}
	}

	rows := evaluate(axes, zero, outcome)
	cases := ruleCases(rows)

	for merged := true; merged; {
		merged = false

		for j := len(axes) - 1; j >= 0; j-- {
			var n int

			rows, n = mergeAxis(rows, j)
			merged = merged || n > 0
		}
	}

	slices.SortFunc(rows, func(a, b goldenRow) int {
		for i := range axes {
			if a.sets[i] != b.sets[i] {
				return bits.TrailingZeros16(a.sets[i]) - bits.TrailingZeros16(b.sets[i])
			}
		}

		return 0
	})

	var buf bytes.Buffer

	header := make([]string, 0, len(axes))

	for _, a := range axes {
		names := make([]string, 0, len(a.values))
		for _, v := range a.values {
			names = append(names, v.name)
		}

		_, _ = fmt.Fprintf(&buf, "# %s: %s\n", a.name, strings.Join(names, " "))

		header = append(header, a.name)
	}

	for _, rc := range cases {
		_, _ = fmt.Fprintf(&buf, "# rule %s: %d cases\n", rc.rule, rc.n)
	}

	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "# %s\toutcome\n", strings.Join(header, "\t"))

	for _, r := range rows {
		cols := make([]string, len(axes))
		for i, a := range axes {
			cols[i] = setString(a.values, r.sets[i])
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\n", strings.Join(cols, "\t"), r.outcome)
	}

	_ = w.Flush()

	return buf.Bytes()
}

type ruleCount struct {
	rule string
	n    int
}

// ruleCases returns the number of cases per rule, the first word of an outcome, ordered by rule.
func ruleCases(rows []goldenRow) []ruleCount {
	n := map[string]int{}

	for _, r := range rows {
		rule, _, _ := strings.Cut(r.outcome, " ")
		n[rule]++
	}

	out := make([]ruleCount, 0, len(n))
	for rule, c := range n {
		out = append(out, ruleCount{rule: rule, n: c})
	}

	slices.SortFunc(out, func(a, b ruleCount) int { return strings.Compare(a.rule, b.rule) })

	return out
}

// evaluate returns one row per cell of the product of axes.
func evaluate[T any](axes []*axis[T], zero func() T, outcome func(T) string) []goldenRow {
	total := 1
	for _, a := range axes {
		total *= len(a.values)
	}

	rows := make([]goldenRow, total)

	for cell := range total {
		var r goldenRow

		f := zero()

		for i, rest := len(axes)-1, cell; i >= 0; i-- {
			n := len(axes[i].values)
			r.sets[i] = 1 << (rest % n)
			rest /= n
		}

		for i, a := range axes {
			a.values[bits.TrailingZeros16(r.sets[i])].set(&f)
		}

		r.outcome = outcome(f)
		rows[cell] = r
	}

	return rows
}

// mergeAxis merges the rows that are equal except in axis j. It returns the rows and the number of merges.
func mergeAxis(rows []goldenRow, j int) ([]goldenRow, int) {
	at := make(map[goldenRow]int, len(rows))
	out := make([]goldenRow, 0, len(rows))

	var merges int

	for _, r := range rows {
		k := r
		k.sets[j] = 0

		if i, ok := at[k]; ok {
			out[i].sets[j] |= r.sets[j]
			merges++

			continue
		}

		at[k] = len(out)
		out = append(out, r)
	}

	return out, merges
}

func setString[T any](values []axisValue[T], s uint16) string {
	if s == 1<<len(values)-1 {
		return "*"
	}

	var in []string

	for i, v := range values {
		if s&(1<<i) != 0 {
			in = append(in, v.name)
		}
	}

	return strings.Join(in, "|")
}

// assertGolden compares got with testdata/name; -update rewrites the file.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)

	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test ./internal/services/decide -run %s -update", t.Name())

	assert.Equal(t, string(want), string(got), "%s differs: review the diff, then run with -update to accept", path)
}

func TestDecideGolden(t *testing.T) {
	zero := func() Facts { return Facts{View: models.IssueView{Issue: models.Issue{Key: key}}} }

	got := renderGolden(decideAxes(), zero, func(f Facts) string {
		d := Decide(f, limits)
		assert.False(t, slices.ContainsFunc(d.Plan, func(e Effect) bool { return e.Kind == KindAdvance }),
			"an advance outside the activation loop: %s", d.Rule)

		return fmt.Sprintf("%s %s", d.Rule, d.Plan)
	})

	assertGolden(t, "decide.golden", got)
}

func TestDecideUnreadGolden(t *testing.T) {
	zero := func() Facts { return Facts{View: models.IssueView{Issue: models.Issue{Key: key}}} }

	got := renderGolden(unreadAxes(), zero, func(f Facts) string {
		d := Decide(f, limits)
		return fmt.Sprintf("%s %s", d.Rule, d.Plan)
	})

	assertGolden(t, "decide-unread.golden", got)
}

func TestPollGolden(t *testing.T) {
	zero := func() PollFacts {
		return PollFacts{View: models.IssueView{Issue: models.Issue{Key: key}}, Since: pollSince}
	}

	got := renderGolden(pollAxes(), zero, func(p PollFacts) string {
		return pollOutcome(Poll(p, limits))
	})

	assertGolden(t, "poll.golden", got)
}

func TestPollUnreadGolden(t *testing.T) {
	zero := func() PollFacts {
		return PollFacts{View: models.IssueView{Issue: models.Issue{Key: key}}, Since: pollSince}
	}

	got := renderGolden(pollUnreadAxes(), zero, func(p PollFacts) string { return pollOutcome(Poll(p, limits)) })

	assertGolden(t, "poll-unread.golden", got)
}

// TestTriggerChecksCoverDecide: where TriggerChecks names a check as not read, its fact does not change Decide.
// The trigger-after-last and author axes are left out of the walk and set inside each case.
func TestTriggerChecksCoverDecide(t *testing.T) {
	zero := func() Facts { return Facts{View: models.IssueView{Issue: models.Issue{Key: key}}} }
	axes := slices.DeleteFunc(decideAxes(), func(a *axis[Facts]) bool {
		return a.name == "trigger-after-last" || a.name == "author"
	})
	authors := []func(*Facts){
		func(f *Facts) { f.AuthorOK = true },
		func(*Facts) {},
		func(f *Facts) { f.AuthorUnknown = true },
	}

	evaluate(axes, zero, func(f Facts) string {
		comments, author := TriggerChecks(f, limits)

		var got [2][3]string

		for tal := range 2 {
			for a, set := range authors {
				g := f
				g.TriggerAfterLast = tal == 1
				set(&g)

				d := Decide(g, limits)
				got[tal][a] = string(d.Rule) + " " + d.Plan.String()
			}
		}

		for a := range authors {
			if !comments {
				assert.Equal(t, got[0][a], got[1][a], "TriggerAfterLast changes Decide where TriggerChecks skips it: %+v", f)
			}
		}

		for tal := range 2 {
			if !author {
				assert.Equal(t, got[tal][0], got[tal][1], "the author check changes Decide where TriggerChecks skips it: %+v", f)
				assert.Equal(t, got[tal][0], got[tal][2], "the author check changes Decide where TriggerChecks skips it: %+v", f)
			}
		}

		return ""
	})
}

func TestActorAxisCoversEveryActorState(t *testing.T) {
	covered := map[models.ActorState]bool{}
	for _, a := range actorValues {
		covered[a.view.State] = true
	}

	for _, s := range models.ActorStates() {
		assert.True(t, covered[s], "actor state %q is not classified: decide how %s enters the golden map", s, s)
	}
}
