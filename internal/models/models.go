// Package models holds the domain types of the dispatcher: issues, notes, turn markers, records and writes.
package models

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// IssueKey identifies one issue across projects.
type IssueKey struct {
	ProjectID int64
	IID       int64
}

// String returns "<project-id>/<iid>".
func (k IssueKey) String() string { return fmt.Sprintf("%d/%d", k.ProjectID, k.IID) }

// ActorName returns the name of the issue's actor, template and egress policy owner: "issue-<project-id>-<iid>".
func (k IssueKey) ActorName() string { return fmt.Sprintf("issue-%d-%d", k.ProjectID, k.IID) }

// IssueBranch returns the issue branch, the source branch of the agent's merge request: "agent/issue-<iid>".
func (k IssueKey) IssueBranch() string { return fmt.Sprintf("agent/issue-%d", k.IID) }

// Issue state values of the GitLab API.
const (
	IssueOpened = "opened"
	IssueClosed = "closed"
)

// Issue is the part of a GitLab issue the dispatcher reads.
type Issue struct {
	Key         IssueKey
	ProjectPath string
	IssueState  string
	Labels      []string
}

// Note is one issue note. UpdatedAt is the time of its last edit, the creation time when never edited.
type Note struct {
	ID        int64
	Body      string
	Author    string
	System    bool
	UpdatedAt time.Time
}

// AgentState is the agent state of an issue, as written in the record and shown by the state label.
type AgentState string

// Agent states.
const (
	Working AgentState = "working"
	Waiting AgentState = "waiting"
	Done    AgentState = "done"
	Error   AgentState = "error"
)

var agentStates = []AgentState{Working, Waiting, Done, Error}

const stateLabelPrefix = "agent::"

// StateLabel returns the state label of s, for example "agent::waiting".
func StateLabel(s AgentState) string { return stateLabelPrefix + string(s) }

// StateLabels returns every state label.
func StateLabels() []string {
	out := make([]string, 0, len(agentStates))
	for _, s := range agentStates {
		out = append(out, StateLabel(s))
	}

	return out
}

// StateFromLabels returns the agent state of the first state label in labels.
func StateFromLabels(labels []string) (AgentState, bool) {
	for _, l := range labels {
		for _, s := range agentStates {
			if l == StateLabel(s) {
				return s, true
			}
		}
	}

	return "", false
}

// HasLabel reports whether labels contains label.
func HasLabel(labels []string, label string) bool { return slices.Contains(labels, label) }

// IssueView is everything the dispatcher reads about one issue in one load.
// Status holds the rows of the status note; zero when HasRecord is false.
type IssueView struct {
	Issue     Issue
	Notes     []Note
	Record    Record
	HasRecord bool
	Status    StatusView
}

// NewIssueView returns the view of issue with notes in note id order and the record and status rows of the bot
// user's status note.
func NewIssueView(issue Issue, notes []Note, bot string) IssueView {
	notes = slices.Clone(notes)
	slices.SortFunc(notes, func(a, b Note) int { return cmp.Compare(a.ID, b.ID) })

	v := IssueView{Issue: issue, Notes: notes}

	if n, ok := StatusNote(notes, bot); ok {
		v.Record, v.HasRecord = ParseRecord(n.Body)
		v.Status, _ = ParseStatusView(n.Body)
	}

	return v
}

// LastUpdate returns the latest UpdatedAt of notes; the zero time for no notes.
func LastUpdate(notes []Note) time.Time {
	var last time.Time

	for _, n := range notes {
		if n.UpdatedAt.After(last) {
			last = n.UpdatedAt
		}
	}

	return last
}

// MergeNotes returns notes with each note of changed added, or put in place of the note with its id.
func MergeNotes(notes, changed []Note) []Note {
	at := make(map[int64]int, len(notes))
	out := slices.Clone(notes)

	for i, n := range out {
		at[n.ID] = i
	}

	for _, n := range changed {
		if i, ok := at[n.ID]; ok {
			out[i] = n
			continue
		}

		at[n.ID] = len(out)
		out = append(out, n)
	}

	return out
}

// NewNote is a note to post. A note whose Marker is already on the issue from the bot user is not posted again.
type NewNote struct {
	Body   string
	Marker string
}

// Writes are the GitLab changes of one action, applied in order: record and status note, label, notes.
// Label is a state label to set (the others are removed); empty leaves the labels unchanged.
type Writes struct {
	Record *Record
	Status *StatusView
	Label  string
	Notes  []NewNote
}

// Empty reports whether w changes nothing.
func (w Writes) Empty() bool { return w.Record == nil && w.Label == "" && len(w.Notes) == 0 }

// Note texts and hidden markers (exact).
const (
	RefusedMarker      = "<!-- krci-agent-refused -->"
	StartRefusedMarker = "<!-- krci-agent-start-refused -->"
	RefusedText        = "Agent stopped for this issue. Open a new issue to start again."
	NoRoomText         = "No free worker. Comment again later."
	StoppedMRMarker    = "<!-- krci-agent-stopped -->"
	StoppedMRText      = "Agent stopped; this MR is no longer updated."
)

// StatusNote returns the bot user's status note: the first bot note whose first line is a record.
func StatusNote(notes []Note, bot string) (Note, bool) {
	for _, n := range notes {
		if n.Author != bot || n.System {
			continue
		}

		if _, ok := ParseRecord(n.Body); ok {
			return n, true
		}
	}

	return Note{}, false
}

func isComment(n Note, bot string) bool { return !n.System && n.Author != bot }

func isBotNote(n Note, bot string) bool { return !n.System && n.Author == bot }

// HasMarker reports whether a bot user note contains marker.
func HasMarker(notes []Note, bot, marker string) bool {
	return slices.ContainsFunc(notes, func(n Note) bool { return isBotNote(n, bot) && strings.Contains(n.Body, marker) })
}

// CommentAfter reports whether a comment (a non-system note by a person) has a note id above after.
func CommentAfter(notes []Note, bot string, after int64) bool {
	return slices.ContainsFunc(notes, func(n Note) bool { return isComment(n, bot) && n.ID > after })
}
