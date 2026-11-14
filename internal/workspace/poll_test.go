package workspace_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness/harnesstest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state/statetest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/workspace"
)

// A build output left untracked next to a commit fails publish; the log and the next prompt name it, and the turn
// that removes it publishes the commit.
func TestPollNamesAnUntrackedPathThatBlocksPublish(t *testing.T) {
	r := newRemote(t)
	dir := filepath.Join(t.TempDir(), "repo")
	key := models.IssueKey{ProjectID: 12, IID: 7}

	gl := gitlabtest.NewFake("krci-agent")
	gl.AddIssue(key, "group/project", "opened")
	gl.SetDefaultBranch(key.ProjectID, "main")

	agent := &harnesstest.Agent{}
	agent.Result = func(_ context.Context, call int) (string, int, error) {
		if call == 1 {
			commitContent(t, dir, "handler.go", "package main\n")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "gin-gonic"), []byte("ELF"), 0o600))

			return "Added the handler.", 0, nil
		}

		if !strings.Contains(agent.Prompts()[call-1], "gin-gonic") {
			return "Everything is committed.", 0, nil
		}

		require.NoError(t, os.Remove(filepath.Join(dir, "gin-gonic")))

		return "Removed the build output.", 0, nil
	}

	env := map[string]string{
		"GITLAB_URL": "https://gitlab.example.com", "PROJECT_ID": "12", "ISSUE_IID": "7",
		"PROJECT_PATH": "group/project", "BOT_USERNAME": "krci-agent", "SESSION_ID": "session-1",
		"AGENT_CONTRACT_VERSION": "1", "TURN_TIMEOUT_SECONDS": "900", "POLL_SECONDS": "10", "TURN_LIMIT": "20",
		"AGENT_STEPS": "40", "ANTHROPIC_MODEL": "model-x", "HOME": "/home/agent",
	}
	cfg, err := harness.Handshake(func(k string) string { return env[k] })
	require.NoError(t, err)

	logs := &bytes.Buffer{}
	h := harness.New(cfg, harness.Ports{
		GitLab: gl, Agent: agent, Workspace: workspace.New(workspace.Options{RepoURL: r.url, Dir: dir, Branch: issueBranch}),
		Clock: systemtest.NewClock(time.Now()), State: statetest.NewFake(), Log: slog.New(slog.NewTextHandler(logs, nil)),
	})

	turn := func(body string) string {
		t.Helper()
		gl.AddNote(key, models.Note{Author: "dev1", Body: body})
		h.Poll(context.Background())

		notes := gl.Notes(key)

		return notes[len(notes)-1].Body
	}

	assert.Contains(t, turn("please add a health handler"), "Turn failed at publish, exit 1.")
	assert.Contains(t, logs.String(), "gin-gonic", "the log names the untracked path")
	assert.False(t, r.hasBranchFile(t, "handler.go"))

	reply := turn("publish failed, please push your work")
	assert.NotContains(t, reply, "Turn failed")

	prompts := agent.Prompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "gin-gonic")
	assert.True(t, strings.HasPrefix(prompts[1], "dev1:\npublish failed, please push your work"), prompts[1])
	assert.True(t, r.hasBranchFile(t, "handler.go"), "the issue branch reached GitLab")
	assert.Len(t, gl.MergeRequests(key.ProjectID), 1)
}
