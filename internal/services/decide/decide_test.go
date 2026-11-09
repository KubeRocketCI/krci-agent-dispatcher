package decide

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

const bot = "krci-agent"

var limits = Limits{MaxTurns: 3, Bot: bot, TriggerLabel: "agent", TurnTimeout: 15 * time.Minute}

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

func TestDecideTable(t *testing.T) {
	cases := []struct {
		name  string
		facts Facts
		want  Decision
	}{
		{"D1 closed with actor", Facts{
			View:    closed("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   suspended,
			Request: intake.Stop,
		},
			Decision{Row: "D1", Action: Stop, Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)}}},
		{"D1 label removed in error", Facts{
			View:    open(lbl(models.Error)).record(models.Error, models.ReasonTurnLimit).v,
			Actor:   suspended,
			Request: intake.Stop,
		},
			Decision{Row: "D1", Action: Stop, Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)}}},
		{"D1 cut stop: no actor, record working", Facts{
			View:    closed("agent").record(models.Working, "").v,
			Actor:   noActor,
			Request: intake.Recover,
		},
			Decision{Row: "D1", Action: Stop, Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)}}},
		{"D1 running actor", Facts{View: closed("agent").record(models.Working, "").v, Actor: running, Request: intake.Stop},
			Decision{Row: "D1", Action: Stop, Writes: models.Writes{Record: rec(models.Done, ""), Label: lbl(models.Done)}}},
		{"D2 close without agent", Facts{View: closed("bug").v, Actor: noActor, Request: intake.Stop},
			Decision{Row: "D2", Action: None}},
		{"D2 reopen without label", Facts{View: open("bug").v, Actor: noActor, Request: intake.Activate},
			Decision{Row: "D2", Action: None}},
		{"D3 start on done: refused once, label repaired", Facts{
			View:    open("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Start,
		},
			Decision{
				Row:    "D3",
				Action: None,
				Writes: models.Writes{Label: lbl(models.Done), Notes: []models.NewNote{refusedNote}},
			}},
		{"D3 comment on done: no note", Facts{
			View:    open("agent", lbl(models.Done)).record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{Row: "D3", Action: None}},
		{"D4 error ignores activate, label repaired", Facts{
			View:             open("agent").record(models.Error, models.ReasonTurnTimeout).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Row: "D4", Action: None, Writes: models.Writes{Label: lbl(models.Error)}}},
		{"D4 error ignores start", Facts{
			View:     open("agent", lbl(models.Error)).record(models.Error, models.ReasonTurnTimeout).v,
			Actor:    suspended,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{Row: "D4", Action: None}},
		{"D5 start by developer", Facts{View: open("agent").v, Actor: noActor, Request: intake.Start, AuthorOK: true},
			Decision{Row: "D5", Action: Create}},
		{"D6 start by reporter refused", Facts{View: open("agent").v, Actor: noActor, Request: intake.Start, AuthorOK: false},
			Decision{Row: "D6", Action: None, Writes: models.Writes{Notes: []models.NewNote{startRefusedNote}}}},
		{"D6 partial, reporter refused", Facts{View: open("agent").v, Actor: partial, Request: intake.Start, AuthorOK: false},
			Decision{Row: "D6", Action: None, Writes: models.Writes{Notes: []models.NewNote{startRefusedNote}}}},
		{"D7 partial create cleaned then created", Facts{
			View:     open("agent").v,
			Actor:    partial,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{Row: "D7", Action: Create}},
		{"D7 partial create, not a start", Facts{View: open("agent").v, Actor: partial, Request: intake.Recover},
			Decision{Row: "D7", Action: None}},
		{"D8 complete, no record, no marker: activation since 0", Facts{
			View:     open("agent").v,
			Actor:    suspended,
			Request:  intake.Start,
			AuthorOK: true,
		},
			Decision{
				Row:    "D8",
				Action: Activate,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"D8 complete, no record, marker: waiting", Facts{
			View:    open("agent").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Recover,
		},
			Decision{
				Row:    "D8",
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"D8 then D9 with a trigger comment", Facts{
			View:             open("agent").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{
				Row:    "D9",
				Action: Activate,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"D9 suspended, trigger comment", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Row: "D9", Action: Activate}},
		{"D9 crashed, trigger comment: revert first", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:            crashed,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Row: "D9", Action: Revert}},
		{"D9 no trigger comment: nothing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Row: "D18", Action: None}},
		{"D10 turn limit before resume", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(130, 3, 125).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{
				Row:    "D10",
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"D10 failed turns count", Facts{View: open("agent", lbl(models.Waiting)).record(models.Waiting, "").
			note(130, bot, "failed\n<!-- krci-agent-turn n=3 exit=1 last=125 -->").v,
			Actor: suspended, Request: intake.Activate, TriggerAfterLast: true},
			Decision{
				Row:    "D10",
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"D10 below the limit resumes", Facts{
			View:             open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(130, 2, 125).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{Row: "D9", Action: Activate}},
		{"D11 running, lane active", Facts{
			View:       open("agent", lbl(models.Working)).record(models.Working, "").v,
			Actor:      running,
			LaneActive: true,
			Request:    intake.Activate,
		},
			Decision{Row: "D11", Action: None}},
		{"D12 running, lane not active: recover", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").v,
			Actor:   running,
			Request: intake.Recover,
		},
			Decision{Row: "D12", Action: Continue}},
		{"D12 running, record waiting: record working", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   running,
			Request: intake.Recover,
		},
			Decision{
				Row:    "D12",
				Action: Continue,
				Writes: models.Writes{Record: rec(models.Working, ""), Label: lbl(models.Working)},
			}},
		{"D13 suspended, record working", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Recover,
		},
			Decision{
				Row:    "D13",
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"D13 then D9 with a trigger comment", Facts{
			View:             open("agent", lbl(models.Working)).record(models.Working, "").marker(110, 1, 100).v,
			Actor:            suspended,
			Request:          intake.Activate,
			TriggerAfterLast: true,
		},
			Decision{
				Row:    "D9",
				Action: Activate,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"D14 crashed, waiting, no trigger", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   crashed,
			Request: intake.Recover,
		},
			Decision{Row: "D14", Action: None}},
		{"D15 crashed, record working", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Working, "").v,
			Actor:   crashed,
			Request: intake.Recover,
		},
			Decision{
				Row:    "D15",
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorCrashed), Label: lbl(models.Error)},
			}},
		{"D16 transitional: re-check", Facts{
			View:     open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:    moving,
			Request:  intake.Activate,
			Rechecks: 2,
		},
			Decision{Row: "D16", Action: Recheck}},
		{"D16 transitional after 3 re-checks: actor busy", Facts{
			View:     open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:    moving,
			Request:  intake.Activate,
			Rechecks: 3,
		},
			Decision{
				Row:    "D16",
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorBusy), Label: lbl(models.Error)},
			}},
		{"D17 no actor, record waiting", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{
				Row:    "D17",
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorMissing), Label: lbl(models.Error)},
			}},
		{"D17 no actor, record working", Facts{
			View:    open("agent").record(models.Working, "").v,
			Actor:   noActor,
			Request: intake.Recover,
		},
			Decision{
				Row:    "D17",
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorMissing), Label: lbl(models.Error)},
			}},
		{"D18 hand-set label replaced from the record", Facts{
			View:    open("agent", lbl(models.Done)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Row: "D18", Action: None, Writes: models.Writes{Label: lbl(models.Waiting)}}},
		{"D18 two state labels", Facts{
			View:    open("agent", lbl(models.Waiting), lbl(models.Working)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Row: "D18", Action: None, Writes: models.Writes{Label: lbl(models.Waiting)}}},
		{"D18 closed, done record, label missing", Facts{
			View:    closed("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Stop,
		},
			Decision{Row: "D18", Action: None, Writes: models.Writes{Label: lbl(models.Done)}}},
		{"D18 labels match: nothing", Facts{
			View:    open("agent", lbl(models.Waiting)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Row: "D18", Action: None}},
		{"R2 agent::error removed → D8", Facts{
			View:    open("agent").record(models.Error, models.ReasonTurnTimeout).marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.ClearError,
		},
			Decision{
				Row:    "D8",
				Action: None,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"R2 agent::error removed, trigger comment → D9", Facts{
			View:             open("agent").record(models.Error, models.ReasonActorCrashed).marker(110, 1, 100).v,
			Actor:            crashed,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			Decision{
				Row:    "D9",
				Action: Revert,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"R2 turn limit stays terminal", Facts{
			View:             open("agent").record(models.Error, models.ReasonTurnLimit).marker(130, 3, 125).v,
			Actor:            suspended,
			Request:          intake.ClearError,
			TriggerAfterLast: true,
		},
			Decision{
				Row:    "D10",
				Action: None,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)},
			}},
		{"R2 agent::done removed → D3", Facts{
			View:    open("agent").record(models.Done, "").v,
			Actor:   noActor,
			Request: intake.Activate,
		},
			Decision{Row: "D3", Action: None, Writes: models.Writes{Label: lbl(models.Done)}}},
		{"R2 hand-set agent::working → D18", Facts{
			View:    open("agent", lbl(models.Working)).record(models.Waiting, "").marker(110, 1, 100).v,
			Actor:   suspended,
			Request: intake.Activate,
		},
			Decision{Row: "D18", Action: None, Writes: models.Writes{Label: lbl(models.Waiting)}}},
		{"D4 error with a running actor: suspend, keep the error",
			Facts{View: open("agent", lbl(models.Error)).record(models.Error, models.ReasonSubstrateError).v, Actor: running,
				Request: intake.Recover},
			Decision{Row: "D4", Action: SuspendOnly}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, Decide(c.facts, limits))
		})
	}
}

func TestPollTable(t *testing.T) {
	base := open("agent", lbl(models.Working)).record(models.Working, "").note(100, "dev1", "go").marker(110, 1, 100)
	gone := closed("agent", lbl(models.Working)).record(models.Working, "").note(100, "dev1", "go").marker(110, 1, 100)

	cases := []struct {
		name string
		p    PollFacts
		want PollDecision
	}{
		{"no marker yet: wait", PollFacts{View: base.v, Actor: running, Since: 110, Elapsed: time.Minute},
			PollDecision{Row: "wait", Action: Wait}},
		{"new marker by note id with repeated n, no comment: suspend → waiting",
			PollFacts{
				View:    base.note(115, "dev1", "more").marker(120, 1, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Row: "marker-idle", Action: Suspend, Marker: models.Marker{NoteID: 120, Turn: 1, Last: 115},
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)}}},
		{"new marker, comment after last=: stay resumed",
			PollFacts{
				View:    base.note(115, "dev1", "more").note(118, "reporter", "and this").marker(120, 2, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Row: "marker-comment", Action: StayResumed, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 115}}},
		{"comment by any person keeps it resumed",
			PollFacts{
				View:    base.marker(120, 2, 110).note(121, "reporter", "hi").v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Row: "marker-comment", Action: StayResumed, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 110}}},
		{"marker reaches the turn limit: suspend → error",
			PollFacts{
				View:    base.note(115, "dev1", "more").note(118, "dev1", "and more").marker(120, 3, 115).v,
				Actor:   running,
				Since:   110,
				Elapsed: time.Minute,
			},
			PollDecision{Row: "marker-limit", Action: Suspend, Marker: models.Marker{NoteID: 120, Turn: 3, Last: 115},
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnLimit), Label: lbl(models.Error)}}},
		{"crashed: error, no suspend", PollFacts{View: base.v, Actor: crashed, Since: 110, Elapsed: time.Minute},
			PollDecision{
				Row:    "crashed",
				Action: End,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonActorCrashed), Label: lbl(models.Error)},
			}},
		{"suspended by hand, no marker: waiting", PollFacts{View: base.v, Actor: suspended, Since: 110, Elapsed: time.Minute},
			PollDecision{
				Row:    "suspended-by-hand",
				Action: End,
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)},
			}},
		{"actor gone: actor missing", PollFacts{View: base.v, Actor: noActor, Since: 110, Elapsed: time.Minute},
			PollDecision{
				Row:    "actor-gone",
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
			PollDecision{Row: "wait", Action: Wait}},
		{"stop pending after the marker: stop", PollFacts{
			View:        gone.marker(120, 2, 110).note(121, "dev1", "x").v,
			Actor:       running,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			PollDecision{Row: "stop-pending", Action: StopNow, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 110}}},
		{"stop pending at the timeout: stop", PollFacts{
			View:        gone.v,
			Actor:       running,
			Since:       110,
			Elapsed:     15 * time.Minute,
			StopPending: true,
		},
			PollDecision{Row: "stop-pending", Action: StopNow}},
		{"stop pending, actor crashed: stop", PollFacts{
			View:        gone.v,
			Actor:       crashed,
			Since:       110,
			Elapsed:     time.Minute,
			StopPending: true,
		},
			PollDecision{Row: "stop-pending", Action: StopNow}},
		{"stale stop: issue enabled again, the marker is handled normally",
			PollFacts{View: base.marker(120, 2, 110).v, Actor: running, Since: 110, Elapsed: time.Minute, StopPending: true},
			PollDecision{Row: "marker-idle", Action: Suspend, Marker: models.Marker{NoteID: 120, Turn: 2, Last: 110},
				Writes: models.Writes{Record: rec(models.Waiting, ""), Label: lbl(models.Waiting)}}},
		{"stale stop: issue enabled again, no marker yet: wait",
			PollFacts{View: base.v, Actor: running, Since: 110, Elapsed: time.Minute, StopPending: true},
			PollDecision{Row: "wait", Action: Wait}},
		{"timeout while running: suspend → error", PollFacts{
			View:    base.v,
			Actor:   running,
			Since:   110,
			Elapsed: 15 * time.Minute,
		},
			PollDecision{
				Row:    "timeout",
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
				Row:    "timeout",
				Action: End,
				Writes: models.Writes{Record: rec(models.Error, models.ReasonTurnTimeout), Label: lbl(models.Error)},
			}},
		{"transitional before the timeout: wait", PollFacts{View: base.v, Actor: moving, Since: 110, Elapsed: time.Minute},
			PollDecision{Row: "wait", Action: Wait}},
		{"a person's forged marker is not a marker", PollFacts{
			View:    base.note(120, "dev1", "<!-- krci-agent-turn n=2 exit=0 last=119 -->").v,
			Actor:   running,
			Since:   110,
			Elapsed: time.Minute,
		},
			PollDecision{Row: "wait", Action: Wait}},
	}

	for _, c := range cases {
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
