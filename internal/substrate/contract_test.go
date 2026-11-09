package substrate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/substratetest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/templates"
)

const (
	atespace = "krci-agents"
	actor    = "issue-12-7"
)

// port is the Substrate port as the dispatcher declares it.
type port interface {
	Inspect(ctx context.Context, name string) (models.ActorView, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Create(ctx context.Context, name string, tmpl *ateapi.ActorTemplate, pol *ateapi.EgressPolicy) error
	Resume(ctx context.Context, name string) error
	Suspend(ctx context.Context, name string) error
	Revert(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
}

func subjects() map[string]func(t *testing.T, f *substratetest.Fake) port {
	return map[string]func(t *testing.T, f *substratetest.Fake) port{
		"fake": func(_ *testing.T, f *substratetest.Fake) port { return f },
		"grpc": func(t *testing.T, f *substratetest.Fake) port { return newGRPC(t, f).client },
	}
}

func rendered(t *testing.T) (*ateapi.ActorTemplate, *ateapi.EgressPolicy) {
	t.Helper()

	tmpl, pol, err := templates.Render(templates.Params{
		Atespace: atespace, Pool: "workers", SecretsNamespace: atespace, Name: actor,
		Image: "localhost:5001/krci-agent-go:dev", GitLabURL: "https://gitlab.example", ProjectPath: "krci/app",
		BotUsername: "krci-agent", SessionID: "s1", IID: 7,
	})
	require.NoError(t, err)

	return tmpl, pol
}

func TestContract(t *testing.T) {
	cases := map[string]func(t *testing.T, f *substratetest.Fake, p port){
		"CreateOrder":            CreateOrder,
		"DeleteOrderOnlyPresent": DeleteOrderOnlyPresent,
		"DeleteClearsPartial":    DeleteClearsPartial,
		"InspectReportsParts":    InspectReportsParts,
		"InspectStateMapping":    InspectStateMapping,
		"ResumeNoRoom":           ResumeNoRoom,
		"ResumeSuspendRevert":    ResumeSuspendRevert,
		"NotFound":               NotFound,
		"ListByPrefix":           ListByPrefix,
		"CreateCompletesPartial": CreateCompletesPartial,
	}

	for subject, build := range subjects() {
		for name, run := range cases {
			t.Run(subject+"/"+name, func(t *testing.T) {
				f := substratetest.NewFake(atespace)
				run(t, f, build(t, f))
			})
		}
	}
}

func CreateOrder(t *testing.T, f *substratetest.Fake, p port) {
	tmpl, pol := rendered(t)
	require.NoError(t, p.Create(context.Background(), actor, tmpl, pol))

	assert.Equal(t, []string{"create-template " + actor, "create-actor " + actor, "create-policy " + actor}, f.Ops())

	v, err := p.Inspect(context.Background(), actor)
	require.NoError(t, err)
	assert.True(t, v.Complete())
	assert.Equal(t, models.Suspended, v.State)
	assert.NotEmpty(t, v.UID)
}

func DeleteOrderOnlyPresent(t *testing.T, f *substratetest.Fake, p port) {
	ctx := context.Background()
	tmpl, pol := rendered(t)
	require.NoError(t, p.Create(ctx, actor, tmpl, pol))
	f.ClearOps()

	require.NoError(t, p.Delete(ctx, actor))
	assert.Equal(t, []string{"delete-policy " + actor, "delete-actor " + actor, "delete-template " + actor}, f.Ops())

	f.ClearOps()
	require.NoError(t, p.Delete(ctx, actor))
	assert.Empty(t, f.Ops())

	v, err := p.Inspect(ctx, actor)
	require.NoError(t, err)
	assert.Equal(t, models.ActorView{State: models.None}, v)
}

func DeleteClearsPartial(t *testing.T, f *substratetest.Fake, p port) {
	f.AddPartial(actor, true)

	require.NoError(t, p.Delete(context.Background(), actor))
	assert.Equal(t, []string{"delete-actor " + actor, "delete-template " + actor}, f.Ops())

	v, err := p.Inspect(context.Background(), actor)
	require.NoError(t, err)
	assert.False(t, v.Exists())
}

func InspectReportsParts(t *testing.T, f *substratetest.Fake, p port) {
	f.AddPartial(actor, false)

	v, err := p.Inspect(context.Background(), actor)
	require.NoError(t, err)
	assert.Equal(t, models.ActorView{Template: true, State: models.None}, v)
	assert.True(t, v.Partial())

	f.AddPartial("issue-12-8", true)

	v, err = p.Inspect(context.Background(), "issue-12-8")
	require.NoError(t, err)
	assert.True(t, v.Template)
	assert.True(t, v.Actor)
	assert.False(t, v.EgressPolicy)
	assert.Equal(t, models.Suspended, v.State)
}

func InspectStateMapping(t *testing.T, f *substratetest.Fake, p port) {
	tmpl, pol := rendered(t)
	require.NoError(t, p.Create(context.Background(), actor, tmpl, pol))

	for in, want := range map[ateapi.ActorState]models.ActorState{
		ateapi.ActorState_ACTOR_STATE_RESUMING:   models.Transitional,
		ateapi.ActorState_ACTOR_STATE_RUNNING:    models.Running,
		ateapi.ActorState_ACTOR_STATE_SUSPENDING: models.Transitional,
		ateapi.ActorState_ACTOR_STATE_SUSPENDED:  models.Suspended,
		ateapi.ActorState_ACTOR_STATE_CRASHED:    models.Crashed,
		ateapi.ActorState_ACTOR_STATE_DELETING:   models.Transitional,
		ateapi.ActorState_ACTOR_STATE_REVERTING:  models.Transitional,
	} {
		f.SetState(actor, in)

		v, err := p.Inspect(context.Background(), actor)
		require.NoError(t, err)
		assert.Equal(t, want, v.State, in.String())
	}

	v, err := p.Inspect(context.Background(), "issue-12-99")
	require.NoError(t, err)
	assert.Equal(t, models.None, v.State)
}

func ResumeNoRoom(t *testing.T, f *substratetest.Fake, p port) {
	tmpl, pol := rendered(t)
	require.NoError(t, p.Create(context.Background(), actor, tmpl, pol))
	f.SetNoRoom(true)

	err := p.Resume(context.Background(), actor)
	require.ErrorIs(t, err, substrate.ErrNoRoom)

	v, _ := p.Inspect(context.Background(), actor)
	assert.Equal(t, models.Suspended, v.State)
}

func ResumeSuspendRevert(t *testing.T, f *substratetest.Fake, p port) {
	ctx := context.Background()
	tmpl, pol := rendered(t)
	require.NoError(t, p.Create(ctx, actor, tmpl, pol))

	require.NoError(t, p.Resume(ctx, actor))
	v, _ := p.Inspect(ctx, actor)
	assert.Equal(t, models.Running, v.State)
	assert.NotEmpty(t, v.Pod)

	require.NoError(t, p.Suspend(ctx, actor))
	v, _ = p.Inspect(ctx, actor)
	assert.Equal(t, models.Suspended, v.State)

	f.SetState(actor, ateapi.ActorState_ACTOR_STATE_CRASHED)
	require.NoError(t, p.Revert(ctx, actor))
	v, _ = p.Inspect(ctx, actor)
	assert.Equal(t, models.Suspended, v.State)
}

func NotFound(t *testing.T, _ *substratetest.Fake, p port) {
	ctx := context.Background()

	require.ErrorIs(t, p.Resume(ctx, actor), substrate.ErrNotFound)
	require.ErrorIs(t, p.Suspend(ctx, actor), substrate.ErrNotFound)
	require.ErrorIs(t, p.Revert(ctx, actor), substrate.ErrNotFound)
}

func ListByPrefix(t *testing.T, f *substratetest.Fake, p port) {
	for _, n := range []string{"issue-2-5", "other-x", "issue-1-1", "issue-3-3", "probe-a"} {
		f.AddPartial(n, true)
	}

	got, err := p.List(context.Background(), "issue-")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"issue-1-1", "issue-2-5", "issue-3-3"}, got)
}

func CreateCompletesPartial(t *testing.T, f *substratetest.Fake, p port) {
	f.AddPartial(actor, false)
	f.ClearOps()

	tmpl, pol := rendered(t)
	err := p.Create(context.Background(), actor, tmpl, pol)
	require.NoError(t, err, "an existing template from a cut create is reused")
	assert.Equal(t, []string{"create-actor " + actor, "create-policy " + actor}, f.Ops())

	v, err := p.Inspect(context.Background(), actor)
	require.NoError(t, err)
	assert.True(t, v.Complete())
}
