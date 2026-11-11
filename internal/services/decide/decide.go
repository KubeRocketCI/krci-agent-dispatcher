// Package decide is the decision function of the dispatcher: facts of one issue and its actor in, one action and
// its GitLab writes out. Pure: no I/O, no clock. A new rule is a new Rule constant, a new table-test case and a
// reviewed diff of the golden maps in testdata.
package decide

import (
	"slices"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

// MaxRechecks is the number of re-checks of a transitional actor before the actor busy error.
const MaxRechecks = 3

// Limits are the fixed rules of one dispatcher. TriggerRole names the role in the start-refused note.
type Limits struct {
	TurnLimit    int
	Bot          string
	TriggerLabel string
	TriggerRole  models.Role
	TurnTimeout  time.Duration
}

// Facts are what the issue lane read for one request.
// TriggerAfterLast: a trigger comment has a note id above the last marker's last=.
// AuthorOK: the request author has the trigger role or higher (start and clear-error requests).
// AuthorUnknown: the check of the request author failed (start and clear-error requests).
// Rechecks: how many times a transitional actor was re-read for this request.
type Facts struct {
	View             models.IssueView
	Actor            models.ActorView
	Request          intake.Kind
	AuthorOK         bool
	AuthorUnknown    bool
	TriggerAfterLast bool
	Rechecks         int
}

// Action is what the issue lane does.
type Action int

// Actions. Create: delete a partial create, create the actor, record waiting, then an activation from note 0.
// Activate: an activation (resume first). Revert: revert, then an activation. Continue: the activation loop
// of a running actor, after the resume step. Stop: delete the actor, running or not, then the writes.
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

// Rule names the branch that made a decision. It is logged with each decision (rule=).
type Rule string

// Rules of Decide. Enabled: the issue is open and has the trigger label. Record: the record of the status note;
// a clear-error request ignores a record in error; without a record, agent::done on an enabled issue stands for
// one, and so does agent::error unless the request is clear-error. Partial create: an actor template or actor
// without the egress policy. Complete actor: the egress policy exists. Label repair: the state label is set from
// the record when the issue does not have exactly that one state label.
const (
	// Not enabled; an actor exists, or the record or the state label is not done. Stop, record done.
	RuleNotEnabledStop Rule = "not-enabled-stop"
	// Not enabled, no actor, no record. Nothing.
	RuleNotEnabledNoRecord Rule = "not-enabled-no-record"
	// Not enabled, no actor, record done. Label repair.
	RuleNotEnabledDone Rule = "not-enabled-done"

	// Record done, start request. The refused note, label repair.
	RuleDoneStartRefused Rule = "done-start-refused"
	// Record done, any other request. Label repair.
	RuleDoneKept Rule = "done-kept"
	// Record error, actor running. Suspend, label repair.
	RuleErrorSuspend Rule = "error-suspend"
	// Record error, actor not running. Label repair.
	RuleErrorKept Rule = "error-kept"

	// Clear-error request, no record, last marker at the turn limit. Record error, reason turn limit; a running
	// actor is suspended.
	RuleClearErrorTurnLimit Rule = "clear-error-turn-limit"

	// No actor, record working or waiting. Record error, reason actor missing.
	RuleActorMissing Rule = "actor-missing"
	// No actor, start request, author can trigger. Create.
	RuleStartCreate Rule = "start-create"
	// No actor, no record, request other than start and clear-error. Nothing.
	RuleNoActorIgnored Rule = "no-actor-ignored"

	// Partial create, start request, author can trigger. Create.
	RuleStartPartialCreate Rule = "start-partial-create"
	// Partial create, record working or waiting, request other than start. Record error, reason actor missing.
	RulePartialCreateActorMissing Rule = "partial-create-actor-missing"
	// Partial create, no record, recover request. Create.
	RuleRecoverPartialCreate Rule = "recover-partial-create"
	// Partial create, no record, request other than start, clear-error and recover. Nothing.
	RulePartialCreateIgnored Rule = "partial-create-ignored"

	// No complete actor, start or clear-error request, author check failed. No create, no note; the state
	// label is set from the status note record.
	RuleAuthorUnknown Rule = "author-unknown"
	// No complete actor, start or clear-error request, author cannot trigger. The start-refused note; the state
	// label is set from the status note record.
	RuleStartRefused Rule = "start-refused"
	// No complete actor, clear-error request, author can trigger. Create.
	RuleClearErrorCreate Rule = "clear-error-create"

	// Complete actor, transitional, re-checks below MaxRechecks. Recheck.
	RuleTransitionalRecheck Rule = "transitional-recheck"
	// Complete actor, transitional, re-checks at MaxRechecks. Record error, reason actor busy.
	RuleActorBusy Rule = "actor-busy"
	// Complete actor, running. Continue; record working unless the record and the state label are working.
	RuleRunningContinue Rule = "running-continue"
	// Complete actor, crashed, record working. Record error, reason actor crashed.
	RuleCrashedWorking Rule = "crashed-working"
	// Complete actor, no record, no marker. Record waiting, then activate (revert first when crashed).
	RuleFirstActivation Rule = "first-activation"
	// Complete actor, trigger comment, last marker at the turn limit. Record error, reason turn limit.
	RuleTurnLimit Rule = "turn-limit"
	// Complete actor, trigger comment. Activate (revert first when crashed); record waiting first when there is
	// no record or the record is working.
	RuleTriggerActivate Rule = "trigger-activate"
	// Complete actor, no record, marker, no trigger comment. Record waiting.
	RuleNoRecordWaiting Rule = "no-record-waiting"
	// Complete actor, not running, record working, no trigger comment. Record waiting.
	RuleWorkingRecordWaiting Rule = "working-record-waiting"
	// Complete actor, crashed, record waiting, no trigger comment. Nothing.
	RuleCrashedNoTrigger Rule = "crashed-no-trigger"
	// Complete actor, record waiting, no trigger comment. Label repair.
	RuleWaitingKept Rule = "waiting-kept"
)

// Rules of Poll, in evaluation order. New marker: the bot user's turn marker with the highest note id above
// PollFacts.Since. Timed out: PollFacts.Elapsed is at or over the turn timeout.
const (
	// Stop pending, not enabled, and one of: new marker, timed out, actor not running and not transitional.
	// StopNow.
	PollStopPending Rule = "stop-pending"
	// New marker at the turn limit. Suspend, record error, reason turn limit.
	PollMarkerLimit Rule = "marker-limit"
	// New marker, a comment with a note id above its last=. StayResumed.
	PollMarkerComment Rule = "marker-comment"
	// New marker, no such comment. Suspend, record waiting.
	PollMarkerIdle Rule = "marker-idle"
	// No actor. End, record error, reason actor missing.
	PollActorGone Rule = "actor-gone"
	// Actor crashed. End, record error, reason actor crashed.
	PollCrashed Rule = "crashed"
	// Actor suspended. End, record waiting.
	PollSuspendedByHand Rule = "suspended-by-hand"
	// Timed out. Record error, reason turn timeout; Suspend when the actor is running, End otherwise.
	PollTimeout Rule = "timeout"
	// Any other case. Wait.
	PollWait Rule = "wait"
)

// Decision is the result of Decide. Writes are applied before the action, except for Stop (after it).
type Decision struct {
	Rule   Rule
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

// Decide returns the decision of the first matching rule.
func Decide(f Facts, l Limits) Decision {
	is := facts{f: f, l: l, rec: f.View.Record, hasRec: f.View.HasRecord}
	is.marker, is.hasMarker = models.LastMarker(f.View.Notes, l.Bot)

	// A clear-error request ignores a record in error: the issue continues as if it had no record and no
	// agent::error label.
	if f.Request == intake.ClearError && is.hasRec && is.rec.State == models.Error {
		is.hasRec = false
	}

	labels := f.View.Issue.Labels
	enabled := f.View.Issue.IssueState == models.IssueOpened && models.HasLabel(labels, l.TriggerLabel)

	if !enabled {
		return is.disabled()
	}

	// ADR 0004: without a record, agent::done or agent::error is the record. A clear-error request is the evidence
	// that agent::error was removed: the label is not a record for it, however it came back.
	if !is.hasRec {
		is.rec, is.hasRec = labelRecord(labels, f.Request != intake.ClearError)
	}

	if is.hasRec && is.rec.State == models.Done {
		if f.Request != intake.Start {
			return is.repair(RuleDoneKept)
		}

		d := is.repair(RuleDoneStartRefused)
		d.Writes.Notes = []models.NewNote{{
			Body: models.RefusedText + "\n" + models.RefusedMarker, Marker: models.RefusedMarker,
		}}

		return d
	}

	if is.hasRec && is.rec.State == models.Error {
		// An actor in error keeps no worker: a running one is suspended, the error stays.
		if f.Actor.State == models.Running {
			d := is.repair(RuleErrorSuspend)
			d.Action = SuspendOnly

			return d
		}

		return is.repair(RuleErrorKept)
	}

	// The turn limit is terminal: a clear-error request does not clear it.
	if f.Request == intake.ClearError && !is.hasRec && is.atTurnLimit() {
		d := Decision{Rule: RuleClearErrorTurnLimit, Action: None, Writes: record(models.Error, models.ReasonTurnLimit)}
		if f.Actor.State == models.Running {
			d.Action = SuspendOnly
		}

		return d
	}

	switch {
	case !f.Actor.Exists():
		return is.noActor()
	case f.Actor.Partial():
		return is.partial()
	}

	return is.complete()
}

// disabled: issue closed or without the trigger label.
func (is facts) disabled() Decision {
	state, hasLabel := models.StateFromLabels(is.f.View.Issue.Labels)
	if is.f.Actor.Exists() || (is.hasRec && is.rec.State != models.Done) || (hasLabel && state != models.Done) {
		return Decision{Rule: RuleNotEnabledStop, Action: Stop, Writes: record(models.Done, "")}
	}

	if !is.hasRec {
		return Decision{Rule: RuleNotEnabledNoRecord, Action: None}
	}

	return is.repair(RuleNotEnabledDone)
}

// create returns Create under rule for an author who can trigger, the start-refused note for one who cannot, and
// neither when that is unknown. Without a create the state label is set from the record of the view.
func (is facts) create(rule Rule) Decision {
	var d Decision

	switch {
	case is.f.AuthorUnknown:
		d = Decision{Rule: RuleAuthorUnknown, Action: None}
	case is.f.AuthorOK:
		return Decision{Rule: rule, Action: Create}
	default:
		d = Decision{Rule: RuleStartRefused, Action: None, Writes: models.Writes{Notes: []models.NewNote{{
			Body:   models.StartRefusedText(is.l.TriggerRole) + "\n" + models.StartRefusedMarker,
			Marker: models.StartRefusedMarker,
		}}}}
	}

	if is.f.View.HasRecord && !is.labelIs(is.f.View.Record.State) {
		d.Writes.Label = models.StateLabel(is.f.View.Record.State)
	}

	return d
}

// atTurnLimit reports whether the last marker is at the turn limit.
func (is facts) atTurnLimit() bool {
	return is.hasMarker && is.marker.Turn >= is.l.TurnLimit
}

// noActor: no actor template, no actor, no egress policy.
func (is facts) noActor() Decision {
	if is.hasRec {
		return Decision{Rule: RuleActorMissing, Action: None, Writes: record(models.Error, models.ReasonActorMissing)}
	}

	switch is.f.Request {
	case intake.Start:
		return is.create(RuleStartCreate)
	case intake.ClearError:
		return is.create(RuleClearErrorCreate)
	}

	return Decision{Rule: RuleNoActorIgnored, Action: None}
}

// partial: a create that stopped before the egress policy.
func (is facts) partial() Decision {
	if is.f.Request == intake.Start {
		return is.create(RuleStartPartialCreate)
	}

	if is.hasRec {
		return Decision{
			Rule:   RulePartialCreateActorMissing,
			Action: None,
			Writes: record(models.Error, models.ReasonActorMissing),
		}
	}

	switch is.f.Request {
	case intake.ClearError:
		return is.create(RuleClearErrorCreate)
	case intake.Recover:
		// A partial create without a record is a create cut after its author check.
		return Decision{Rule: RuleRecoverPartialCreate, Action: Create}
	}

	return Decision{Rule: RulePartialCreateIgnored, Action: None}
}

// complete: the egress policy exists.
func (is facts) complete() Decision {
	f := is.f

	switch f.Actor.State {
	case models.Transitional:
		if f.Rechecks < MaxRechecks {
			return Decision{Rule: RuleTransitionalRecheck, Action: Recheck}
		}

		return Decision{Rule: RuleActorBusy, Action: None, Writes: record(models.Error, models.ReasonActorBusy)}
	case models.Running:
		if is.hasRec && is.rec.State == models.Working && is.labelIs(models.Working) {
			return Decision{Rule: RuleRunningContinue, Action: Continue}
		}

		return Decision{Rule: RuleRunningContinue, Action: Continue, Writes: record(models.Working, "")}
	case models.Crashed:
		if is.hasRec && is.rec.State == models.Working {
			return Decision{
				Rule:   RuleCrashedWorking,
				Action: None,
				Writes: record(models.Error, models.ReasonActorCrashed),
			}
		}
	}

	var pre models.Writes

	var settle Rule

	switch {
	case !is.hasRec:
		settle, pre = RuleNoRecordWaiting, record(models.Waiting, "")

		if !is.hasMarker {
			return Decision{Rule: RuleFirstActivation, Action: is.activation(), Writes: pre}
		}
	case is.rec.State == models.Working:
		settle, pre = RuleWorkingRecordWaiting, record(models.Waiting, "")
	}

	if f.TriggerAfterLast {
		if is.atTurnLimit() {
			return Decision{Rule: RuleTurnLimit, Action: None, Writes: record(models.Error, models.ReasonTurnLimit)}
		}

		return Decision{Rule: RuleTriggerActivate, Action: is.activation(), Writes: pre}
	}

	if settle != "" {
		return Decision{Rule: settle, Action: None, Writes: pre}
	}

	if f.Actor.State == models.Crashed {
		return Decision{Rule: RuleCrashedNoTrigger, Action: None}
	}

	return is.repair(RuleWaitingKept)
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

// repair returns rule with the state label set from the record when the labels differ from it.
func (is facts) repair(rule Rule) Decision {
	d := Decision{Rule: rule, Action: None}
	if is.hasRec && !is.labelIs(is.rec.State) {
		d.Writes.Label = models.StateLabel(is.rec.State)
	}

	return d
}

// labelRecord returns the record that the state labels stand for: done before error; working and waiting follow
// from the actor state and are not a record. Without withError, agent::error stands for nothing.
func labelRecord(labels []string, withError bool) (models.Record, bool) {
	states := []models.AgentState{models.Done}
	if withError {
		states = append(states, models.Error)
	}

	for _, s := range states {
		if models.HasLabel(labels, models.StateLabel(s)) {
			return models.Record{State: s}, true
		}
	}

	return models.Record{}, false
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

// Poll actions. StayResumed: a new turn starts (reset the turn timeout, Since = Marker.NoteID). Suspend:
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
	Rule   Rule
	Action PollAction
	Writes models.Writes
	Marker models.Marker
}

// Poll returns the activation-loop decision for one poll: the first matching rule.
func Poll(p PollFacts, l Limits) PollDecision {
	m, hasNew := models.MarkerAfter(p.View.Notes, l.Bot, p.Since)
	timedOut := p.Elapsed >= l.TurnTimeout
	settled := p.Actor.State != models.Running && p.Actor.State != models.Transitional

	// A stop request counts only while the issue is still closed or without the trigger label; a stop that a
	// later reopen or label made stale is handled by Decide on the lane.
	enabled := p.View.Issue.IssueState == models.IssueOpened && models.HasLabel(p.View.Issue.Labels, l.TriggerLabel)

	if p.StopPending && !enabled && (hasNew || timedOut || settled) {
		return PollDecision{Rule: PollStopPending, Action: StopNow, Marker: m}
	}

	switch {
	case hasNew && m.Turn >= l.TurnLimit:
		return PollDecision{
			Rule:   PollMarkerLimit,
			Action: Suspend,
			Marker: m,
			Writes: record(models.Error, models.ReasonTurnLimit),
		}
	case hasNew && models.CommentAfter(p.View.Notes, l.Bot, m.Last):
		return PollDecision{Rule: PollMarkerComment, Action: StayResumed, Marker: m}
	case hasNew:
		return PollDecision{Rule: PollMarkerIdle, Action: Suspend, Marker: m, Writes: record(models.Waiting, "")}
	case !p.Actor.Actor:
		return PollDecision{Rule: PollActorGone, Action: End, Writes: record(models.Error, models.ReasonActorMissing)}
	case p.Actor.State == models.Crashed:
		return PollDecision{Rule: PollCrashed, Action: End, Writes: record(models.Error, models.ReasonActorCrashed)}
	case p.Actor.State == models.Suspended:
		return PollDecision{Rule: PollSuspendedByHand, Action: End, Writes: record(models.Waiting, "")}
	case timedOut && p.Actor.State == models.Running:
		return PollDecision{Rule: PollTimeout, Action: Suspend, Writes: record(models.Error, models.ReasonTurnTimeout)}
	case timedOut:
		return PollDecision{Rule: PollTimeout, Action: End, Writes: record(models.Error, models.ReasonTurnTimeout)}
	}

	return PollDecision{Rule: PollWait, Action: Wait}
}
