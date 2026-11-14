package harness_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
)

func TestSystemPromptNamesTheIssueCheckoutAndBranch(t *testing.T) {
	cfg, err := harness.Handshake(getenv(validEnv()))
	require.NoError(t, err)

	p := harness.SystemPrompt(cfg, "/work/project", harness.Profile{})

	assert.Contains(t, p, "GitLab issue #7 of group/project")
	assert.Contains(t, p, "checked out at /work/project on branch agent/issue-7")
	assert.Contains(t, p, "Do not push")
	assert.True(t, strings.HasSuffix(p, "and any question."), "no profile text: the harness rules end the prompt")
}

func TestSystemPromptEndsWithTheProfilePrompt(t *testing.T) {
	cfg, err := harness.Handshake(getenv(validEnv()))
	require.NoError(t, err)

	p := harness.SystemPrompt(cfg, "/work/project", harness.Profile{Prompt: "Run make fmt before you commit.\n"})

	assert.True(t, strings.HasSuffix(p, "and any question.\n\nRun make fmt before you commit."), p)
}

func TestSystemPromptNamesThePublishRules(t *testing.T) {
	cfg, err := harness.Handshake(getenv(validEnv()))
	require.NoError(t, err)

	p := harness.SystemPrompt(cfg, "/work/project", harness.Profile{})

	assert.Contains(t, p, "Untracked files that .gitignore does not exclude fail the turn")
	assert.Contains(t, p, "Do not amend, rebase or squash commits already on origin/agent/issue-7")
	assert.Contains(t, p, "Take in the default branch with a merge.")
	assert.NotContains(t, p, "The harness verifies with", "no verify command: no verify rule")
}

func TestSystemPromptNamesTheVerifyCommand(t *testing.T) {
	cfg, err := harness.Handshake(getenv(validEnv()))
	require.NoError(t, err)

	p := harness.SystemPrompt(cfg, "/work/project", harness.Profile{Verify: "make test\n"})

	assert.Contains(t, p, "The harness verifies with `make test`, run in the checkout before the push")
}
