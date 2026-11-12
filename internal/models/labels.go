package models

import (
	"cmp"
	"slices"
)

const (
	defaultTriggerLabel = "krci-agent"
	defaultStatePrefix  = "krci::agent"
)

// AgentLabels names the trigger label and the state labels "<prefix>::<state>". The zero value holds the defaults:
// "krci-agent" and "krci::agent".
type AgentLabels struct {
	trigger     string
	statePrefix string
}

// Trigger returns the trigger label.
func (l AgentLabels) Trigger() string { return cmp.Or(l.trigger, defaultTriggerLabel) }

// State returns the state label of s, for example "krci::agent::waiting".
func (l AgentLabels) State(s AgentState) string {
	return cmp.Or(l.statePrefix, defaultStatePrefix) + "::" + string(s)
}

// States returns every state label, in AgentStates order.
func (l AgentLabels) States() []string {
	out := make([]string, 0, len(agentStates))
	for _, s := range agentStates {
		out = append(out, l.State(s))
	}

	return out
}

// StateFrom returns the agent state of the first state label in labels.
func (l AgentLabels) StateFrom(labels []string) (AgentState, bool) {
	for _, label := range labels {
		for _, s := range agentStates {
			if label == l.State(s) {
				return s, true
			}
		}
	}

	return "", false
}

// IsState reports whether label is a state label.
func (l AgentLabels) IsState(label string) bool { return slices.Contains(l.States(), label) }
