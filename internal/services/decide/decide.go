// Package decide is the decision function of the dispatcher: facts of one issue and its actor in, one rule and its
// plan out. Pure: no I/O, no clock. A new rule is a new Rule constant, a new table-test case and a
// reviewed diff of the golden maps in testdata.
package decide

import (
	"slices"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

// MaxRechecks is the number of re-checks of a transitional actor before the actor busy error.
const MaxRechecks = 3

// Limits are the fixed rules of one dispatcher. TriggerRole names the role in the start-refused note.
type Limits struct {
	TurnLimit   int
	Bot         string
	Labels      models.AgentLabels
	TriggerRole models.Role
	TurnTimeout time.Duration
}

// IssueRead is the outcome of the issue read. ReadGone: the issue does not exist and its project is readable; the
// outcome alone decides and Actor is unset. ReadUnavailable: any other failed read; the outcome and Actor decide.
// View is unset after either.
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
// Issue: the outcome of the issue read; View holds the issue only for ReadOK; Actor is read for every outcome but
// ReadGone.
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

// Rules of Decide. Enabled: the issue is open and has the trigger label. Turn history: the markers of the session of
// the actor, else of the status note (turn.Session, turn.NewHistory); a clear-error request that ignores the record
// keeps that session. Record: the record of the status note; a clear-error request ignores a record in error; without
// a record, krci::agent::done on an enabled issue stands for one, and so does krci::agent::error unless the request is
// clear-error. Re-enable cut: the status note records done and the actor's template names another session than the
// status note; on an enabled issue it is no record, and the state labels stand for none. Partial create: an actor
// template or actor without the egress policy. Complete actor: the egress policy exists. Label repair: the state label
// is set from the record when the issue does not have exactly that one state label.
const (
	// Issue gone. Discard.
	RuleIssueGone Rule = "issue-gone"
	// Issue unavailable. Keep the actor, suspended when it runs or cannot be read.
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
	// Record done, actor not running and not transitional, start request, author can trigger. Create with a new
	// session; an actor left is deleted first. A transitional actor is re-checked first (RuleTransitionalRecheck,
	// RuleActorBusy).
	RuleDoneStartCreate Rule = "done-start-create"
	// Record done, any other case. Label repair.
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

	// No complete actor, start or clear-error request, or record done and start request; author check failed. No
	// create, no note; the state label is set from the status note record.
	RuleAuthorUnknown Rule = "author-unknown"
	// No complete actor, start or clear-error request, or record done and start request; author cannot trigger.
	// The start-refused note; the state label is set from the status note record.
	RuleStartRefused Rule = "start-refused"
	// No complete actor, clear-error request, author can trigger. Create.
	RuleClearErrorCreate Rule = "clear-error-create"

	// Complete actor, or record done and start request with an author who can trigger; transitional, re-checks below
	// MaxRechecks. Recheck.
	RuleTransitionalRecheck Rule = "transitional-recheck"
	// As RuleTransitionalRecheck, re-checks at MaxRechecks. Record error, reason actor busy.
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

// Rules of Poll, in evaluation order. New marker: the bot user's turn marker of the session of the actor, else of the
// status note (turn.Session), with the highest note id above PollFacts.Since. Timed out: PollFacts.Elapsed is at or
// over the turn timeout. Read failed: the issue is unavailable or the actor unknown. Issue gone comes first and is
// RuleIssueGone: discard; the activation ends.
const (
	// Read failed, timed out. Suspend, record error, reason turn timeout, with status rows from a fresh read; the
	// activation ends. Read failed, not timed out: wait.
	PollReadTimeout Rule = "read-timeout"
	// Stop pending, not enabled, and one of: new marker, timed out, actor not running and not transitional.
	// Stop, record done; the activation ends.
	PollStopPending Rule = "stop-pending"
	// New marker at the turn limit. Suspend, record error, reason turn limit.
	PollMarkerLimit Rule = "marker-limit"
	// New marker, a comment with a note id above its last=. Advance to the new marker; the actor stays resumed.
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
	// Any other case. Nothing; poll again.
	PollWait Rule = "wait"
)

// Decision is the result of Decide.
type Decision struct {
	Rule Rule
	Plan Plan
}

type facts struct {
	f      Facts
	l      Limits
	rec    models.Record
	hasRec bool
	turns  turn.History
}

// TriggerChecks reports which trigger checks Decide reads for f, the facts without them: whether a comment no turn
// covered is a trigger comment, and whether the request author can trigger the agent. Decide reads neither after a
// failed issue read, for an unknown actor or on a disabled issue; the author only for start and clear-error.
func TriggerChecks(f Facts, l Limits) (comments, author bool) {
	if _, ok := unread(f, l.Labels); ok || !f.View.Enabled(l.Labels) {
		return false, false
	}

	return true, f.Request == intake.Start || f.Request == intake.ClearError
}

// Decide returns the decision of the first matching rule.
func Decide(f Facts, l Limits) Decision {
	if d, ok := unread(f, l.Labels); ok {
		return d
	}

	is := facts{f: f, l: l, rec: f.View.Record, hasRec: f.View.HasRecord}
	is.turns = turn.NewHistory(f.View.Notes, l.Bot, turn.Session(f.Actor, f.View.Status))

	// A clear-error request ignores a record in error: the issue continues as if it had no record and no
	// krci::agent::error label.
	if f.Request == intake.ClearError && is.hasRec && is.rec.State == models.Error {
		is.hasRec = false
	}

	labels := f.View.Issue.Labels
	enabled := f.View.Enabled(l.Labels)

	if !enabled {
		return is.disabled()
	}

	// ADR 0004: without a record, krci::agent::done or krci::agent::error is the record. A clear-error request is the
	// evidence that krci::agent::error was removed: the label is not a record for it, however it came back.
	switch {
	case reEnableCut(f):
		is.rec, is.hasRec = models.Record{}, false
	case !is.hasRec:
		is.rec, is.hasRec = labelRecord(labels, l.Labels, f.Request != intake.ClearError)
	}

	if is.hasRec && is.rec.State == models.Done {
		return is.done()
	}

	if is.hasRec && is.rec.State == models.Error {
		// An actor in error keeps no worker: a running one is suspended, the error stays.
		if f.Actor.State == models.Running {
			return suspendOnly(RuleErrorSuspend, is.repairWrites())
		}

		return is.repair(RuleErrorKept)
	}

	// The turn limit is terminal: a clear-error request does not clear it.
	if f.Request == intake.ClearError && !is.hasRec && is.turns.AtLimit(is.l.TurnLimit) {
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
func unread(f Facts, labels models.AgentLabels) (Decision, bool) {
	switch {
	case f.Issue == ReadGone:
		return Decision{Rule: RuleIssueGone, Plan: Plan{effect(KindDelete, FailAbort)}}, true
	case f.Issue == ReadUnavailable:
		return keep(f.Actor), true
	case f.Actor.State == models.Unknown:
		return actorUnknown(f.View, labels), true
	}

	return Decision{}, false
}

// keep: the issue is unavailable. A running actor, or one that cannot be read, is suspended; any other actor is
// left as it is.
func keep(a models.ActorView) Decision {
	if a.State != models.Running && a.State != models.Unknown {
		return Decision{Rule: RuleIssueUnavailable}
	}

	return Decision{Rule: RuleIssueUnavailable, Plan: Plan{effect(KindSuspend, FailLog)}}
}

// actorUnknown: the actor read failed. A started agent, by its record or else its state label, records substrate
// error; a stop request then runs on the next request for the issue.
func actorUnknown(v models.IssueView, labels models.AgentLabels) Decision {
	state, known := v.Record.State, v.HasRecord
	if !known {
		state, known = labels.StateFrom(v.Issue.Labels)
	}

	if !known || state == models.Done {
		return Decision{Rule: RuleActorUnknown}
	}

	return decision(RuleActorUnknownStarted, record(models.Error, models.ReasonSubstrateError))
}

// disabled: issue closed or without the trigger label.
func (is facts) disabled() Decision {
	state, hasLabel := is.l.Labels.StateFrom(is.f.View.Issue.Labels)
	if is.f.Actor.Exists() || (is.hasRec && is.rec.State != models.Done) || (hasLabel && state != models.Done) {
		return Decision{Rule: RuleNotEnabledStop, Plan: stopPlan(is.f.Actor)}
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
		w.Label = is.f.View.Record.State
	}

	return decision(refusal, w)
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
		return is.transitional()
	case st == models.Running:
		return is.continueRunning(RuleRunningContinue)
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

		if _, ok := is.turns.Last(); !ok {
			return is.activate(RuleFirstActivation, pre)
		}
	case is.rec.State == models.Working:
		settle, pre = RuleWorkingRecordWaiting, record(models.Waiting, "")
	}

	if f.TriggerAfterLast {
		if is.turns.AtLimit(is.l.TurnLimit) {
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

// continueRunning records working unless the record and the state label are working, then runs the activation loop
// on the running actor.
func (is facts) continueRunning(rule Rule) Decision {
	var w models.Writes
	if !is.hasRec || is.rec.State != models.Working || !is.labelIs(models.Working) {
		w = record(models.Working, "")
	}

	// The loop starts at the previous marker, so it sees the last marker as new.
	return Decision{Rule: rule, Plan: append(write(w), loop(is.turns.PreviousID(), true))}
}

// done: record done. A start re-enables the agent with a new session.
func (is facts) done() Decision {
	if is.f.Actor.State == models.Running {
		return Decision{Rule: RuleDoneRunningStop, Plan: stopPlan(is.f.Actor)}
	}

	if is.f.Request == intake.Start {
		return is.doneStart()
	}

	return is.repair(RuleDoneKept)
}

// reEnableCut reports a re-enable cut: a done record in the status note and an actor template of another session.
// A label-only done, the actor's own session or an actor without a session is no cut.
func reEnableCut(f Facts) bool {
	return f.View.HasRecord && f.View.Record.State == models.Done && f.Actor.Session != "" &&
		f.Actor.Session != f.View.Status.Session
}

func (is facts) doneStart() Decision {
	if is.f.Actor.State.Transitional() && is.f.AuthorOK && !is.f.AuthorUnknown {
		return is.transitional()
	}

	return is.create(RuleDoneStartCreate)
}

// transitional re-checks a transitional actor up to MaxRechecks, then records actor busy.
func (is facts) transitional() Decision {
	if is.f.Rechecks < MaxRechecks {
		return Decision{Rule: RuleTransitionalRecheck, Plan: Plan{recheck()}}
	}

	return decision(RuleActorBusy, record(models.Error, models.ReasonActorBusy))
}

// stopPlan notes the agent's merge request, deletes the actor when it exists, running or not, then records done. The
// note comes before the delete: a stop cut later runs again from restart recovery, and the marker keeps it to one
// note.
func stopPlan(actor models.ActorView) Plan {
	p := Plan{effect(KindNoteMR, FailLog)}
	if actor.Exists() {
		p = append(p, effect(KindDelete, FailError))
	}

	return append(p, write(record(models.Done, ""))...)
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
	if m, ok := is.turns.Last(); ok {
		since = m.NoteID
	}

	p := Plan{effect(KindResume, FailNoRoom), effect(KindReload, FailLog)}
	p = append(p, write(record(models.Working, ""))...)

	return append(p, loop(since, false))
}

func decision(rule Rule, w models.Writes) Decision { return Decision{Rule: rule, Plan: write(w)} }

// suspendOnly applies w, then suspends the actor; no activation.
func suspendOnly(rule Rule, w models.Writes) Decision {
	return Decision{Rule: rule, Plan: append(write(w), effect(KindSuspend, FailAbort))}
}

// labelIs reports whether the only state label is the one of s.
func (is facts) labelIs(s models.AgentState) bool {
	var got []string

	for _, l := range is.f.View.Issue.Labels {
		if is.l.Labels.IsState(l) {
			got = append(got, l)
		}
	}

	return len(got) == 1 && got[0] == is.l.Labels.State(s)
}

// repair returns rule with the state label set from the record when the labels differ from it.
func (is facts) repair(rule Rule) Decision { return decision(rule, is.repairWrites()) }

// repairWrites sets the state label from the record when the labels differ from it.
func (is facts) repairWrites() models.Writes {
	var w models.Writes
	if is.hasRec && !is.labelIs(is.rec.State) {
		w.Label = is.rec.State
	}

	return w
}

// labelRecord returns the record that the state labels stand for: done before error; working and waiting follow
// from the actor state and are not a record. Without withError, krci::agent::error stands for nothing.
func labelRecord(labels []string, names models.AgentLabels, withError bool) (models.Record, bool) {
	states := []models.AgentState{models.Done}
	if withError {
		states = append(states, models.Error)
	}

	for _, s := range states {
		if models.HasLabel(labels, names.State(s)) {
			return models.Record{State: s}, true
		}
	}

	return models.Record{}, false
}

// record returns writes that set the record and its state label.
func record(s models.AgentState, reason string) models.Writes {
	return models.Writes{Record: &models.Record{State: s, Reason: reason}, Label: s}
}

// PollFacts are what one poll of the activation loop read.
// Issue: the outcome of the issue read; View holds the issue only for ReadOK.
// Since is the note id of the turn's previous marker (0 before turn 1). Elapsed counts from the turn start.
// StopPending: a stop request waits on the lane.
type PollFacts struct {
	Issue       IssueRead
	View        models.IssueView
	Actor       models.ActorView
	Since       int64
	Elapsed     time.Duration
	StopPending bool
}

// Next is whether the activation loop polls again after a poll decision.
type Next int

// Next values.
const (
	NextContinue Next = iota
	NextEnd
)

var nexts = []Next{NextContinue, NextEnd}

var nextNames = map[Next]string{NextContinue: "continue", NextEnd: "end"}

func (n Next) String() string { return nextNames[n] }

// Nexts returns every Next value.
func Nexts() []Next { return slices.Clone(nexts) }

// PollDecision is the result of Poll. Marker is the new turn marker, if any.
type PollDecision struct {
	Rule   Rule
	Plan   Plan
	Next   Next
	Marker turn.Marker
}

// Waits reports whether the decision changes nothing: the loop polls again with no effect.
func (d PollDecision) Waits() bool { return d.Next == NextContinue && len(d.Plan) == 0 }

// Poll returns the activation-loop decision for one poll: the first matching rule.
func Poll(p PollFacts, l Limits) PollDecision {
	timedOut := p.Elapsed >= l.TurnTimeout

	if d, ok := pollUnread(p, l.Labels, timedOut); ok {
		return d
	}

	h := turn.NewHistory(p.View.Notes, l.Bot, turn.Session(p.Actor, p.View.Status))
	m, hasNew := h.MarkerAfter(p.Since)
	settled := p.Actor.State.Settled()

	// A stop request counts only while the issue is still closed or without the trigger label; a stop that a
	// later reopen or label made stale is handled by Decide on the lane.
	enabled := p.View.Enabled(l.Labels)

	if p.StopPending && !enabled && (hasNew || timedOut || settled) {
		return PollDecision{Rule: PollStopPending, Plan: stopPlan(p.Actor), Next: NextEnd, Marker: m}
	}

	switch {
	case hasNew && h.AtLimit(l.TurnLimit):
		return suspendThenEnd(PollMarkerLimit, m, record(models.Error, models.ReasonTurnLimit))
	case hasNew && h.Next().Ready:
		return PollDecision{Rule: PollMarkerComment, Plan: Plan{advance(m.NoteID)}, Marker: m}
	case hasNew:
		return suspendThenEnd(PollMarkerIdle, m, record(models.Waiting, ""))
	case !p.Actor.Actor:
		return endWith(PollActorGone, record(models.Error, models.ReasonActorMissing))
	case p.Actor.State == models.Crashed:
		return endWith(PollCrashed, record(models.Error, models.ReasonActorCrashed))
	case p.Actor.State == models.Suspended:
		return endWith(PollSuspendedByHand, record(models.Waiting, ""))
	case timedOut && p.Actor.State == models.Running:
		return suspendThenEnd(PollTimeout, turn.Marker{}, record(models.Error, models.ReasonTurnTimeout))
	case timedOut:
		return endWith(PollTimeout, record(models.Error, models.ReasonTurnTimeout))
	}

	return PollDecision{Rule: PollWait}
}

// pollUnread decides a poll whose issue or actor read failed: a gone issue is discarded; any other failed read
// waits, and at the turn timeout suspends and records turn timeout with status rows from a fresh read.
func pollUnread(p PollFacts, labels models.AgentLabels, timedOut bool) (PollDecision, bool) {
	switch {
	case p.Issue == ReadGone:
		d, _ := unread(Facts{Issue: ReadGone}, labels)
		return PollDecision{Rule: d.Rule, Plan: d.Plan, Next: NextEnd}, true
	case p.Issue != ReadUnavailable && p.Actor.State != models.Unknown:
		return PollDecision{}, false
	case !timedOut:
		return PollDecision{Rule: PollWait}, true
	}

	plan := append(Plan{effect(KindSuspend, FailLog), effect(KindReload, FailLog)},
		write(record(models.Error, models.ReasonTurnTimeout))...)

	return PollDecision{Rule: PollReadTimeout, Plan: plan, Next: NextEnd}, true
}

// suspendThenEnd suspends the actor, reads it for the status rows, applies w, then ends the activation.
func suspendThenEnd(rule Rule, m turn.Marker, w models.Writes) PollDecision {
	p := append(Plan{effect(KindSuspend, FailError), effect(KindInspect, FailLog)}, write(w)...)
	return PollDecision{Rule: rule, Plan: p, Next: NextEnd, Marker: m}
}

// endWith applies w, then ends the activation.
func endWith(rule Rule, w models.Writes) PollDecision {
	return PollDecision{Rule: rule, Plan: write(w), Next: NextEnd}
}
