package models

import (
	"fmt"
	"regexp"
	"strings"
)

// Record is the bot user's statement of an issue's agent state and error reason.
// It is the first line of the status note and the truth for done and error (ADR 0004).
type Record struct {
	State  AgentState
	Reason string
}

// Error reasons (exact).
const (
	ReasonTurnLimit      = "turn limit"
	ReasonTurnTimeout    = "turn timeout"
	ReasonActorCrashed   = "actor crashed"
	ReasonActorMissing   = "actor missing"
	ReasonSubstrateError = "substrate error"
	ReasonActorBusy      = "actor busy"
	ReasonAgentProfile   = "agent profile"
)

var recordRe = regexp.MustCompile(`^<!-- krci-agent-status state=(working|waiting|done|error) reason=(.*?) -->$`)

// RecordLine returns the record line: <!-- krci-agent-status state=<state> reason=<reason> -->.
func RecordLine(r Record) string {
	return fmt.Sprintf("<!-- krci-agent-status state=%s reason=%s -->", r.State, r.Reason)
}

// ParseRecord parses the record from the first line of body.
func ParseRecord(body string) (Record, bool) {
	first, _, _ := strings.Cut(body, "\n")

	sub := recordRe.FindStringSubmatch(strings.TrimRight(first, "\r"))
	if sub == nil {
		return Record{}, false
	}

	return Record{State: AgentState(sub[1]), Reason: sub[2]}, true
}

// StatusView holds the rows of the status note.
type StatusView struct {
	State    AgentState
	Actor    string
	ActorUID string
	Pod      string
	Session  string
	Updated  string
	Reason   string
	Profile  string
	Image    string
	// Detail is the rule of a failed profile resolution; written only with that record.
	Detail string
}

// StatusBody returns the status note body: the record line, then the status table; a Reason row in error, a Detail
// row when set. Detail is written as one table cell: | becomes /, ` becomes ', CR and LF become spaces.
func StatusBody(r Record, v StatusView) string {
	var b strings.Builder

	b.WriteString(RecordLine(r))
	b.WriteString("\n**Agent status**\n\n| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| State | `%s` |\n", v.State)
	fmt.Fprintf(&b, "| Actor | `%s` |\n", v.Actor)
	fmt.Fprintf(&b, "| Actor UID | `%s` |\n", v.ActorUID)
	fmt.Fprintf(&b, "| Worker pod | `%s` |\n", v.Pod)
	fmt.Fprintf(&b, "| Session | `%s` |\n", v.Session)
	fmt.Fprintf(&b, "| Profile | `%s` |\n", v.Profile)
	fmt.Fprintf(&b, "| Image | `%s` |\n", v.Image)
	fmt.Fprintf(&b, "| Updated (UTC) | %s |\n", v.Updated)

	if r.Reason != "" {
		fmt.Fprintf(&b, "| Reason | %s |\n", r.Reason)
	}

	if v.Detail != "" {
		fmt.Fprintf(&b, "| Detail | %s |\n", cellText.Replace(v.Detail))
	}

	return b.String()
}

var cellText = strings.NewReplacer("|", "/", "`", "'", "\r", " ", "\n", " ")

var statusRowRe = regexp.MustCompile("(?m)^\\| ([A-Za-z ()]+) \\| `?(.*?)`? \\|$")

// statusRows maps each row name of the status table to its StatusView field.
var statusRows = map[string]func(v *StatusView) *string{
	"Actor":         func(v *StatusView) *string { return &v.Actor },
	"Actor UID":     func(v *StatusView) *string { return &v.ActorUID },
	"Worker pod":    func(v *StatusView) *string { return &v.Pod },
	"Session":       func(v *StatusView) *string { return &v.Session },
	"Profile":       func(v *StatusView) *string { return &v.Profile },
	"Image":         func(v *StatusView) *string { return &v.Image },
	"Updated (UTC)": func(v *StatusView) *string { return &v.Updated },
	"Reason":        func(v *StatusView) *string { return &v.Reason },
	"Detail":        func(v *StatusView) *string { return &v.Detail },
}

// ParseStatusView reads the rows of a status note body written by StatusBody.
func ParseStatusView(body string) (StatusView, bool) {
	if _, ok := ParseRecord(body); !ok {
		return StatusView{}, false
	}

	var v StatusView

	for _, sub := range statusRowRe.FindAllStringSubmatch(body, -1) {
		if sub[1] == "State" {
			v.State = AgentState(sub[2])
		} else if field, ok := statusRows[sub[1]]; ok {
			*field(&v) = sub[2]
		}
	}

	return v, true
}

// MergeStatus returns the status rows to write with record r: next when set, else prev without its Detail.
// State and Reason always come from r.
func MergeStatus(prev StatusView, r Record, next *StatusView) StatusView {
	v := prev
	v.Detail = ""

	if next != nil {
		v = *next
	}

	v.State, v.Reason = r.State, r.Reason

	return v
}
