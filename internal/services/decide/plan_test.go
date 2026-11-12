package decide

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/consttest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

func TestEffectKindsListsEveryEffectKind(t *testing.T) {
	consts, err := consttest.Declared("plan.go", "EffectKind")
	require.NoError(t, err)
	require.Len(t, EffectKinds(), len(consts), "an EffectKind constant is missing from kinds")

	for i, k := range EffectKinds() {
		assert.Equal(t, EffectKind(i), k, "kinds is not in declaration order")
		assert.NotEmpty(t, k.String(), "%s has no name", consts[i].Name)
	}
}

func TestFailsListsEveryFailure(t *testing.T) {
	consts, err := consttest.Declared("plan.go", "Fail")
	require.NoError(t, err)
	require.Len(t, Fails(), len(consts), "a Fail constant is missing from fails")

	for i, f := range Fails() {
		assert.Equal(t, Fail(i), f, "fails is not in declaration order")
		assert.NotEmpty(t, f.String(), "%s has no name", consts[i].Name)
	}
}

func TestDecideDoneStartRefusedNote(t *testing.T) {
	got := Decide(Facts{View: open("agent").record(models.Done, "").v, Actor: noActor, Request: intake.Start}, limits)

	assert.Equal(t, Decision{Rule: RuleDoneStartRefused, Plan: Plan{{
		Kind: KindWrite, Fail: FailLog, Writes: models.Writes{
			Label: "agent::done",
			Notes: []models.NewNote{{Body: models.RefusedText + "\n" + models.RefusedMarker, Marker: models.RefusedMarker}},
		},
	}}}, got)
}

func TestPlanString(t *testing.T) {
	p := Plan{
		effect(KindNoteMR, FailLog),
		effect(KindDelete, FailError),
		{Kind: KindWrite, Fail: FailLog, Writes: models.Writes{
			Record: &models.Record{State: models.Error, Reason: models.ReasonTurnLimit}, Label: "agent::error",
		}},
		loop(120, true),
	}

	assert.Equal(t, "note-mr!log > delete!error > write(record=error/turn-limit,label=error)!log > "+
		"loop(since=120,recovered)", p.String())
	assert.Equal(t, "-", Plan(nil).String())
	assert.True(t, Plan{{Kind: KindRecheck}}.IsRecheck())
	assert.False(t, Plan{{Kind: KindRecheck}, loop(0, false)}.IsRecheck())
}
