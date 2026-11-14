package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness/harnesstest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/workspace"
)

// scenario is the state a contract case asks of a subject: the issue branch on the remote before Prepare, the
// agent's work committed after Prepare, an untracked file the agent's work leaves, a failing verify step, a setup step
// that does not end.
type scenario struct {
	onRemote    bool
	committed   bool
	untracked   string
	verifyFails bool
	setupBlocks bool
}

// subject is a Workspace under contract; work runs after Prepare as the agent's turn.
type subject struct {
	ws   harness.Workspace
	work func()
}

func subjects() map[string]func(t *testing.T, sc scenario) subject {
	return map[string]func(t *testing.T, sc scenario) subject{
		"fake": func(_ *testing.T, sc scenario) subject {
			ws := &harnesstest.Workspace{NoNewCommits: !sc.committed, BlockPrepare: sc.setupBlocks}
			if sc.verifyFails {
				ws.VerifyErr = assert.AnError
			}

			return subject{ws: ws, work: func() {
				if sc.untracked != "" {
					ws.Untracked = []string{sc.untracked}
				}
			}}
		},
		"git": func(t *testing.T, sc scenario) subject {
			opts := workspace.Options{}
			if sc.verifyFails {
				opts.Verify = "exit 1"
			}

			if sc.setupBlocks {
				opts.Setup = "sleep 30"
			}

			r := newRemote(t)
			if sc.onRemote {
				r.pushBranch(t, "pushed.txt")
			}

			w, dir := newGit(t, r, opts)

			return subject{ws: w, work: func() {
				if sc.committed {
					commit(t, dir, "work.txt")
				}

				if sc.untracked != "" {
					require.NoError(t, os.WriteFile(filepath.Join(dir, sc.untracked), nil, 0o600))
				}
			}}
		},
	}
}

func TestContract(t *testing.T) {
	cases := map[string]func(t *testing.T, build func(*testing.T, scenario) subject){
		"PreparedWithoutWorkHasNoNewCommits": PreparedWithoutWorkHasNoNewCommits,
		"RemoteIssueBranchHasNoNewCommits":   RemoteIssueBranchHasNoNewCommits,
		"CommittedWorkIsVerifiedAndPushed":   CommittedWorkIsVerifiedAndPushed,
		"FailingVerifyFails":                 FailingVerifyFails,
		"WatchdogCutsPrepare":                WatchdogCutsPrepare,
		"UntrackedFilesFailAndAreNamed":      UntrackedFilesFailAndAreNamed,
	}

	for name, build := range subjects() {
		for c, run := range cases {
			t.Run(name+"/"+c, func(t *testing.T) { run(t, build) })
		}
	}
}

func PreparedWithoutWorkHasNoNewCommits(t *testing.T, build func(*testing.T, scenario) subject) {
	s := build(t, scenario{})
	prepare(t, s.ws, true)
	s.work()

	got, err := s.ws.NewCommitsCheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, got)
}

func RemoteIssueBranchHasNoNewCommits(t *testing.T, build func(*testing.T, scenario) subject) {
	s := build(t, scenario{onRemote: true})
	prepare(t, s.ws, true)
	s.work()
	prepare(t, s.ws, false)

	got, err := s.ws.NewCommitsCheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, got)
}

func CommittedWorkIsVerifiedAndPushed(t *testing.T, build func(*testing.T, scenario) subject) {
	s := build(t, scenario{committed: true})
	prepare(t, s.ws, true)
	s.work()

	got, err := s.ws.NewCommitsCheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{NewCommits: true}, got)
	require.NoError(t, s.ws.Verify(t.Context()))
	require.NoError(t, s.ws.Push(t.Context()))
}

func FailingVerifyFails(t *testing.T, build func(*testing.T, scenario) subject) {
	s := build(t, scenario{committed: true, verifyFails: true})
	prepare(t, s.ws, true)
	s.work()

	require.Error(t, s.ws.Verify(t.Context()))
}

func UntrackedFilesFailAndAreNamed(t *testing.T, build func(*testing.T, scenario) subject) {
	s := build(t, scenario{committed: true, untracked: "app.bin"})
	prepare(t, s.ws, true)
	s.work()

	_, err := s.ws.NewCommitsCheck(t.Context())
	require.Error(t, err)

	got, err := s.ws.Prepare(t.Context(), false)
	require.NoError(t, err)
	assert.Equal(t, harness.Prepared{Untracked: []string{"app.bin"}}, got)
}

func WatchdogCutsPrepare(t *testing.T, build func(*testing.T, scenario) subject) {
	s := build(t, scenario{setupBlocks: true})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	_, err := s.ws.Prepare(ctx, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
