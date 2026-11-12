// Package intake maps a GitLab event payload to a request for an issue lane. Pure.
// The event selects the issue and the request only; the lane re-reads the state.
package intake

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// GitLab X-Gitlab-Event values.
const (
	IssueHook = "Issue Hook"
	NoteHook  = "Note Hook"
)

// Kind is the request kind for an issue lane.
type Kind int

// Request kinds. Activate also re-evaluates the issue after a person's label or field change.
const (
	Ignore Kind = iota
	Start
	Activate
	Stop
	ClearError
	Recover
)

var kindNames = map[Kind]string{
	Ignore: "ignore", Start: "start", Activate: "activate", Stop: "stop", ClearError: "clear-error", Recover: "recover",
}

func (k Kind) String() string { return kindNames[k] }

var kinds = []Kind{Ignore, Start, Activate, Stop, ClearError, Recover}

// Kinds returns every request kind, Ignore first.
func Kinds() []Kind { return slices.Clone(kinds) }

// Request is one request for the issue lane of Key. Author is the start author (Issue Hook user) or the note
// author (Note Hook). An Ignore request keeps the key and author the payload had, for logs.
type Request struct {
	Key    models.IssueKey
	Kind   Kind
	Author string
}

type apiLabel struct {
	Title string `json:"title"`
}

type payload struct {
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		ID int64 `json:"id"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int64  `json:"iid"`
		Action       string `json:"action"`
		NoteableType string `json:"noteable_type"`
	} `json:"object_attributes"`
	Labels  *[]apiLabel `json:"labels"`
	Changes struct {
		Labels *struct {
			Previous []apiLabel `json:"previous"`
			Current  []apiLabel `json:"current"`
		} `json:"labels"`
	} `json:"changes"`
	Issue *struct {
		IID    int64       `json:"iid"`
		Labels *[]apiLabel `json:"labels"`
	} `json:"issue"`
}

// Parse maps one event to a request. bot is the bot user; labels names the trigger label and the state labels.
// Events other than Issue Hook and Note Hook, confidential ones included, map to an empty Ignore request.
// An issue event by the bot user maps to Stop or Ignore. A note or an issue update maps to Ignore when the
// payload lists the issue labels, none of them is the trigger label or a state label, and the update removed none.
// It returns an error only when the body is not a JSON object.
func Parse(event string, body []byte, bot string, labels models.AgentLabels) (Request, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Request{}, fmt.Errorf("event payload: %w", err)
	}

	switch event {
	case IssueHook:
		return issueRequest(p, bot, labels), nil
	case NoteHook:
		return noteRequest(p, bot, labels), nil
	default:
		return Request{}, nil
	}
}

func has(labels []apiLabel, title string) bool {
	return slices.ContainsFunc(labels, func(l apiLabel) bool { return l.Title == title })
}

// hasTriggerOrStateLabel reports whether labels hold the trigger label or a state label.
func hasTriggerOrStateLabel(labels []apiLabel, names models.AgentLabels) bool {
	return slices.ContainsFunc(labels, func(l apiLabel) bool {
		return l.Title == names.Trigger() || names.IsState(l.Title)
	})
}

func issueRequest(p payload, bot string, labels models.AgentLabels) Request {
	key := models.IssueKey{ProjectID: p.Project.ID, IID: p.ObjectAttributes.IID}
	r := Request{Key: key, Author: p.User.Username}

	if key.ProjectID == 0 || key.IID == 0 {
		return r
	}

	switch p.ObjectAttributes.Action {
	case "open", "reopen":
		if has(deref(p.Labels), labels.Trigger()) {
			r.Kind = Start
		}
	case "close":
		r.Kind = Stop
	case "update":
		r.Kind = updateKind(p, labels)
	}

	if r.Author == bot && r.Kind != Stop {
		r.Kind = Ignore
	}

	return r
}

func deref(labels *[]apiLabel) []apiLabel {
	if labels == nil {
		return nil
	}

	return *labels
}

func updateKind(p payload, labels models.AgentLabels) Kind {
	trigger := labels.Trigger()
	now := p.Labels

	var previous []apiLabel

	if ch := p.Changes.Labels; ch != nil {
		errorLabel := labels.State(models.Error)

		switch {
		case has(ch.Previous, trigger) && !has(ch.Current, trigger):
			return Stop
		case has(ch.Current, trigger) && !has(ch.Previous, trigger):
			return Start
		case has(ch.Previous, errorLabel) && !has(ch.Current, errorLabel):
			return ClearError
		}

		previous = ch.Previous

		if now == nil {
			now = &ch.Current
		}
	}

	if now != nil && !hasTriggerOrStateLabel(*now, labels) && !hasTriggerOrStateLabel(previous, labels) {
		return Ignore
	}

	return Activate
}

func noteRequest(p payload, bot string, labels models.AgentLabels) Request {
	r := Request{Key: models.IssueKey{ProjectID: p.Project.ID}, Author: p.User.Username}
	if p.Issue != nil {
		r.Key.IID = p.Issue.IID
	}

	if p.ObjectAttributes.NoteableType != "Issue" || r.Key.IID == 0 || r.Key.ProjectID == 0 || r.Author == "" ||
		r.Author == bot {
		return r
	}

	if issueLabels := p.Issue.Labels; issueLabels != nil && !hasTriggerOrStateLabel(*issueLabels, labels) {
		return r
	}

	r.Kind = Activate

	return r
}
