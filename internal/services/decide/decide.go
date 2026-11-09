// Package decide is the decision function of the dispatcher (spec §5.12): facts of one issue and its actor in,
// one action and its GitLab writes out. Pure: no I/O, no clock. A new rule is a new row.
package decide

import (
	"slices"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// MaxRechecks is the number of re-checks of a transitional actor before the actor busy error.
const MaxRechecks = 3

// Limits are the fixed rules of one dispatcher.
type Limits struct {
	MaxTurns     int
	Bot          string
	TriggerLabel string
	TurnTimeout  time.Duration
}

// Facts are what the issue lane read for one request.
// TriggerAfterLast: a trigger comment (author ≥ Developer) has a note id above the last marker's last=.
// AuthorOK: the start author has access level ≥ Developer (start requests only).
// Rechecks: how many times a transitional actor was re-read for this request.
type Facts struct {
	View             models.IssueView
	Actor            models.ActorView
	LaneActive       bool
	Request          intake.Kind
	AuthorOK         bool
	TriggerAfterLast bool
	Rechecks         int
}

// Action is what the issue lane does.
type Action int

// Actions. Create: delete a partial create, create the actor, record waiting, then an activation from note 0.
// Activate: an activation (resume first). Revert: revert, then an activation. Continue: the activation loop
// of a running actor, after the resume step. Stop: suspend if running, delete the actor, then the writes.
// Recheck: read the actor again after a pause, then decide again. SuspendOnly: suspend, no activation.
const (
	None Action = iota
	Create
	Activate
	Revert
	Stop
	Continue
	Recheck
	SuspendOnly
)

var actionNames = map[Action]string{
	None: "none", Create: "create", Activate: "activate", Revert: "revert", Stop: "stop", Continue: "continue",
	Recheck: "recheck", SuspendOnly: "suspend",
}

func (a Action) String() string { return actionNames[a] }

// Decision is the result of Decide. Writes are applied before the action, except for Stop (after it).
type Decision struct {
	Row    string
	Action Action
	Writes models.Writes
}

type facts struct {
	f         Facts
	l         Limits
	rec       models.Record
	hasRec    bool
	marker    models.Marker
	hasMarker bool
}

// Decide returns the action of the first matching row of the decision table.
func Decide(f Facts, l Limits) Decision {
	is := facts{f: f, l: l, rec: f.View.Record, hasRec: f.View.HasRecord}
	is.marker, is.hasMarker = models.LastMarker(f.View.Notes, l.Bot)

	// R2: removing agent::error clears the error on purpose; the issue continues as if it had no record.
	if f.Request == intake.ClearError && is.hasRec && is.rec.State == models.Error {
		is.hasRec = false
	}

	labels := f.View.Issue.Labels
	enabled := f.View.Issue.IssueState == models.IssueOpened && models.HasLabel(labels, l.TriggerLabel)

	if !enabled {
		return is.disabled()
	}

	if is.hasRec && is.rec.State == models.Done {
		d := is.repair("D3")
		if f.Request == intake.Start {
			d.Writes.Notes = []models.NewNote{{
				Body: models.RefusedText + "\n" + models.RefusedMarker, Marker: models.RefusedMarker,
			}}
		}

		return d
	}

	if is.hasRec && is.rec.State == models.Error {
		d := is.repair("D4")
		// An actor in error keeps no worker: a running one is suspended, the error stays.
		if f.Actor.State == models.Running && !f.LaneActive {
			d.Action = SuspendOnly
		}

		return d
	}

	if f.LaneActive && f.Actor.State == models.Running {
		return Decision{Row: "D11", Action: None}
	}

	switch {
	case !f.Actor.Exists():
		return is.noActor()
	case f.Actor.Partial():
		return is.partial()
	}

	return is.complete()
}

// disabled: issue closed or trigger label removed (D1, D2).
func (is facts) disabled() Decision {
	state, hasLabel := models.StateFromLabels(is.f.View.Issue.Labels)
	if is.f.Actor.Exists() || (is.hasRec && is.rec.State != models.Done) || (hasLabel && state != models.Done) {
		return Decision{Row: "D1", Action: Stop, Writes: record(models.Done, "")}
	}

	if !is.hasRec {
		return Decision{Row: "D2", Action: None}
	}

	return is.repair("D18")
}

func (is facts) startRefused() Decision {
	return Decision{Row: "D6", Action: None, Writes: models.Writes{Notes: []models.NewNote{{
		Body: models.StartRefusedText + "\n" + models.StartRefusedMarker, Marker: models.StartRefusedMarker,
	}}}}
}

// noActor: D5, D6, D17.
func (is facts) noActor() Decision {
	if is.hasRec {
		return Decision{Row: "D17", Action: None, Writes: record(models.Error, models.ReasonActorMissing)}
	}

	if is.f.Request != intake.Start {
		return Decision{Row: "D2", Action: None}
	}

	if !is.f.AuthorOK {
		return is.startRefused()
	}

	return Decision{Row: "D5", Action: Create}
}

// partial: a create that stopped before the egress policy (D6, D7, D17).
func (is facts) partial() Decision {
	if is.f.Request == intake.Start {
		if !is.f.AuthorOK {
			return is.startRefused()
		}

		return Decision{Row: "D7", Action: Create}
	}

	if is.hasRec {
		return Decision{Row: "D17", Action: None, Writes: record(models.Error, models.ReasonActorMissing)}
	}

	return Decision{Row: "D7", Action: None}
}

// complete: actor and egress policy exist (D8–D16, D18).
func (is facts) complete() Decision {
	f := is.f

	switch f.Actor.State {
	case models.Transitional:
		if f.Rechecks < MaxRechecks {
			return Decision{Row: "D16", Action: Recheck}
		}

		return Decision{Row: "D16", Action: None, Writes: record(models.Error, models.ReasonActorBusy)}
	case models.Running:
		if is.hasRec && is.rec.State == models.Working && is.labelIs(models.Working) {
			return Decision{Row: "D12", Action: Continue}
		}

		return Decision{Row: "D12", Action: Continue, Writes: record(models.Working, "")}
	case models.Crashed:
		if is.hasRec && is.rec.State == models.Working {
			return Decision{Row: "D15", Action: None, Writes: record(models.Error, models.ReasonActorCrashed)}
		}
	}

	var pre models.Writes

	row := ""

	switch {
	case !is.hasRec:
		row, pre = "D8", record(models.Waiting, "")

		if !is.hasMarker {
			return Decision{Row: "D8", Action: is.activation(), Writes: pre}
		}
	case is.rec.State == models.Working:
		row, pre = "D13", record(models.Waiting, "")
	}

	if f.TriggerAfterLast {
		if is.hasMarker && is.marker.Turn >= is.l.MaxTurns {
			return Decision{Row: "D10", Action: None, Writes: record(models.Error, models.ReasonTurnLimit)}
		}

		return Decision{Row: "D9", Action: is.activation(), Writes: pre}
	}

	if row != "" {
		return Decision{Row: row, Action: None, Writes: pre}
	}

	if f.Actor.State == models.Crashed {
		return Decision{Row: "D14", Action: None}
	}

	return is.repair("D18")
}

func (is facts) activation() Action {
	if is.f.Actor.State == models.Crashed {
		return Revert
	}

	return Activate
}

// labelIs reports whether the only state label is the one of s.
func (is facts) labelIs(s models.AgentState) bool {
	var got []string

	for _, l := range is.f.View.Issue.Labels {
		if slices.Contains(models.StateLabels(), l) {
			got = append(got, l)
		}
	}

	return len(got) == 1 && got[0] == models.StateLabel(s)
}

// repair returns row with the state label set from the record when the labels differ from it (D18).
func (is facts) repair(row string) Decision {
	d := Decision{Row: row, Action: None}
	if is.hasRec && !is.labelIs(is.rec.State) {
		d.Writes.Label = models.StateLabel(is.rec.State)
	}

	return d
}

// record returns writes that set the record and its state label.
func record(s models.AgentState, reason string) models.Writes {
	return models.Writes{Record: &models.Record{State: s, Reason: reason}, Label: models.StateLabel(s)}
}

// PollFacts are what one poll of the activation loop read.
// Since is the note id of the turn's previous marker (0 before turn 1). Elapsed counts from the turn start.
// StopPending: a stop request waits on the lane.
type PollFacts struct {
	View        models.IssueView
	Actor       models.ActorView
	Since       int64
	Elapsed     time.Duration
	StopPending bool
}

// PollAction is what the activation loop does after one poll.
type PollAction int

// Poll actions. StayResumed: a new turn starts (reset the turn deadline, Since = Marker.NoteID). Suspend:
// suspend, then the writes. End: the writes, no suspend. StopNow: run the stop action.
const (
	Wait PollAction = iota
	StayResumed
	Suspend
	End
	StopNow
)

var pollActionNames = map[PollAction]string{
	Wait: "wait", StayResumed: "stay-resumed", Suspend: "suspend", End: "end", StopNow: "stop-now",
}

func (a PollAction) String() string { return pollActionNames[a] }

// PollDecision is the result of Poll. Marker is the new turn marker, if any.
type PollDecision struct {
	Row    string
	Action PollAction
	Writes models.Writes
	Marker models.Marker
}

// Poll returns the activation-loop action for one poll (spec §5.12, activation loop).
func Poll(p PollFacts, l Limits) PollDecision {
	m, hasNew := models.MarkerAfter(p.View.Notes, l.Bot, p.Since)
	timedOut := p.Elapsed >= l.TurnTimeout
	settled := p.Actor.State != models.Running && p.Actor.State != models.Transitional

	// A stop request counts only while the issue is still closed or without the trigger label; a stop that a
	// later reopen or label made stale is handled by Decide on the lane.
	enabled := p.View.Issue.IssueState == models.IssueOpened && models.HasLabel(p.View.Issue.Labels, l.TriggerLabel)

	if p.StopPending && !enabled && (hasNew || timedOut || settled) {
		return PollDecision{Row: "stop-pending", Action: StopNow, Marker: m}
	}

	switch {
	case hasNew && m.Turn >= l.MaxTurns:
		return PollDecision{
			Row:    "marker-limit",
			Action: Suspend,
			Marker: m,
			Writes: record(models.Error, models.ReasonTurnLimit),
		}
	case hasNew && models.CommentAfter(p.View.Notes, l.Bot, m.Last):
		return PollDecision{Row: "marker-comment", Action: StayResumed, Marker: m}
	case hasNew:
		return PollDecision{Row: "marker-idle", Action: Suspend, Marker: m, Writes: record(models.Waiting, "")}
	case !p.Actor.Actor:
		return PollDecision{Row: "actor-gone", Action: End, Writes: record(models.Error, models.ReasonActorMissing)}
	case p.Actor.State == models.Crashed:
		return PollDecision{Row: "crashed", Action: End, Writes: record(models.Error, models.ReasonActorCrashed)}
	case p.Actor.State == models.Suspended:
		return PollDecision{Row: "suspended-by-hand", Action: End, Writes: record(models.Waiting, "")}
	case timedOut && p.Actor.State == models.Running:
		return PollDecision{Row: "timeout", Action: Suspend, Writes: record(models.Error, models.ReasonTurnTimeout)}
	case timedOut:
		return PollDecision{Row: "timeout", Action: End, Writes: record(models.Error, models.ReasonTurnTimeout)}
	}

	return PollDecision{Row: "wait", Action: Wait}
}
