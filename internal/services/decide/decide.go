// Package decide is the decision function of the dispatcher: facts of one issue and its actor in, one rule and its
// plan out. Pure: no I/O, no clock. A new rule is a new Rule constant, a new table-test case and a
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

// IssueRead is the outcome of the issue read. ReadGone: the issue does not exist and its project is readable.
// ReadUnavailable: any other failed read. Both decide on the outcome alone; View is unset.
type IssueRead int

// Issue read outcomes.
const (
	ReadOK IssueRead = iota
	ReadGone
	ReadUnavailable
)

var issueReads = []IssueRead{ReadOK, ReadGone, ReadUnavailable}

var issueReadNames = map[IssueRead]string{ReadOK: "ok", ReadGone: "gone", ReadUnavailable: "unavailable"}

func (r IssueRead) String() string { return issueReadNames[r] }

// IssueReads returns every issue read outcome.
func IssueReads() []IssueRead { return slices.Clone(issueReads) }

// Facts are what the issue lane read for one request.
// Issue: the outcome of the issue read; View holds the issue only for ReadOK.
// TriggerAfterLast: a trigger comment has a note id above the last marker's last=.
// AuthorOK: the request author has the trigger role or higher (start and clear-error requests).
// AuthorUnknown: the check of the request author failed (start and clear-error requests).
// Rechecks: how many times a transitional actor was re-read for this request.
type Facts struct {
	Issue            IssueRead
	View             models.IssueView
	Actor            models.ActorView
	Request          intake.Kind
	AuthorOK         bool
	AuthorUnknown    bool
	TriggerAfterLast bool
	Rechecks         int
}

// Rule names the branch that made a decision. It is logged with each decision (rule=).
type Rule string

// Rules of Decide. Enabled: the issue is open and has the trigger label. Record: the record of the status note;
// a clear-error request ignores a record in error; without a record, agent::done on an enabled issue stands for
// one, and so does agent::error unless the request is clear-error. Partial create: an actor template or actor
// without the egress policy. Complete actor: the egress policy exists. Label repair: the state label is set from
// the record when the issue does not have exactly that one state label.
const (
	// Issue gone. Discard.
	RuleIssueGone Rule = "issue-gone"
	// Issue unavailable. Keep the actor, suspended when it runs.
	RuleIssueUnavailable Rule = "issue-unavailable"
	// Actor unknown; the record, or the state label without a record, is working, waiting or error. Record error,
	// reason substrate error; the status note keeps its rows.
	RuleActorUnknownStarted Rule = "actor-unknown-started"
	// Actor unknown; any other issue. Nothing.
	RuleActorUnknown Rule = "actor-unknown"
	// Not enabled; an actor exists, or the record or the state label is not done. Stop, record done.
	RuleNotEnabledStop Rule = "not-enabled-stop"
	// Not enabled, no actor, no record. Nothing.
	RuleNotEnabledNoRecord Rule = "not-enabled-no-record"
	// Not enabled, no actor, record done. Label repair.
	RuleNotEnabledDone Rule = "not-enabled-done"

	// Record done, actor running, any request. Stop.
	RuleDoneRunningStop Rule = "done-running-stop"
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

// Decision is the result of Decide.
type Decision struct {
	Rule Rule
	Plan Plan
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
	if d, ok := unread(f); ok {
		return d
	}

	is := facts{f: f, l: l, rec: f.View.Record, hasRec: f.View.HasRecord}
	is.marker, is.hasMarker = models.LastMarker(f.View.Notes, l.Bot)

	// A clear-error request ignores a record in error: the issue continues as if it had no record and no
	// agent::error label.
	if f.Request == intake.ClearError && is.hasRec && is.rec.State == models.Error {
		is.hasRec = false
	}

	labels := f.View.Issue.Labels
	enabled := f.View.Enabled(l.TriggerLabel)

	if !enabled {
		return is.disabled()
	}

	// ADR 0004: without a record, agent::done or agent::error is the record. A clear-error request is the evidence
	// that agent::error was removed: the label is not a record for it, however it came back.
	if !is.hasRec {
		is.rec, is.hasRec = labelRecord(labels, f.Request != intake.ClearError)
	}

	if is.hasRec && is.rec.State == models.Done {
		if f.Actor.State == models.Running {
			return is.stop(RuleDoneRunningStop)
		}

		if f.Request != intake.Start {
			return is.repair(RuleDoneKept)
		}

		w := is.repairWrites()
		w.Notes = []models.NewNote{{Body: models.RefusedText + "\n" + models.RefusedMarker, Marker: models.RefusedMarker}}

		return decision(RuleDoneStartRefused, w)
	}

	if is.hasRec && is.rec.State == models.Error {
		// An actor in error keeps no worker: a running one is suspended, the error stays.
		if f.Actor.State == models.Running {
			return suspendOnly(RuleErrorSuspend, is.repairWrites())
		}

		return is.repair(RuleErrorKept)
	}

	// The turn limit is terminal: a clear-error request does not clear it.
	if f.Request == intake.ClearError && !is.hasRec && is.atTurnLimit() {
		w := record(models.Error, models.ReasonTurnLimit)
		if f.Actor.State == models.Running {
			return suspendOnly(RuleClearErrorTurnLimit, w)
		}

		return decision(RuleClearErrorTurnLimit, w)
	}

	switch {
	case !f.Actor.Exists():
		return is.noActor()
	case f.Actor.Partial():
		return is.partial()
	}

	return is.complete()
}

// unread decides a failed read: the issue read first, then the actor read.
func unread(f Facts) (Decision, bool) {
	switch {
	case f.Issue == ReadGone:
		return Decision{Rule: RuleIssueGone, Plan: Plan{effect(KindDelete, FailAbort)}}, true
	case f.Issue == ReadUnavailable:
		return Decision{Rule: RuleIssueUnavailable, Plan: Plan{effect(KindSuspendIfRunning, FailAbort)}}, true
	case f.Actor.State == models.Unknown:
		return actorUnknown(f.View), true
	}

	return Decision{}, false
}

// actorUnknown: the actor read failed. A started agent, by its record or else its state label, records substrate
// error; a stop request then runs on the next request for the issue.
func actorUnknown(v models.IssueView) Decision {
	state, known := v.Record.State, v.HasRecord
	if !known {
		state, known = models.StateFromLabels(v.Issue.Labels)
	}

	if !known || state == models.Done {
		return Decision{Rule: RuleActorUnknown}
	}

	return decision(RuleActorUnknownStarted, record(models.Error, models.ReasonSubstrateError))
}

// disabled: issue closed or without the trigger label.
func (is facts) disabled() Decision {
	state, hasLabel := models.StateFromLabels(is.f.View.Issue.Labels)
	if is.f.Actor.Exists() || (is.hasRec && is.rec.State != models.Done) || (hasLabel && state != models.Done) {
		return is.stop(RuleNotEnabledStop)
	}

	if !is.hasRec {
		return Decision{Rule: RuleNotEnabledNoRecord}
	}

	return is.repair(RuleNotEnabledDone)
}

// create returns the create plan under rule for an author who can trigger, the start-refused note for one who
// cannot, and neither when that is unknown. Without a create the state label is set from the record of the view.
func (is facts) create(rule Rule) Decision {
	if is.f.AuthorOK && !is.f.AuthorUnknown {
		return is.createPlan(rule)
	}

	var w models.Writes

	refusal := RuleAuthorUnknown
	if !is.f.AuthorUnknown {
		refusal, w.Notes = RuleStartRefused, []models.NewNote{{
			Body:   models.StartRefusedText(is.l.TriggerRole) + "\n" + models.StartRefusedMarker,
			Marker: models.StartRefusedMarker,
		}}
	}

	if is.f.View.HasRecord && !is.labelIs(is.f.View.Record.State) {
		w.Label = models.StateLabel(is.f.View.Record.State)
	}

	return decision(refusal, w)
}

// atTurnLimit reports whether the last marker is at the turn limit.
func (is facts) atTurnLimit() bool {
	return is.hasMarker && is.marker.Turn >= is.l.TurnLimit
}

// noActor: no actor template, no actor, no egress policy.
func (is facts) noActor() Decision {
	if is.hasRec {
		return decision(RuleActorMissing, record(models.Error, models.ReasonActorMissing))
	}

	switch is.f.Request {
	case intake.Start:
		return is.create(RuleStartCreate)
	case intake.ClearError:
		return is.create(RuleClearErrorCreate)
	}

	return Decision{Rule: RuleNoActorIgnored}
}

// partial: a create that stopped before the egress policy.
func (is facts) partial() Decision {
	if is.f.Request == intake.Start {
		return is.create(RuleStartPartialCreate)
	}

	if is.hasRec {
		return decision(RulePartialCreateActorMissing, record(models.Error, models.ReasonActorMissing))
	}

	switch is.f.Request {
	case intake.ClearError:
		return is.create(RuleClearErrorCreate)
	case intake.Recover:
		// A partial create without a record is a create cut after its author check.
		return is.createPlan(RuleRecoverPartialCreate)
	}

	return Decision{Rule: RulePartialCreateIgnored}
}

// complete: the egress policy exists.
func (is facts) complete() Decision {
	f := is.f

	switch st := f.Actor.State; {
	case st.Transitional():
		if f.Rechecks < MaxRechecks {
			return Decision{Rule: RuleTransitionalRecheck, Plan: Plan{{Kind: KindRecheck}}}
		}

		return decision(RuleActorBusy, record(models.Error, models.ReasonActorBusy))
	case st == models.Running:
		var w models.Writes
		if !is.hasRec || is.rec.State != models.Working || !is.labelIs(models.Working) {
			w = record(models.Working, "")
		}

		return Decision{
			Rule: RuleRunningContinue,
			Plan: append(write(w), loop(is.previousMarkerID(), true)),
		}
	case st == models.Crashed:
		if is.hasRec && is.rec.State == models.Working {
			return decision(RuleCrashedWorking, record(models.Error, models.ReasonActorCrashed))
		}
	}

	var pre models.Writes

	var settle Rule

	switch {
	case !is.hasRec:
		settle, pre = RuleNoRecordWaiting, record(models.Waiting, "")

		if !is.hasMarker {
			return is.activate(RuleFirstActivation, pre)
		}
	case is.rec.State == models.Working:
		settle, pre = RuleWorkingRecordWaiting, record(models.Waiting, "")
	}

	if f.TriggerAfterLast {
		if is.atTurnLimit() {
			return decision(RuleTurnLimit, record(models.Error, models.ReasonTurnLimit))
		}

		return is.activate(RuleTriggerActivate, pre)
	}

	if settle != "" {
		return decision(settle, pre)
	}

	if f.Actor.State == models.Crashed {
		return Decision{Rule: RuleCrashedNoTrigger}
	}

	return is.repair(RuleWaitingKept)
}

// stop notes the agent's merge request, deletes the actor when it exists, running or not, then records done. The
// note comes before the delete: a stop cut later runs again from restart recovery, and the marker keeps it to one
// note.
func (is facts) stop(rule Rule) Decision {
	p := Plan{effect(KindNoteMR, FailLog)}
	if is.f.Actor.Exists() {
		p = append(p, effect(KindDelete, FailError))
	}

	return Decision{Rule: rule, Plan: append(p, write(record(models.Done, ""))...)}
}

// createPlan deletes a partial create, creates the actor, records waiting, then runs the first activation.
func (is facts) createPlan(rule Rule) Decision {
	var p Plan
	if is.f.Actor.Exists() {
		p = Plan{effect(KindDelete, FailError)}
	}

	p = append(p, effect(KindCreate, FailError), effect(KindInspect, FailError))
	p = append(p, write(record(models.Waiting, ""))...)

	return Decision{Rule: rule, Plan: append(p, is.activation()...)}
}

// activate applies pre, reverts a crashed actor, then runs an activation.
func (is facts) activate(rule Rule, pre models.Writes) Decision {
	p := write(pre)
	if is.f.Actor.State == models.Crashed {
		p = append(p, effect(KindRevert, FailErrorFresh))
	}

	return Decision{Rule: rule, Plan: append(p, is.activation()...)}
}

// activation resumes the actor, records working with fresh status rows, then runs the activation loop from the
// last marker read before the resume.
func (is facts) activation() Plan {
	var since int64
	if is.hasMarker {
		since = is.marker.NoteID
	}

	p := Plan{effect(KindResume, FailErrorFresh), effect(KindReload, FailLog)}
	p = append(p, write(record(models.Working, ""))...)

	return append(p, loop(since, false))
}

func decision(rule Rule, w models.Writes) Decision { return Decision{Rule: rule, Plan: write(w)} }

// suspendOnly applies w, then suspends the actor; no activation.
func suspendOnly(rule Rule, w models.Writes) Decision {
	return Decision{Rule: rule, Plan: append(write(w), effect(KindSuspend, FailAbort))}
}

// previousMarkerID returns the note id of the marker before the last one (0 if none): the loop of a running actor
// found after a dispatcher restart then sees the last marker as new.
func (is facts) previousMarkerID() int64 {
	if !is.hasMarker {
		return 0
	}

	var prev int64

	for _, n := range is.f.View.Notes {
		if n.ID < is.marker.NoteID {
			if m, ok := models.MarkerAfter([]models.Note{n}, is.l.Bot, 0); ok {
				prev = max(prev, m.NoteID)
			}
		}
	}

	return prev
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
func (is facts) repair(rule Rule) Decision { return decision(rule, is.repairWrites()) }

// repairWrites sets the state label from the record when the labels differ from it.
func (is facts) repairWrites() models.Writes {
	var w models.Writes
	if is.hasRec && !is.labelIs(is.rec.State) {
		w.Label = models.StateLabel(is.rec.State)
	}

	return w
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
// suspend, then the writes. End: the writes, no suspend. StopNow: run the stop action, then the writes.
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
	settled := p.Actor.State.Settled()

	// A stop request counts only while the issue is still closed or without the trigger label; a stop that a
	// later reopen or label made stale is handled by Decide on the lane.
	enabled := p.View.Enabled(l.TriggerLabel)

	if p.StopPending && !enabled && (hasNew || timedOut || settled) {
		return PollDecision{Rule: PollStopPending, Action: StopNow, Marker: m, Writes: record(models.Done, "")}
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
