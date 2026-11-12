package decide

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/consttest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

const bot = "krci-agent"

var limits = Limits{
	TurnLimit: 3, Bot: bot, TriggerLabel: "agent", TriggerRole: models.Developer, TurnTimeout: 15 * time.Minute,
}

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
	moving    = models.ActorView{Template: true, Actor: true, EgressPolicy: true, State: models.Resuming}
)

func lbl(s models.AgentState) string { return models.StateLabel(s) }

type decideCase struct {
	name  string
	facts Facts
	want  string
}

func decideCases() []decideCase {
	return []decideCase{
		{"issue gone: discard", Facts{Issue: ReadGone, Actor: running, Request: intake.Activate},
			"issue-gone delete!abort"},
		{"issue unavailable: keep", Facts{Issue: ReadUnavailable, Actor: running, Request: intake.Recover},
			"issue-unavailable suspend-if-running!abort"},
		{"actor unknown, record waiting: substrate error", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   models.ActorView{State: models.Unknown},
			Request: intake.Activate,
		},
			"actor-unknown-started write(record=error/substrate-error,label=error)!log"},
		{"actor unknown, no record, label working: substrate error", Facts{
			View:    open("agent", lbl(models.Working)).v,
			Actor:   models.ActorView{State: models.Unknown},
			Request: intake.Stop,
		},
			"actor-unknown-started write(record=error/substrate-error,label=error)!log"},
		{"actor unknown, record done: nothing", Facts{
			View:    closed("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:   models.ActorView{State: models.Unknown},
			Request: intake.Stop,
		},
			"actor-unknown -"},
		{"closed, actor: stop", Facts{
			View:    closed("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   suspended,
			Request: intake.Stop,
		},
			"not-enabled-stop note-mr!log > delete!error > write(record=done,label=done)!log"},
		{"trigger label removed, record error: stop", Facts{
			View:    open(lbl(models.Error)).record(models.Error, models.ReasonTurnLimit).v,
			Actor:   suspended,
			Request: intake.Stop,
		},
			"not-enabled-stop note-mr!log > delete!error > write(record=done,label=done)!log"},
		{"closed, no actor, record working: stop", Facts{
			View:    closed("agent").record(models.Working, "").v,
			Actor:   noActor,
			Request: intake.Recover,
		},
			"not-enabled-stop note-mr!log > write(record=done,label=done)!log"},
		{"closed, running actor: stop", Facts{
			View:    closed("agent").record(models.Working, "").v,
			Actor:   running,
			Request: intake.Stop,
		},
			"not-enabled-stop note-mr!log > delete!error > write(record=done,label=done)!log"},
		{"closed, no trigger label, no record: nothing", Facts{
			View:    closed("bug").v,
			Actor:   noActor,
			Request: intake.Stop,
		},
			"not-enabled-no-record -"},
		{"reopened without the trigger label: nothing", Facts{
			View:    open("bug").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			"not-enabled-no-record -"},
		{"start, record done: refused note, label repaired", Facts{
			View:    open("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Start,
		},
			"done-start-refused write(label=done,note=krci-agent-refused)!log"},
		{"comment, record done: no note", Facts{
			View:    open("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			"done-kept -"},
		{"comment, record done, running actor: stop", Facts{
			View:    open("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:   running,
			Request: intake.Activate,
		},
			"done-running-stop note-mr!log > delete!error > write(record=done,label=done)!log"},
		{"start, record done, running actor: stop, no refused note", Facts{
			View:     open("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:    running,
			Request:  intake.Start,
			AuthorOK: true,
		},
			"done-running-stop note-mr!log > delete!error > write(record=done,label=done)!log"},
		{"record error, trigger comment: no activation, label repaired", Facts{
			View:             open("agent").record(models.Error, models.ReasonTurnTimeout).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"error-kept write(label=error)!log"},
		{"record error, start: nothing", Facts{
			View:     open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnTimeout).v,
			Actor:    suspended,
			Request:  intake.Start,
			AuthorOK: true,
		},
			"error-kept -"},
		{"start by a developer, no actor: create", Facts{
			View:     open("agent").v,
			Actor:    noActor,
			Request:  intake.Start,
			AuthorOK: true,
		},
			"start-create create!error > inspect!error > write(record=waiting,label=waiting)!log > " +
				"resume!error-fresh > reload!log > write(record=working,label=working)!log > loop(since=0)"},
		{"start by a reporter, no actor: refused", Facts{
			View:     open("agent").v,
			Actor:    noActor,
			Request:  intake.Start,
			AuthorOK: false,
		},
			"start-refused write(note=krci-agent-start-refused)!log"},
		{"start by a reporter, partial create: refused", Facts{
			View:     open("agent").v,
			Actor:    partial,
			Request:  intake.Start,
			AuthorOK: false,
		},
			"start-refused write(note=krci-agent-start-refused)!log"},
		{"start by a developer, partial create: create", Facts{
			View:     open("agent").v,
			Actor:    partial,
			Request:  intake.Start,
			AuthorOK: true,
		},
			"start-partial-create delete!error > create!error > inspect!error > " +
				"write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=0)"},
		{"comment, partial create, no record: nothing", Facts{
			View:    open("agent").v,
			Actor:   partial,
			Request: intake.Activate,
		},
			"partial-create-ignored -"},
		{"complete actor, no record, no marker: activation", Facts{
			View:     open("agent").v,
			Actor:    suspended,
			Request:  intake.Start,
			AuthorOK: true,
		},
			"first-activation write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=0)"},
		{"complete actor, no record, marker: waiting", Facts{
			View:    open("agent").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Recover,
		},
			"no-record-waiting write(record=waiting,label=waiting)!log"},
		{"complete actor, no record, marker, trigger comment: waiting, activation", Facts{
			View:             open("agent").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"trigger-activate write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=110)"},
		{"suspended, trigger comment: activation", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"trigger-activate resume!error-fresh > reload!log > write(record=working,label=working)!log > " +
				"loop(since=110)"},
		{"crashed, trigger comment: revert first", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:            crashed,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"trigger-activate revert!error-fresh > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=110)"},
		{"suspended, record waiting, no trigger comment: nothing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			"waiting-kept -"},
		{"trigger comment at the turn limit: error", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(130, 3, 125).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"turn-limit write(record=error/turn-limit,label=error)!log"},
		{"failed turns count to the turn limit", Facts{View: open("agent", lbl(models.Waiting)).record(models.Waiting, "").
			note(130, bot, "failed\n<!-- krci-agent-turn n=3 exit=1 last=125 -->").v,
			Actor: suspended, Request: intake.Activate, TriggerAfterLast: true},
			"turn-limit write(record=error/turn-limit,label=error)!log"},
		{"trigger comment below the turn limit: activation", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(130, 2, 125).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"trigger-activate resume!error-fresh > reload!log > write(record=working,label=working)!log > " +
				"loop(since=130)"},
		{"running, record working: continue", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").v,
			Actor:   running,
			Request: intake.Recover,
		},
			"running-continue loop(since=0,recovered)"},
		{"running after two turns: continue from the previous marker", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").marker(140, 2, 135).marker(110, 1, 100).v,
			Actor:   running,
			Request: intake.Recover,
		},
			"running-continue loop(since=110,recovered)"},
		{"running, record waiting: record working, continue", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   running,
			Request: intake.Recover,
		},
			"running-continue write(record=working,label=working)!log > loop(since=0,recovered)"},
		{"suspended, record working, no trigger comment: waiting", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Recover,
		},
			"working-record-waiting write(record=waiting,label=waiting)!log"},
		{"suspended, record working, trigger comment: waiting, activation", Facts{
			View:             open("agent", lbl(models.Working)).record(models.Working, "").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"trigger-activate write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=110)"},
		{"crashed, record waiting, no trigger comment: nothing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   crashed,
			Request: intake.Recover,
		},
			"crashed-no-trigger -"},
		{"crashed, record working: actor crashed", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").v,
			Actor:   crashed,
			Request: intake.Recover,
		},
			"crashed-working write(record=error/actor-crashed,label=error)!log"},
		{"transitional: re-check", Facts{
			View:     open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:    moving,
			Request:  intake.Activate,
			Rechecks: 2,
		},
			"transitional-recheck recheck"},
		{"transitional after 3 re-checks: actor busy", Facts{
			View:     open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:    moving,
			Request:  intake.Activate,
			Rechecks: 3,
		},
			"actor-busy write(record=error/actor-busy,label=error)!log"},
		{"no actor, record waiting: actor missing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			"actor-missing write(record=error/actor-missing,label=error)!log"},
		{"no actor, record working: actor missing", Facts{
			View:    open("agent").record(models.Working, "").v,
			Actor:   noActor,
			Request: intake.Recover,
		},
			"actor-missing write(record=error/actor-missing,label=error)!log"},
		{"hand-set agent::done, record waiting: label repaired", Facts{
			View:    open("agent", lbl(models.Done)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			"waiting-kept write(label=waiting)!log"},
		{"two state labels: label repaired", Facts{
			View:    open("agent", lbl(models.Waiting), lbl(models.Working)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			"waiting-kept write(label=waiting)!log"},
		{"closed, record done, no state label: label repaired", Facts{
			View:    closed("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Stop,
		},
			"not-enabled-done write(label=done)!log"},
		{"clear-error, record error, no trigger comment: waiting", Facts{
			View:    open("agent").record(models.Error, models.ReasonTurnTimeout).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			"no-record-waiting write(record=waiting,label=waiting)!log"},
		{"clear-error, record error, crashed, trigger comment: waiting, revert", Facts{
			View:             open("agent").record(models.Error, models.ReasonActorCrashed).marker(110, 1, 100).v,
			Actor:            crashed,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			"trigger-activate write(record=waiting,label=waiting)!log > revert!error-fresh > " +
				"resume!error-fresh > reload!log > write(record=working,label=working)!log > loop(since=110)"},
		{"clear-error, complete actor, turn limit, trigger comment: error kept", Facts{
			View:             open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:            suspended,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			"clear-error-turn-limit write(record=error/turn-limit,label=error)!log"},
		{"clear-error, complete actor, turn limit, no trigger comment: error kept", Facts{
			View:    open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			"clear-error-turn-limit write(record=error/turn-limit,label=error)!log"},
		{"clear-error, running actor, turn limit: suspend, error kept", Facts{
			View:    open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:   running,
			Request: intake.ClearError,
		},
			"clear-error-turn-limit write(record=error/turn-limit,label=error)!log > suspend!abort"},
		{"agent::done removed, record done: label repaired", Facts{
			View:    open("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			"done-kept write(label=done)!log"},
		{"hand-set agent::working, record waiting: label repaired", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			"waiting-kept write(label=waiting)!log"},
		{"record error, running actor: suspend, error kept",
			Facts{View: open("agent", lbl(models.Error)).record(models.Error, models.ReasonSubstrateError).v, Actor: running,
				Request: intake.Recover},
			"error-suspend suspend!abort"},
		{"clear-error by a developer, no actor: create", Facts{
			View:     open("agent").record(models.Error, models.ReasonActorMissing).marker(110, 1, 100).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			"clear-error-create create!error > inspect!error > write(record=waiting,label=waiting)!log > " +
				"resume!error-fresh > reload!log > write(record=working,label=working)!log > loop(since=110)"},
		{"clear-error by a developer, partial create: create", Facts{
			View:     open("agent").record(models.Error, models.ReasonSubstrateError).v,
			Actor:    partial,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			"clear-error-create delete!error > create!error > inspect!error > " +
				"write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=0)"},
		{"clear-error by a reporter, no actor: refused, error label restored", Facts{
			View:    open("agent").record(models.Error, models.ReasonActorMissing).v,
			Actor:   noActor,
			Request: intake.ClearError,
		},
			"start-refused write(label=error,note=krci-agent-start-refused)!log"},
		{"clear-error by a developer, no actor, turn limit: error kept", Facts{
			View:     open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			"clear-error-turn-limit write(record=error/turn-limit,label=error)!log"},
		{"clear-error, record error, agent::error present, complete actor: waiting", Facts{
			View:    open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnTimeout).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			"no-record-waiting write(record=waiting,label=waiting)!log"},
		{"clear-error, record error, agent::error present, complete actor, trigger comment: waiting, activate", Facts{
			View: open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnTimeout).
				marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			"trigger-activate write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=110)"},
		{"clear-error, no record, agent::error present, complete actor: waiting", Facts{
			View:    open("agent", lbl(models.Error)).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			"no-record-waiting write(record=waiting,label=waiting)!log"},
		{"clear-error by a developer, agent::error present, no actor: create", Facts{
			View:     open("agent", lbl(models.Error)).record(models.Error, models.ReasonActorMissing).marker(110, 1, 100).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			"clear-error-create create!error > inspect!error > write(record=waiting,label=waiting)!log > " +
				"resume!error-fresh > reload!log > write(record=working,label=working)!log > loop(since=110)"},
		{"clear-error by a developer, no record, agent::error present, no actor: create", Facts{
			View:     open("agent", lbl(models.Error)).v,
			Actor:    noActor,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			"clear-error-create create!error > inspect!error > write(record=waiting,label=waiting)!log > " +
				"resume!error-fresh > reload!log > write(record=working,label=working)!log > loop(since=0)"},
		{"clear-error by a developer, agent::error present, partial create: create", Facts{
			View:     open("agent", lbl(models.Error)).record(models.Error, models.ReasonSubstrateError).v,
			Actor:    partial,
			Request:  intake.ClearError,
			AuthorOK: true,
		},
			"clear-error-create delete!error > create!error > inspect!error > " +
				"write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=0)"},
		{"clear-error by a reporter, agent::error present, no actor: refused, error kept", Facts{
			View:    open("agent", lbl(models.Error)).record(models.Error, models.ReasonActorMissing).v,
			Actor:   noActor,
			Request: intake.ClearError,
		},
			"start-refused write(note=krci-agent-start-refused)!log"},
		{"clear-error, author unknown, agent::error present, no actor: error kept", Facts{
			View:          open("agent", lbl(models.Error)).record(models.Error, models.ReasonActorMissing).v,
			Actor:         noActor,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			"author-unknown -"},
		{"clear-error, agent::error present, complete actor, turn limit: error kept", Facts{
			View:    open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			"clear-error-turn-limit write(record=error/turn-limit,label=error)!log"},
		{"clear-error, no record, agent::done and agent::error: done", Facts{
			View:    open("agent", lbl(models.Error), lbl(models.Done)).v,
			Actor:   noActor,
			Request: intake.ClearError,
		},
			"done-kept write(label=done)!log"},
		{"recover, partial create, no record: create", Facts{
			View:    open("agent").v,
			Actor:   partial,
			Request: intake.Recover,
		},
			"recover-partial-create delete!error > create!error > inspect!error > " +
				"write(record=waiting,label=waiting)!log > resume!error-fresh > reload!log > " +
				"write(record=working,label=working)!log > loop(since=0)"},
		{"start, author unknown, no actor: no create, no note", Facts{
			View:          open("agent").v,
			Actor:         noActor,
			Request:       intake.Start,
			AuthorUnknown: true,
		},
			"author-unknown -"},
		{"start, author unknown, partial create: no create, no note", Facts{
			View:          open("agent").v,
			Actor:         partial,
			Request:       intake.Start,
			AuthorUnknown: true,
		},
			"author-unknown -"},
		{"clear-error, author unknown, no actor: error label restored", Facts{
			View:          open("agent").record(models.Error, models.ReasonActorMissing).v,
			Actor:         noActor,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			"author-unknown write(label=error)!log"},
		{"clear-error, author unknown, partial create: error label restored", Facts{
			View:          open("agent").record(models.Error, models.ReasonSubstrateError).v,
			Actor:         partial,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			"author-unknown write(label=error)!log"},
		{"clear-error, author unknown, no actor, turn limit: error kept", Facts{
			View:          open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:         noActor,
			Request:       intake.ClearError,
			AuthorUnknown: true,
		},
			"clear-error-turn-limit write(record=error/turn-limit,label=error)!log"},
		{"start, record done, author unknown: refused note", Facts{
			View:          open("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:         noActor,
			Request:       intake.Start,
			AuthorUnknown: true,
		},
			"done-start-refused write(note=krci-agent-refused)!log"},
		{"recover, partial create, record waiting: actor missing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   partial,
			Request: intake.Recover,
		},
			"partial-create-actor-missing write(record=error/actor-missing,label=error)!log"},
		{"start, no record, agent::done: refused note", Facts{
			View:     open("agent", lbl(models.Done)).v,
			Actor:    noActor,
			Request:  intake.Start,
			AuthorOK: true,
		},
			"done-start-refused write(note=krci-agent-refused)!log"},
		{"no record, agent::error, trigger comment: no activation", Facts{
			View:             open("agent", lbl(models.Error)).marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			"error-kept -"},
		{"no record, agent::error, running actor: suspend", Facts{
			View:    open("agent", lbl(models.Error)).v,
			Actor:   running,
			Request: intake.Recover,
		},
			"error-suspend suspend!abort"},
		{"no record, agent::done and agent::error: done", Facts{
			View:    open("agent", lbl(models.Error), lbl(models.Done)).v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			"done-kept write(label=done)!log"},
		{"no record, agent::waiting is not a record: waiting", Facts{
			View:    open("agent", lbl(models.Waiting)).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			"no-record-waiting write(record=waiting,label=waiting)!log"},
		{"clear-error, no record, complete actor: waiting", Facts{
			View:    open("agent").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			"no-record-waiting write(record=waiting,label=waiting)!log"},
		{"comment, no actor, no record: nothing", Facts{
			View:    open("agent").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			"no-actor-ignored -"},
	}
}

func TestDecideTable(t *testing.T) {
	for _, c := range decideCases() {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.facts, limits)
			assert.Equal(t, c.want, string(d.Rule)+" "+d.Plan.String())
		})
	}
}

func TestDecideStartRefusedNamesTriggerRole(t *testing.T) {
	for r, text := range map[models.Role]string{
		models.Developer:  "Only Developers or higher can start the agent.",
		models.Maintainer: "Only Maintainers or higher can start the agent.",
		models.Owner:      "Only Owners or higher can start the agent.",
	} {
		t.Run(r.String(), func(t *testing.T) {
			l := limits
			l.TriggerRole = r

			got := Decide(Facts{View: open("agent").v, Actor: noActor, Request: intake.Start}, l)

			want := models.NewNote{Body: text + "\n" + models.StartRefusedMarker, Marker: models.StartRefusedMarker}
			assert.Equal(t, Decision{Rule: RuleStartRefused, Plan: Plan{{
				Kind: KindWrite, Fail: FailLog, Writes: models.Writes{Notes: []models.NewNote{want}},
			}}}, got)
		})
	}
}

type pollCase struct {
	name string
	p    PollFacts
	want string
}

func pollCases() []pollCase {
	base := open("agent", lbl(models.Working)).record(models.Working, "").note(100, "dev1", "go").marker(110, 1, 100)
	gone := closed("agent", lbl(models.Working)).record(models.Working, "").note(100, "dev1", "go").marker(110, 1, 100)

	return []pollCase{
		{"no marker yet: wait", PollFacts{View: base.v, Actor: running, Since: 110, Elapsed: time.Minute},
			"wait - next=continue -"},
		{"new marker by note id with repeated n, no comment: suspend → waiting",
			PollFacts{
				View:    base.note(115, "dev1", "more").marker(120, 1, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			"marker-idle suspend!error > inspect!log > " +
				"write(record=waiting,label=waiting)!log next=end marker=120/n=1/last=115"},
		{"new marker, comment after last=: stay resumed",
			PollFacts{
				View:    base.note(115, "dev1", "more").note(118, "reporter", "and this").marker(120, 2, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			"marker-comment advance(since=120) next=continue marker=120/n=2/last=115"},
		{"comment by any person keeps it resumed",
			PollFacts{
				View:    base.marker(120, 2, 110).note(121, "reporter", "hi").v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			"marker-comment advance(since=120) next=continue marker=120/n=2/last=110"},
		{"marker reaches the turn limit: suspend → error",
			PollFacts{
				View:    base.note(115, "dev1", "more").note(118, "dev1", "and more").marker(120, 3, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			"marker-limit suspend!error > inspect!log > " +
				"write(record=error/turn-limit,label=error)!log next=end marker=120/n=3/last=115"},
		{"crashed: error, no suspend", PollFacts{View: base.v, Actor: crashed, Since: 110, Elapsed: time.Minute},
			"crashed write(record=error/actor-crashed,label=error)!log next=end -"},
		{"suspended by hand, no marker: waiting", PollFacts{View: base.v, Actor: suspended, Since: 110, Elapsed: time.Minute},
			"suspended-by-hand write(record=waiting,label=waiting)!log next=end -"},
		{"actor gone: actor missing", PollFacts{View: base.v, Actor: noActor, Since: 110, Elapsed: time.Minute},
			"actor-gone write(record=error/actor-missing,label=error)!log next=end -"},
		{"stop pending waits for the marker", PollFacts{
			View:        gone.v,
			Actor:       running,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			"wait - next=continue -"},
		{"stop pending after the marker: stop", PollFacts{
			View:        gone.marker(120, 2, 110).note(121, "dev1", "x").v,
			Actor:       running,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			"stop-pending note-mr!log > delete!error > " +
				"write(record=done,label=done)!log next=end marker=120/n=2/last=110"},
		{"stop pending at the timeout: stop", PollFacts{
			View:        gone.v,
			Actor:       running,
			Since:       110,
			Elapsed:     15 * time.Minute,
			StopPending: true,
		},
			"stop-pending note-mr!log > delete!error > write(record=done,label=done)!log next=end -"},
		{"stop pending, actor crashed: stop", PollFacts{
			View:        gone.v,
			Actor:       crashed,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			"stop-pending note-mr!log > delete!error > write(record=done,label=done)!log next=end -"},
		{"stale stop: issue enabled again, the marker is handled normally",
			PollFacts{View: base.marker(120, 2, 110).v, Actor: running, Since: 110, Elapsed: time.Minute, StopPending: true},
			"marker-idle suspend!error > inspect!log > " +
				"write(record=waiting,label=waiting)!log next=end marker=120/n=2/last=110"},
		{"stale stop: issue enabled again, no marker yet: wait",
			PollFacts{View: base.v, Actor: running, Since: 110, Elapsed: time.Minute, StopPending: true},
			"wait - next=continue -"},
		{"timeout while running: suspend → error", PollFacts{
			View:    base.v,
			Actor:   running,
			Since:   110,
			Elapsed: 15 * time.Minute,
		},
			"timeout suspend!error > inspect!log > write(record=error/turn-timeout,label=error)!log next=end -"},
		{"timeout while transitional: error without suspend", PollFacts{
			View:    base.v,
			Actor:   moving,
			Since:   110,
			Elapsed: 16 * time.Minute,
		},
			"timeout write(record=error/turn-timeout,label=error)!log next=end -"},
		{"transitional before the timeout: wait", PollFacts{View: base.v, Actor: moving, Since: 110, Elapsed: time.Minute},
			"wait - next=continue -"},
		{"issue gone: discard", PollFacts{Issue: ReadGone, Actor: running, Since: 110, Elapsed: time.Minute},
			"issue-gone delete!abort next=end -"},
		{"issue unavailable before the timeout: wait",
			PollFacts{Issue: ReadUnavailable, Actor: running, Since: 110, Elapsed: time.Minute},
			"wait - next=continue -"},
		{"issue unavailable at the timeout: suspend, record turn timeout with fresh status rows",
			PollFacts{Issue: ReadUnavailable, Since: 110, Elapsed: 15 * time.Minute},
			"read-timeout suspend!log > reload!log > write(record=error/turn-timeout,label=error)!log next=end -"},
		{"actor unknown before the timeout, new marker: wait", PollFacts{
			View:    base.marker(120, 2, 110).v,
			Actor:   models.ActorView{State: models.Unknown},
			Since:   110,
			Elapsed: time.Minute,
		},
			"wait - next=continue -"},
		{"actor unknown at the timeout, stop pending: read timeout", PollFacts{
			View:        gone.v,
			Actor:       models.ActorView{State: models.Unknown},
			Since:       110,
			Elapsed:     15 * time.Minute,
			StopPending: true,
		},
			"read-timeout suspend!log > reload!log > write(record=error/turn-timeout,label=error)!log next=end -"},
		{"a person's forged marker is not a marker", PollFacts{
			View:    base.note(120, "dev1", "<!-- krci-agent-turn n=2 exit=0 last=119 -->").v,
			Actor:   running,
			Since:   110,
			Elapsed: time.Minute,
		},
			"wait - next=continue -"},
	}
}

func TestPollTable(t *testing.T) {
	for _, c := range pollCases() {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, pollOutcome(Poll(c.p, limits)))
		})
	}
}

// pollOutcome renders a poll decision as rule, plan, next and the new marker.
func pollOutcome(d PollDecision) string {
	marker := "-"
	if d.Marker.NoteID != 0 {
		marker = fmt.Sprintf("marker=%d/n=%d/last=%d", d.Marker.NoteID, d.Marker.Turn, d.Marker.Last)
	}

	return fmt.Sprintf("%s %s next=%s %s", d.Rule, d.Plan, d.Next, marker)
}

func TestIssueReadsListsEveryIssueRead(t *testing.T) {
	consts, err := consttest.Declared("decide.go", "IssueRead")
	require.NoError(t, err)
	require.Len(t, IssueReads(), len(consts), "an IssueRead constant is missing from issueReads")

	for i, r := range IssueReads() {
		assert.Equal(t, IssueRead(i), r, "issueReads is not in declaration order")
		assert.NotEmpty(t, r.String(), "%s has no name", consts[i].Name)
	}
}

// declaredRules returns the Rule constants of decide.go by constant name.
func declaredRules(t *testing.T) map[string]Rule {
	t.Helper()

	consts, err := consttest.Declared("decide.go", "Rule")
	require.NoError(t, err)

	rules := map[string]Rule{}

	for _, c := range consts {
		require.NotEmpty(t, c.Value, "%s: a Rule constant is a string literal", c.Name)

		rules[c.Name] = Rule(c.Value)
	}

	return rules
}

func TestEveryRuleHasATableCase(t *testing.T) {
	decideRules, pollRules := map[Rule]bool{}, map[Rule]bool{}

	for _, c := range decideCases() {
		rule, _, _ := strings.Cut(c.want, " ")
		decideRules[Rule(rule)] = true
	}

	for _, c := range pollCases() {
		rule, _, _ := strings.Cut(c.want, " ")
		pollRules[Rule(rule)] = true
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
