package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("GITLAB_URL", "https://gitlab.example")
	t.Setenv("GITLAB_TOKEN", "glpat-test")
	t.Setenv("HOOK_TOKEN", "hook-test")
	t.Setenv("AGENT_IMAGE", "localhost:5001/krci-agent-go@sha256:0")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "8080", cfg.Port)
	assert.Equal(t, 15*time.Minute, cfg.TurnTimeout)
	assert.Equal(t, 10*time.Second, cfg.PollInterval)
	assert.Equal(t, 20, cfg.MaxTurnsPerIssue)
	assert.Equal(t, "krci-agents", cfg.Atespace)
	assert.Equal(t, "workers", cfg.Pool)
	assert.Equal(t, "krci-agents", cfg.SecretsNamespace)
	assert.Equal(t, "krci-agent", cfg.BotUsername)
	assert.Equal(t, "agent", cfg.TriggerLabel)
	assert.Equal(t, "api.ate-system.svc:443", cfg.ATEAPIAddr)
	assert.Equal(t, "/var/run/ate/token", cfg.ATETokenFile)
	assert.Equal(t, "/var/run/ate/ca.crt", cfg.ATECAFile)
	assert.Empty(t, cfg.GitLabCAFile)
	assert.Equal(t, "https://gitlab.example", cfg.GitLabURL)
	assert.Equal(t, "glpat-test", cfg.GitLabToken)
	assert.Equal(t, "hook-test", cfg.HookToken)
	assert.Equal(t, "localhost:5001/krci-agent-go@sha256:0", cfg.AgentImage)
}

func TestLoadOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "9090")
	t.Setenv("TURN_TIMEOUT", "2m")
	t.Setenv("MAX_TURNS_PER_ISSUE", "3")
	t.Setenv("GITLAB_CA_FILE", "/etc/gitlab-ca/ca.crt")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.Port)
	assert.Equal(t, 2*time.Minute, cfg.TurnTimeout)
	assert.Equal(t, 3, cfg.MaxTurnsPerIssue)
	assert.Equal(t, "/etc/gitlab-ca/ca.crt", cfg.GitLabCAFile)
}

func TestLoadMissingRequired(t *testing.T) {
	for _, name := range []string{"GITLAB_URL", "HOOK_TOKEN", "AGENT_IMAGE"} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(name, "")

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
}

func TestLoadBadDuration(t *testing.T) {
	for _, name := range []string{"TURN_TIMEOUT", "POLL_INTERVAL"} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(name, "soon")

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}

	t.Run("not positive", func(t *testing.T) {
		setRequired(t)
		t.Setenv("POLL_INTERVAL", "0s")

		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "POLL_INTERVAL")
	})
}

func TestLoadBadInt(t *testing.T) {
	for _, v := range []string{"many", "0", "-1"} {
		t.Run(v, func(t *testing.T) {
			setRequired(t)
			t.Setenv("MAX_TURNS_PER_ISSUE", v)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "MAX_TURNS_PER_ISSUE")
		})
	}
}

func TestLoadErrorHasNoSecret(t *testing.T) {
	setRequired(t)
	t.Setenv("AGENT_IMAGE", "")

	_, err := Load()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-test")
	assert.NotContains(t, err.Error(), "hook-test")
}

func TestLoadTokenFile(t *testing.T) {
	setRequired(t)
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN_FILE", "/var/run/gitlab/token")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "/var/run/gitlab/token", cfg.GitLabTokenFile)
	assert.Empty(t, cfg.GitLabToken)

	t.Setenv("GITLAB_TOKEN_FILE", "")

	_, err = Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITLAB_TOKEN or GITLAB_TOKEN_FILE")
}
