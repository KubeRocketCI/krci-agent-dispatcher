package decide

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/consttest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
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

func TestPollDecisionWaits(t *testing.T) {
	assert.True(t, PollDecision{Rule: PollWait}.Waits())
	assert.False(t, PollDecision{Rule: PollMarkerComment, Plan: Plan{advance(120)}}.Waits())
	assert.False(t, PollDecision{Rule: PollActorGone, Next: NextEnd}.Waits())
}

func TestNextsListsEveryNext(t *testing.T) {
	consts, err := consttest.Declared("decide.go", "Next")
	require.NoError(t, err)
	require.Len(t, Nexts(), len(consts), "a Next constant is missing from nexts")

	for i, n := range Nexts() {
		assert.Equal(t, Next(i), n, "nexts is not in declaration order")
		assert.NotEmpty(t, n.String(), "%s has no name", consts[i].Name)
	}
}

func TestPlanString(t *testing.T) {
	p := Plan{
		effect(KindNoteMR, FailLog),
		effect(KindDelete, FailError),
		{Kind: KindWrite, Fail: FailLog, Writes: models.Writes{
			Record: &models.Record{State: models.Error, Reason: models.ReasonTurnLimit}, Label: models.Error,
		}},
		loop(120, true),
	}

	assert.Equal(t, "note-mr!log > delete!error > write(record=error/turn-limit,label=error)!log > "+
		"loop(since=120,recovered)", p.String())
	assert.Equal(t, "-", Plan(nil).String())
	assert.True(t, Plan{recheck()}.IsRecheck())
	assert.False(t, Plan{recheck(), loop(0, false)}.IsRecheck())
}
