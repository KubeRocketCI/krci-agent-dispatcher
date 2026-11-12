package decide

import (
	"slices"
	"strconv"
	"strings"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// EffectKind is what one effect does, by its rendered name.
//
// write: apply Writes with status rows from the current view and actor; an unknown view or actor keeps the rows.
// note-mr: note the agent's merge request once. reload: read the issue, then the actor; a failed read leaves the
// value unknown. inspect: read the actor. delete: delete the actor; the current actor is then none. create:
// create the actor with a new session; the current view then holds the session. revert, resume, suspend: the
// Substrate call; ErrNoRoom on resume notes no room with fresh status rows and ends the plan. suspend-if-running:
// read the actor and suspend it when it runs or cannot be read. loop: the activation loop from Since. advance: a
// new turn of the activation loop starts at Since; the turn timeout restarts. recheck: read the actor again after a
// pause, then decide again; the only effect of its plan.
type EffectKind int

// Effect kinds.
const (
	KindWrite EffectKind = iota
	KindNoteMR
	KindReload
	KindInspect
	KindDelete
	KindCreate
	KindRevert
	KindResume
	KindSuspend
	KindSuspendIfRunning
	KindLoop
	KindAdvance
	KindRecheck
)

var kinds = []EffectKind{
	KindWrite, KindNoteMR, KindReload, KindInspect, KindDelete, KindCreate,
	KindRevert, KindResume, KindSuspend, KindSuspendIfRunning, KindLoop, KindAdvance, KindRecheck,
}

var kindNames = map[EffectKind]string{
	KindWrite: "write", KindNoteMR: "note-mr", KindReload: "reload", KindInspect: "inspect", KindDelete: "delete",
	KindCreate: "create", KindRevert: "revert", KindResume: "resume", KindSuspend: "suspend",
	KindSuspendIfRunning: "suspend-if-running", KindLoop: "loop", KindAdvance: "advance",
	KindRecheck: "recheck",
}

func (k EffectKind) String() string { return kindNames[k] }

// EffectKinds returns every effect kind.
func EffectKinds() []EffectKind { return slices.Clone(kinds) }

// Fail is what the dispatcher does when an effect fails. Effects that call nothing out (loop, advance, recheck) do
// not fail.
//
// FailLog: log, then the next effect. FailAbort: log, end the plan. FailError: record substrate error with the
// current view and actor, end the plan. FailErrorFresh: FailError with status rows from a reload. A failure cut by
// the end of ctx ends the plan and records nothing, whatever its Fail: the actor is left for the next restart
// recovery.
type Fail int

// Failure handling.
const (
	FailLog Fail = iota
	FailAbort
	FailError
	FailErrorFresh
)

var fails = []Fail{FailLog, FailAbort, FailError, FailErrorFresh}

var failNames = map[Fail]string{FailLog: "log", FailAbort: "abort", FailError: "error", FailErrorFresh: "error-fresh"}

func (f Fail) String() string { return failNames[f] }

// Fails returns every failure handling.
func Fails() []Fail { return slices.Clone(fails) }

// Effect is one read, write or Substrate call of a plan. Writes is set for a write; Since for a loop and an advance;
// Recovered for a loop: the loop picks up a running actor without a resume.
type Effect struct {
	Kind      EffectKind
	Fail      Fail
	Writes    models.Writes
	Since     int64
	Recovered bool
}

func (e Effect) String() string {
	switch e.Kind {
	case KindRecheck:
		return e.Kind.String()
	case KindLoop:
		s := "loop(since=" + strconv.FormatInt(e.Since, 10)
		if e.Recovered {
			s += ",recovered"
		}

		return s + ")"
	case KindAdvance:
		return "advance(since=" + strconv.FormatInt(e.Since, 10) + ")"
	case KindWrite:
		return "write(" + writesString(e.Writes) + ")!" + e.Fail.String()
	}

	return e.Kind.String() + "!" + e.Fail.String()
}

// Plan is the ordered effects of one decision. An empty plan does nothing.
type Plan []Effect

func (p Plan) String() string {
	if len(p) == 0 {
		return "-"
	}

	parts := make([]string, len(p))
	for i, e := range p {
		parts[i] = e.String()
	}

	return strings.Join(parts, " > ")
}

// IsRecheck reports whether the plan is a recheck.
func (p Plan) IsRecheck() bool { return len(p) == 1 && p[0].Kind == KindRecheck }

func effect(k EffectKind, f Fail) Effect { return Effect{Kind: k, Fail: f} }

// write returns the plan's write of w, nothing when w is empty.
func write(w models.Writes) Plan {
	if w.Empty() {
		return nil
	}

	return Plan{{Kind: KindWrite, Fail: FailLog, Writes: w}}
}

func advance(since int64) Effect { return Effect{Kind: KindAdvance, Since: since} }

func recheck() Effect { return Effect{Kind: KindRecheck} }

func loop(since int64, recovered bool) Effect {
	return Effect{Kind: KindLoop, Since: since, Recovered: recovered}
}

// writesString renders writes as record=<state>[/<reason>],label=<state>,note=<marker>, or - for none.
func writesString(w models.Writes) string {
	var parts []string

	if w.Record != nil {
		r := "record=" + string(w.Record.State)
		if w.Record.Reason != "" {
			r += "/" + strings.ReplaceAll(w.Record.Reason, " ", "-")
		}

		parts = append(parts, r)
	}

	if w.Label != "" {
		parts = append(parts, "label="+strings.TrimPrefix(w.Label, "agent::"))
	}

	for _, n := range w.Notes {
		parts = append(parts, "note="+strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(n.Marker, "<!--"), "-->")))
	}

	if len(parts) == 0 {
		return "-"
	}

	return strings.Join(parts, ",")
}
