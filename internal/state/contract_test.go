package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state/statetest"
)

// store is the harness StateStore under test; corrupt makes the stored state unreadable.
type store interface {
	Load(session string) (models.HarnessState, error)
	Save(session string, s models.HarnessState) error
}

type subject struct {
	store   store
	corrupt func()
}

func subjects() map[string]func(t *testing.T) subject {
	return map[string]func(t *testing.T) subject{
		"fake": func(*testing.T) subject {
			f := statetest.NewFake()

			return subject{store: f, corrupt: f.Corrupt}
		},
		"file": func(t *testing.T) subject {
			dir := filepath.Join(t.TempDir(), ".krci-agent")

			return subject{store: state.NewFile(dir), corrupt: func() {
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not json"), 0o600))
			}}
		},
	}
}

func TestContract(t *testing.T) {
	cases := map[string]func(t *testing.T, s subject){
		"MissingIsZero":     missingIsZero,
		"PendingRoundTrips": pendingRoundTrips,
		"SavingZeroClears":  savingZeroClears,
		"SessionScoping":    sessionScoping,
		"CorruptIsReported": corruptIsReported,
	}
	for name, newSubject := range subjects() {
		for c, run := range cases {
			t.Run(name+"/"+c, func(t *testing.T) { run(t, newSubject(t)) })
		}
	}
}

func missingIsZero(t *testing.T, s subject) {
	got, err := s.store.Load("s1")
	require.NoError(t, err)
	assert.Equal(t, models.HarnessState{}, got)
}

func pendingRoundTrips(t *testing.T, s subject) {
	want := models.HarnessState{Pending: &models.PendingReply{Turn: 3, Body: "text\n<!-- m -->"}, Started: true}
	require.NoError(t, s.store.Save("s1", want))

	got, err := s.store.Load("s1")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func savingZeroClears(t *testing.T, s subject) {
	require.NoError(t, s.store.Save("s1", models.HarnessState{
		Pending: &models.PendingReply{Turn: 1, Body: "b"}, Started: true,
	}))
	require.NoError(t, s.store.Save("s1", models.HarnessState{Started: true}))

	got, err := s.store.Load("s1")
	require.NoError(t, err)
	assert.Equal(t, models.HarnessState{Started: true}, got)
}

func sessionScoping(t *testing.T, s subject) {
	require.NoError(t, s.store.Save("s1", models.HarnessState{
		Pending: &models.PendingReply{Turn: 2, Body: "b"}, Started: true,
	}))

	other, err := s.store.Load("s2")
	require.NoError(t, err)
	assert.Equal(t, models.HarnessState{}, other, "state of another session is ignored")

	require.NoError(t, s.store.Save("s2", models.HarnessState{Started: true}))

	first, err := s.store.Load("s1")
	require.NoError(t, err)
	assert.Equal(t, models.HarnessState{}, first, "a save for a new session replaces the old state")
}

func corruptIsReported(t *testing.T, s subject) {
	s.corrupt()

	_, err := s.store.Load("s1")
	require.ErrorIs(t, err, models.ErrStateCorrupt)

	require.NoError(t, s.store.Save("s1", models.HarnessState{Started: true}))

	got, err := s.store.Load("s1")
	require.NoError(t, err, "a save replaces a corrupt state")
	assert.True(t, got.Started)
}
