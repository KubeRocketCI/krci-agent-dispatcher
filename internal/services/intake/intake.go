// Package intake maps a GitLab event payload to a request for an issue lane (spec §5.2). Pure.
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

// Request kinds. Activate also re-evaluates the issue after a person's label or field change (spec R2).
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
	Labels  []apiLabel `json:"labels"`
	Changes struct {
		Labels *struct {
			Previous []apiLabel `json:"previous"`
			Current  []apiLabel `json:"current"`
		} `json:"labels"`
	} `json:"changes"`
	Issue *struct {
		IID int64 `json:"iid"`
	} `json:"issue"`
}

// Parse maps one event to a request. bot is the bot user; label is the trigger label.
// It returns an error only when the body is not a JSON object.
func Parse(event string, body []byte, bot, label string) (Request, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Request{}, fmt.Errorf("event payload: %w", err)
	}

	switch event {
	case IssueHook, "Confidential Issue Hook":
		return issueRequest(p, label), nil
	case NoteHook, "Confidential Note Hook":
		return noteRequest(p, bot), nil
	default:
		return Request{}, nil
	}
}

func has(labels []apiLabel, title string) bool {
	return slices.ContainsFunc(labels, func(l apiLabel) bool { return l.Title == title })
}

func issueRequest(p payload, trigger string) Request {
	key := models.IssueKey{ProjectID: p.Project.ID, IID: p.ObjectAttributes.IID}
	r := Request{Key: key, Author: p.User.Username}

	if key.ProjectID == 0 || key.IID == 0 {
		return r
	}

	errorLabel := models.StateLabel(models.Error)

	switch p.ObjectAttributes.Action {
	case "open", "reopen":
		if !has(p.Labels, trigger) {
			return r
		}

		r.Kind = Start
	case "close":
		r.Kind = Stop
	case "update":
		r.Kind = Activate

		if ch := p.Changes.Labels; ch != nil {
			switch {
			case has(ch.Previous, trigger) && !has(ch.Current, trigger):
				r.Kind = Stop
			case has(ch.Current, trigger) && !has(ch.Previous, trigger):
				r.Kind = Start
			case has(ch.Previous, errorLabel) && !has(ch.Current, errorLabel):
				r.Kind = ClearError
			}
		}
	default:
		return r
	}

	return r
}

func noteRequest(p payload, bot string) Request {
	r := Request{Key: models.IssueKey{ProjectID: p.Project.ID}, Author: p.User.Username}
	if p.Issue != nil {
		r.Key.IID = p.Issue.IID
	}

	if p.ObjectAttributes.NoteableType != "Issue" || r.Key.IID == 0 || r.Key.ProjectID == 0 || r.Author == "" ||
		r.Author == bot {
		return r
	}

	r.Kind = Activate

	return r
}
