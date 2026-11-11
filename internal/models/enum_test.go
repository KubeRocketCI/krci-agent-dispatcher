package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/consttest"
)

func declaredValues(t *testing.T, path, typeName string) []string {
	t.Helper()

	consts, err := consttest.Declared(path, typeName)
	require.NoError(t, err)
	require.NotEmpty(t, consts)

	out := make([]string, 0, len(consts))
	for _, c := range consts {
		out = append(out, c.Value)
	}

	return out
}

func TestAgentStatesListsEveryAgentState(t *testing.T) {
	got := make([]string, 0, len(agentStates))
	for _, s := range AgentStates() {
		got = append(got, string(s))
	}

	assert.ElementsMatch(t, declaredValues(t, "models.go", "AgentState"), got,
		"an AgentState constant is missing from agentStates")
}

func TestActorStatesListsEveryActorState(t *testing.T) {
	got := make([]string, 0, len(actorStates))
	for _, s := range ActorStates() {
		got = append(got, string(s))
	}

	assert.ElementsMatch(t, declaredValues(t, "actor.go", "ActorState"), got,
		"an ActorState constant is missing from actorStates")
}

func TestRecordRoundTripsEveryAgentState(t *testing.T) {
	for _, s := range AgentStates() {
		for _, reason := range []string{"", ReasonTurnLimit} {
			want := Record{State: s, Reason: reason}

			got, ok := ParseRecord(RecordLine(want) + "\nstatus rows")
			require.True(t, ok, "record state %q is not parsed: add it to recordRe", s)
			assert.Equal(t, want, got)
		}
	}
}

func TestActorStateClasses(t *testing.T) {
	type class struct{ transitional, settled bool }

	want := map[ActorState]class{
		None: {settled: true}, Suspended: {settled: true}, Crashed: {settled: true},
		Running: {}, Unknown: {},
		Resuming: {transitional: true}, Suspending: {transitional: true}, Reverting: {transitional: true},
		Deleting: {transitional: true}, Unspecified: {transitional: true},
	}

	for _, s := range ActorStates() {
		w, ok := want[s]
		if !assert.True(t, ok, "%s is not classified: decide whether it is transitional or settled", s) {
			continue
		}

		assert.Equal(t, w.transitional, s.Transitional(), "%s transitional", s)
		assert.Equal(t, w.settled, s.Settled(), "%s settled", s)
	}
}
