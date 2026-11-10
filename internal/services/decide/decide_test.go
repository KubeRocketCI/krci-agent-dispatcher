package decide

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

const bot = "krci-agent"

var limits = Limits{TurnLimit: 3, Bot: bot, TriggerLabel: "agent", TurnTimeout: 15 * time.Minute}

var key = models.IssueKey{ProjectID: 12, IID: 7}

type issue struct{ v models.IssueView }

func open(labels ...string) issue {
	return issue{models.IssueView{Issue: models.Issue{Key: key, IssueState: models.IssueOpened, Labels: labels}}}
}

func closed(labels ...string) issue {
	return issue{models.IssueView{Issue: models.Issue{Key: key, IssueState: models.IssueClosed, Labels: labels}}}
}

func (i issue) record(s models.AgentState, reason string) issue {
	i.v.Record, i.v.HasRecord = models.Record{State: s, Reason: reason}, true
	return i
}

func (i issue) marker(id int64, n int, last int64) issue {
	i.v.Notes = append(i.v.Notes, models.Note{ID: id, Author: bot,
		Body: "reply\n<!-- krci-agent-turn n=" + strconv.Itoa(n) + " exit=0 last=" + strconv.FormatInt(last, 10) + " -->"})

	return i
}

func (i issue) note(id int64, author, body string) issue {
	i.v.Notes = append(i.v.Notes, models.Note{ID: id, Author: author, Body: body})
	return i
}

var (
	noActor   = models.ActorView{State: models.None}
	partial   = models.ActorView{Template: true, Actor: true, State: models.Suspended}
	suspended = models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Suspended}
	running   = models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Running}
	crashed   = models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Crashed}
	moving    = models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Transitional}
)

func rec(s models.AgentState, reason string) *models.Record {
	return &models.Record{State: s, Reason: reason}
}

func lbl(s models.AgentState) string { return models.StateLabel(s) }

var (
	refusedNote      = models.NewNote{Body: models.RefusedText + "\n" + models.RefusedMarker, Marker: models.RefusedMarker}
	startRefusedNote = models.NewNote{
		Body:   models.StartRefusedText + "\n" + models.StartRefusedMarker,
		Marker: models.StartRefusedMarker,
	}
)

type decideCase struct {
	name  string
	facts Facts
	want  Decision
}

func decideCases() []decideCase {
	return []decideCase{
		{"closed, actor: stop", Facts{
			View:    closed("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   suspended,
			Request: intake.Stop,
		},
			Decision{
				Rule:   RuleNotEnabledStop,
				Action: Stop,
				Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)},
			}},
		{"trigger label removed, record error: stop", Facts{
			View:    open(lbl(models.Error)).record(models.Error, models.ReasonTurnLimit).v,
			Actor:   suspended,
			Request: intake.Stop,
		},
			Decision{
				Rule:   RuleNotEnabledStop,
				Action: Stop,
				Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)},
			}},
		{"closed, no actor, record working: stop", Facts{
			View:    closed("agent").record(models.Working, "").v,
			Actor:   noActor,
			Request: intake.Recover,
		},
			Decision{
				Rule:   RuleNotEnabledStop,
				Action: Stop,
				Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)},
			}},
		{"closed, running actor: stop", Facts{
			View:    closed("agent").record(models.Working, "").v,
			Actor:   running,
			Request: intake.Stop,
		},
			Decision{
				Rule:   RuleNotEnabledStop,
				Action: Stop,
				Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)},
			}},
		{"closed, no trigger label, no record: nothing", Facts{
			View:    closed("bug").v,
			Actor:   noActor,
			Request: intake.Stop,
		},
			Decision{Rule: RuleNotEnabledNoRecord, Action: None}},
		{"reopened without the trigger label: nothing", Facts{
			View:    open("bug").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{Rule: RuleNotEnabledNoRecord, Action: None}},
		{"start, record done: refused note, label repaired", Facts{
			View:    open("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Start,
		},
			Decision{
				Rule:   RuleDoneStartRefused,
				Action: None,
				Writes: models.Writes{Label: lbl(models.Done), Notes: []models.NewNote{refusedNote}},
			}},
		{"comment, record done: no note", Facts{
			View:    open("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{Rule: RuleDoneKept, Action: None}},
		{"record error, trigger comment: no activation, label repaired", Facts{
			View:             open("agent").record(models.Error, models.ReasonTurnTimeout).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Rule: RuleErrorKept, Action: None, Writes: models.Writes{Label: lbl(models.Error)}}},
		{"record error, start: nothing", Facts{
			View:     open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnTimeout).v,
			Actor:    suspended,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{Rule: RuleErrorKept, Action: None}},
		{"start by a developer, no actor: create", Facts{
			View:     open("agent").v,
			Actor:    noActor,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{Rule: RuleStartCreate, Action: Create}},
		{"start by a reporter, no actor: refused", Facts{
			View:     open("agent").v,
			Actor:    noActor,
			Request:  intake.Start,
			AuthorOK: false,
		},
			Decision{Rule: RuleStartRefused, Action: None, Writes: models.Writes{Notes: []models.NewNote{startRefusedNote}}}},
		{"start by a reporter, partial create: refused", Facts{
			View:     open("agent").v,
			Actor:    partial,
			Request:  intake.Start,
			AuthorOK: false,
		},
			Decision{Rule: RuleStartRefused, Action: None, Writes: models.Writes{Notes: []models.NewNote{startRefusedNote}}}},
		{"start by a developer, partial create: create", Facts{
			View:     open("agent").v,
			Actor:    partial,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{Rule: RuleStartPartialCreate, Action: Create}},
		{"comment, partial create, no record: nothing", Facts{
			View:    open("agent").v,
			Actor:   partial,
			Request: intake.Activate,
		},
			Decision{Rule: RulePartialCreateIgnored, Action: None}},
		{"complete actor, no record, no marker: activation", Facts{
			View:     open("agent").v,
			Actor:    suspended,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{
				Rule:   RuleFirstActivation,
				Action: Activate,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"complete actor, no record, marker: waiting", Facts{
			View:    open("agent").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Recover,
		},
			Decision{
				Rule:   RuleNoRecordWaiting,
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"complete actor, no record, marker, trigger comment: waiting, activation", Facts{
			View:             open("agent").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{
				Rule:   RuleTriggerActivate,
				Action: Activate,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"suspended, trigger comment: activation", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Rule: RuleTriggerActivate, Action: Activate}},
		{"crashed, trigger comment: revert first", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:            crashed,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Rule: RuleTriggerActivate, Action: Revert}},
		{"suspended, record waiting, no trigger comment: nothing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Rule: RuleWaitingKept, Action: None}},
		{"trigger comment at the turn limit: error", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(130, 3, 125).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{
				Rule:   RuleTurnLimit,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"failed turns count to the turn limit", Facts{View: open("agent", lbl(models.Waiting)).record(models.Waiting, "").
			note(130, bot, "failed\n<!-- krci-agent-turn n=3 exit=1 last=125 -->").v,
			Actor: suspended, Request: intake.Activate, TriggerAfterLast: true},
			Decision{
				Rule:   RuleTurnLimit,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"trigger comment below the turn limit: activation", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(130, 2, 125).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Rule: RuleTriggerActivate, Action: Activate}},
		{"running, record working: continue", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").v,
			Actor:   running,
			Request: intake.Recover,
		},
			Decision{Rule: RuleRunningContinue, Action: Continue}},
		{"running, record waiting: record working, continue", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   running,
			Request: intake.Recover,
		},
			Decision{
				Rule:   RuleRunningContinue,
				Action: Continue,
				Writes: models.Writes{Record: rec(models.Working, ""), Label: lbl(models.Working)},
			}},
		{"suspended, record working, no trigger comment: waiting", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Recover,
		},
			Decision{
				Rule:   RuleWorkingRecordWaiting,
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"suspended, record working, trigger comment: waiting, activation", Facts{
			View:             open("agent", lbl(models.Working)).record(models.Working, "").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{
				Rule:   RuleTriggerActivate,
				Action: Activate,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"crashed, record waiting, no trigger comment: nothing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   crashed,
			Request: intake.Recover,
		},
			Decision{Rule: RuleCrashedNoTrigger, Action: None}},
		{"crashed, record working: actor crashed", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").v,
			Actor:   crashed,
			Request: intake.Recover,
		},
			Decision{
				Rule:   RuleCrashedWorking,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorCrashed), Label: lbl(models.Error)},
			}},
		{"transitional: re-check", Facts{
			View:     open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:    moving,
			Request:  intake.Activate,
			Rechecks: 2,
		},
			Decision{Rule: RuleTransitionalRecheck, Action: Recheck}},
		{"transitional after 3 re-checks: actor busy", Facts{
			View:     open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:    moving,
			Request:  intake.Activate,
			Rechecks: 3,
		},
			Decision{
				Rule:   RuleActorBusy,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorBusy), Label: lbl(models.Error)},
			}},
		{"no actor, record waiting: actor missing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{
				Rule:   RuleActorMissing,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorMissing), Label: lbl(models.Error)},
			}},
		{"no actor, record working: actor missing", Facts{
			View:    open("agent").record(models.Working, "").v,
			Actor:   noActor,
			Request: intake.Recover,
		},
			Decision{
				Rule:   RuleActorMissing,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorMissing), Label: lbl(models.Error)},
			}},
		{"hand-set agent::done, record waiting: label repaired", Facts{
			View:    open("agent", lbl(models.Done)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Rule: RuleWaitingKept, Action: None, Writes: models.Writes{Label: lbl(models.Waiting)}}},
		{"two state labels: label repaired", Facts{
			View:    open("agent", lbl(models.Waiting), lbl(models.Working)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Rule: RuleWaitingKept, Action: None, Writes: models.Writes{Label: lbl(models.Waiting)}}},
		{"closed, record done, no state label: label repaired", Facts{
			View:    closed("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Stop,
		},
			Decision{Rule: RuleNotEnabledDone, Action: None, Writes: models.Writes{Label: lbl(models.Done)}}},
		{"clear-error, record error, no trigger comment: waiting", Facts{
			View:    open("agent").record(models.Error, models.ReasonTurnTimeout).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleNoRecordWaiting,
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"clear-error, record error, crashed, trigger comment: waiting, revert", Facts{
			View:             open("agent").record(models.Error, models.ReasonActorCrashed).marker(110, 1, 100).v,
			Actor:            crashed,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			Decision{
				Rule:   RuleTriggerActivate,
				Action: Revert,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"clear-error, complete actor, turn limit, trigger comment: error kept", Facts{
			View:             open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:            suspended,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			Decision{
				Rule:   RuleClearErrorTurnLimit,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"clear-error, complete actor, turn limit, no trigger comment: error kept", Facts{
			View:    open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleClearErrorTurnLimit,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"clear-error, running actor, turn limit: suspend, error kept", Facts{
			View:    open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:   running,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleClearErrorTurnLimit,
				Action: SuspendOnly,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"agent::done removed, record done: label repaired", Facts{
			View:    open("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{Rule: RuleDoneKept, Action: None, Writes: models.Writes{Label: lbl(models.Done)}}},
		{"hand-set agent::working, record waiting: label repaired", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Rule: RuleWaitingKept, Action: None, Writes: models.Writes{Label: lbl(models.Waiting)}}},
		{"record error, running actor: suspend, error kept",
			Facts{View: open("agent", lbl(models.Error)).record(models.Error, models.ReasonSubstrateError).v, Actor: running,
				Request: intake.Recover},
			Decision{Rule: RuleErrorSuspend, Action: SuspendOnly}},
		{"clear-error by a developer, no actor: create", Facts{
			View:     open("agent").record(models.Error, models.ReasonActorMissing).marker(110, 1, 100).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			Decision{Rule: RuleClearErrorCreate, Action: Create}},
		{"clear-error by a developer, partial create: create", Facts{
			View:     open("agent").record(models.Error, models.ReasonSubstrateError).v,
			Actor:    partial,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			Decision{Rule: RuleClearErrorCreate, Action: Create}},
		{"clear-error by a reporter, no actor: refused, error label restored", Facts{
			View:    open("agent").record(models.Error, models.ReasonActorMissing).v,
			Actor:   noActor,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleStartRefused,
				Action: None,
				Writes: models.Writes{Label: lbl(models.Error), Notes: []models.NewNote{startRefusedNote}},
			}},
		{"clear-error by a developer, no actor, turn limit: error kept", Facts{
			View:     open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			Decision{
				Rule:   RuleClearErrorTurnLimit,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"clear-error, record error, agent::error present, complete actor: waiting", Facts{
			View:    open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnTimeout).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleNoRecordWaiting,
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"clear-error, record error, agent::error present, complete actor, trigger comment: waiting, activate", Facts{
			View: open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnTimeout).
				marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			Decision{
				Rule:   RuleTriggerActivate,
				Action: Activate,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"clear-error, no record, agent::error present, complete actor: waiting", Facts{
			View:    open("agent", lbl(models.Error)).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleNoRecordWaiting,
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"clear-error by a developer, agent::error present, no actor: create", Facts{
			View:     open("agent", lbl(models.Error)).record(models.Error, models.ReasonActorMissing).marker(110, 1, 100).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			Decision{Rule: RuleClearErrorCreate, Action: Create}},
		{"clear-error by a developer, no record, agent::error present, no actor: create", Facts{
			View:     open("agent", lbl(models.Error)).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			Decision{Rule: RuleClearErrorCreate, Action: Create}},
		{"clear-error by a developer, agent::error present, partial create: create", Facts{
			View:     open("agent", lbl(models.Error)).record(models.Error, models.ReasonSubstrateError).v,
			Actor:    partial,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			Decision{Rule: RuleClearErrorCreate, Action: Create}},
		{"clear-error by a reporter, agent::error present, no actor: refused, error kept", Facts{
			View:    open("agent", lbl(models.Error)).record(models.Error, models.ReasonActorMissing).v,
			Actor:   noActor,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleStartRefused,
				Action: None,
				Writes: models.Writes{Notes: []models.NewNote{startRefusedNote}},
			}},
		{"clear-error, author unknown, agent::error present, no actor: error kept", Facts{
			View:          open("agent", lbl(models.Error)).record(models.Error, models.ReasonActorMissing).v,
			Actor:         noActor,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			Decision{Rule: RuleAuthorUnknown, Action: None}},
		{"clear-error, agent::error present, complete actor, turn limit: error kept", Facts{
			View:    open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleClearErrorTurnLimit,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"clear-error, no record, agent::done and agent::error: done", Facts{
			View:    open("agent", lbl(models.Error), lbl(models.Done)).v,
			Actor:   noActor,
			Request: intake.ClearError,
		},
			Decision{Rule: RuleDoneKept, Action: None, Writes: models.Writes{Label: lbl(models.Done)}}},
		{"recover, partial create, no record: create", Facts{
			View:    open("agent").v,
			Actor:   partial,
			Request: intake.Recover,
		},
			Decision{Rule: RuleRecoverPartialCreate, Action: Create}},
		{"start, author unknown, no actor: no create, no note", Facts{
			View:          open("agent").v,
			Actor:         noActor,
			Request:       intake.Start,
			AuthorUnknown: true,
		},
			Decision{Rule: RuleAuthorUnknown, Action: None}},
		{"start, author unknown, partial create: no create, no note", Facts{
			View:          open("agent").v,
			Actor:         partial,
			Request:       intake.Start,
			AuthorUnknown: true,
		},
			Decision{Rule: RuleAuthorUnknown, Action: None}},
		{"clear-error, author unknown, no actor: error label restored", Facts{
			View:          open("agent").record(models.Error, models.ReasonActorMissing).v,
			Actor:         noActor,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			Decision{Rule: RuleAuthorUnknown, Action: None, Writes: models.Writes{Label: lbl(models.Error)}}},
		{"clear-error, author unknown, partial create: error label restored", Facts{
			View:          open("agent").record(models.Error, models.ReasonSubstrateError).v,
			Actor:         partial,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			Decision{Rule: RuleAuthorUnknown, Action: None, Writes: models.Writes{Label: lbl(models.Error)}}},
		{"clear-error, author unknown, no actor, turn limit: error kept", Facts{
			View:          open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:         noActor,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			Decision{
				Rule:   RuleClearErrorTurnLimit,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"start, record done, author unknown: refused note", Facts{
			View:          open("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:         noActor,
			Request:       intake.Start,
			AuthorUnknown: true,
		},
			Decision{Rule: RuleDoneStartRefused, Action: None, Writes: models.Writes{Notes: []models.NewNote{refusedNote}}}},
		{"recover, partial create, record waiting: actor missing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   partial,
			Request: intake.Recover,
		},
			Decision{
				Rule:   RulePartialCreateActorMissing,
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorMissing), Label: lbl(models.Error)},
			}},
		{"start, no record, agent::done: refused note", Facts{
			View:     open("agent", lbl(models.Done)).v,
			Actor:    noActor,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{Rule: RuleDoneStartRefused, Action: None, Writes: models.Writes{Notes: []models.NewNote{refusedNote}}}},
		{"no record, agent::error, trigger comment: no activation", Facts{
			View:             open("agent", lbl(models.Error)).marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Rule: RuleErrorKept, Action: None}},
		{"no record, agent::error, running actor: suspend", Facts{
			View:    open("agent", lbl(models.Error)).v,
			Actor:   running,
			Request: intake.Recover,
		},
			Decision{Rule: RuleErrorSuspend, Action: SuspendOnly}},
		{"no record, agent::done and agent::error: done", Facts{
			View:    open("agent", lbl(models.Error), lbl(models.Done)).v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{Rule: RuleDoneKept, Action: None, Writes: models.Writes{Label: lbl(models.Done)}}},
		{"no record, agent::waiting is not a record: waiting", Facts{
			View:    open("agent", lbl(models.Waiting)).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{
				Rule:   RuleNoRecordWaiting,
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"clear-error, no record, complete actor: waiting", Facts{
			View:    open("agent").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			Decision{
				Rule:   RuleNoRecordWaiting,
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"comment, no actor, no record: nothing", Facts{
			View:    open("agent").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{Rule: RuleNoActorIgnored, Action: None}},
	}
}

func TestDecideTable(t *testing.T) {
	for _, c := range decideCases() {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, Decide(c.facts, limits))
		})
	}
}

type pollCase struct {
	name string
	p    PollFacts
	want PollDecision
}

func pollCases() []pollCase {
	base := open("agent", lbl(models.Working)).record(models.Working, "").note(100, "dev1", "go").marker(110, 1, 100)
	gone := closed("agent", lbl(models.Working)).record(models.Working, "").note(100, "dev1", "go").marker(110, 1, 100)

	return []pollCase{
		{"no marker yet: wait", PollFacts{View: base.v, Actor: running, Since: 110, Elapsed: time.Minute},
			PollDecision{Rule: PollWait, Action: Wait}},
		{"new marker by note id with repeated n, no comment: suspend → waiting",
			PollFacts{
				View:    base.note(115, "dev1", "more").marker(120, 1, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Rule: PollMarkerIdle, Action: Suspend, Marker: models.Marker{NoteID: 120, Turn: 1, Last: 115},
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)}}},
		{"new marker, comment after last=: stay resumed",
			PollFacts{
				View:    base.note(115, "dev1", "more").note(118, "reporter", "and this").marker(120, 2, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Rule: PollMarkerComment, Action: StayResumed, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 115}}},
		{"comment by any person keeps it resumed",
			PollFacts{
				View:    base.marker(120, 2, 110).note(121, "reporter", "hi").v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Rule: PollMarkerComment, Action: StayResumed, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 110}}},
		{"marker reaches the turn limit: suspend → error",
			PollFacts{
				View:    base.note(115, "dev1", "more").note(118, "dev1", "and more").marker(120, 3, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Rule: PollMarkerLimit, Action: Suspend, Marker: models.Marker{NoteID: 120, Turn: 3, Last: 115},
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)}}},
		{"crashed: error, no suspend", PollFacts{View: base.v, Actor: crashed, Since: 110, Elapsed: time.Minute},
			PollDecision{
				Rule:   PollCrashed,
				Action: End,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorCrashed), Label: lbl(models.Error)},
			}},
		{"suspended by hand, no marker: waiting", PollFacts{View: base.v, Actor: suspended, Since: 110, Elapsed: time.Minute},
			PollDecision{
				Rule:   PollSuspendedByHand,
				Action: End,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"actor gone: actor missing", PollFacts{View: base.v, Actor: noActor, Since: 110, Elapsed: time.Minute},
			PollDecision{
				Rule:   PollActorGone,
				Action: End,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorMissing), Label: lbl(models.Error)},
			}},
		{"stop pending waits for the marker", PollFacts{
			View:        gone.v,
			Actor:       running,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			PollDecision{Rule: PollWait, Action: Wait}},
		{"stop pending after the marker: stop", PollFacts{
			View:        gone.marker(120, 2, 110).note(121, "dev1", "x").v,
			Actor:       running,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			PollDecision{Rule: PollStopPending, Action: StopNow, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 110}}},
		{"stop pending at the timeout: stop", PollFacts{
			View:        gone.v,
			Actor:       running,
			Since:       110,
			Elapsed:     15 * time.Minute,
			StopPending: true,
		},
			PollDecision{Rule: PollStopPending, Action: StopNow}},
		{"stop pending, actor crashed: stop", PollFacts{
			View:        gone.v,
			Actor:       crashed,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			PollDecision{Rule: PollStopPending, Action: StopNow}},
		{"stale stop: issue enabled again, the marker is handled normally",
			PollFacts{View: base.marker(120, 2, 110).v, Actor: running, Since: 110, Elapsed: time.Minute, StopPending: true},
			PollDecision{Rule: PollMarkerIdle, Action: Suspend, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 110},
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)}}},
		{"stale stop: issue enabled again, no marker yet: wait",
			PollFacts{View: base.v, Actor: running, Since: 110, Elapsed: time.Minute, StopPending: true},
			PollDecision{Rule: PollWait, Action: Wait}},
		{"timeout while running: suspend → error", PollFacts{
			View:    base.v,
			Actor:   running,
			Since:   110,
			Elapsed: 15 * time.Minute,
		},
			PollDecision{
				Rule:   PollTimeout,
				Action: Suspend,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnTimeout), Label: lbl(models.Error)},
			}},
		{"timeout while transitional: error without suspend", PollFacts{
			View:    base.v,
			Actor:   moving,
			Since:   110,
			Elapsed: 16 * time.Minute,
		},
			PollDecision{
				Rule:   PollTimeout,
				Action: End,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnTimeout), Label: lbl(models.Error)},
			}},
		{"transitional before the timeout: wait", PollFacts{View: base.v, Actor: moving, Since: 110, Elapsed: time.Minute},
			PollDecision{Rule: PollWait, Action: Wait}},
		{"a person's forged marker is not a marker", PollFacts{
			View:    base.note(120, "dev1", "<!-- krci-agent-turn n=2 exit=0 last=119 -->").v,
			Actor:   running,
			Since:   110,
			Elapsed: time.Minute,
		},
			PollDecision{Rule: PollWait, Action: Wait}},
	}
}

func TestPollTable(t *testing.T) {
	for _, c := range pollCases() {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, Poll(c.p, limits))
		})
	}
}

func TestActionNames(t *testing.T) {
	assert.Equal(t, "none", None.String())
	assert.Equal(t, "continue", Continue.String())
	assert.Equal(t, "suspend", SuspendOnly.String())
	assert.Equal(t, "suspend", Suspend.String())
	assert.Equal(t, "stop-now", StopNow.String())
}

// declaredRules returns the Rule constants of decide.go by constant name.
func declaredRules(t *testing.T) map[string]Rule {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), "decide.go", nil, 0)
	require.NoError(t, err)

	rules := map[string]Rule{}

	for _, decl := range f.Decls {
		gen, isGen := decl.(*ast.GenDecl)
		if !isGen || gen.Tok != token.CONST {
			continue
		}

		for _, spec := range gen.Specs {
			vs, _ := spec.(*ast.ValueSpec)

			if typ, isIdent := vs.Type.(*ast.Ident); !isIdent || typ.Name != "Rule" {
				continue
			}

			require.Len(t, vs.Values, len(vs.Names))

			for i, name := range vs.Names {
				lit, isLit := vs.Values[i].(*ast.BasicLit)
				require.True(t, isLit, name.Name)

				v, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)

				rules[name.Name] = Rule(v)
			}
		}
	}

	return rules
}

func TestEveryRuleHasATableCase(t *testing.T) {
	decideRules, pollRules := map[Rule]bool{}, map[Rule]bool{}

	for _, c := range decideCases() {
		decideRules[c.want.Rule] = true
	}

	for _, c := range pollCases() {
		pollRules[c.want.Rule] = true
	}

	declared := declaredRules(t)
	require.NotEmpty(t, declared)

	names := map[Rule]string{}

	for name, rule := range declared {
		assert.Regexp(t, `^[a-z]+(-[a-z]+)*$`, string(rule), name)
		assert.Empty(t, names[rule], "%s: rule name %q is declared twice", name, rule)

		names[rule] = name

		switch {
		case strings.HasPrefix(name, "Rule"):
			assert.True(t, decideRules[rule], "%s (%q) is returned by no TestDecideTable case", name, rule)
		case strings.HasPrefix(name, "Poll"):
			assert.True(t, pollRules[rule], "%s (%q) is returned by no TestPollTable case", name, rule)
		default:
			t.Errorf("%s: a Rule constant starts with Rule (Decide) or Poll (Poll)", name)
		}
	}
}
