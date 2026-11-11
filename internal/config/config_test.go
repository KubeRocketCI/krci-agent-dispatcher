package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDigest = "sha256:827f0a832eaa27cf150c20cc8c54976d33e597fb9de82332fc33ccb8699e4df3"
	testImage  = "localhost:5001/krci-agent-go:dev@" + testDigest
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("GITLAB_URL", "https://gitlab.example")
	t.Setenv("GITLAB_TOKEN", "glpat-test")
	t.Setenv("HOOK_TOKEN", "hook-test")
	t.Setenv("AGENT_IMAGE", testImage)
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "8080", cfg.Port)
	assert.Equal(t, 15*time.Minute, cfg.TurnTimeout)
	assert.Equal(t, 10*time.Second, cfg.PollInterval)
	assert.Equal(t, 20, cfg.TurnLimit)
	assert.Equal(t, "krci-agents", cfg.Atespace)
	assert.Equal(t, "workers", cfg.Pool)
	assert.Equal(t, "krci-agents", cfg.SecretsNamespace)
	assert.Equal(t, "krci-agent", cfg.BotUsername)
	assert.Equal(t, "agent", cfg.TriggerLabel)
	assert.Equal(t, "api.ate-system.svc:443", cfg.ATEAPIAddr)
	assert.Equal(t, "/var/run/ate/token", cfg.ATETokenFile)
	assert.Equal(t, "/var/run/ate/ca.crt", cfg.ATECAFile)
	assert.Empty(t, cfg.GitLabCAFile)
	assert.Equal(t, "https://gitlab.example", cfg.GitLabURL.String())
	assert.Equal(t, "glpat-test", cfg.GitLabToken)
	assert.Equal(t, "hook-test", cfg.HookToken)
	assert.Equal(t, testImage, cfg.AgentImage)
}

func TestLoadOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "9090")
	t.Setenv("TURN_TIMEOUT", "2m")
	t.Setenv("TURN_LIMIT", "3")
	t.Setenv("GITLAB_CA_FILE", "/etc/gitlab-ca/ca.crt")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.Port)
	assert.Equal(t, 2*time.Minute, cfg.TurnTimeout)
	assert.Equal(t, 3, cfg.TurnLimit)
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
			t.Setenv("TURN_LIMIT", v)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "TURN_LIMIT")
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

func TestLoadRejectsBadGitLabURL(t *testing.T) {
	for _, v := range []string{
		"http://gitlab.internal",
		"gitlab.example",
		"https://",
		"https:///gitlab",
		"https://gitlab.example:",
		"https://bot:hunter2@gitlab.example",
		"https://gitlab.example?private_token=hunter2",
		"https://gitlab.example#hunter2",
		"https://gitlab.example/a%2Fb",
		" https://gitlab.example",
		"not a url",
	} {
		t.Run(v, func(t *testing.T) {
			setRequired(t)
			t.Setenv("GITLAB_URL", v)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "GITLAB_URL must be https://<host>")
			assert.NotContains(t, err.Error(), "hunter2")
		})
	}
}

func TestLoadCanonicalGitLabURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://gitlab.example":           "https://gitlab.example",
		"https://gitlab.example/":          "https://gitlab.example",
		"https://gitlab.example//":         "https://gitlab.example",
		"https://gitlab.example/gitlab":    "https://gitlab.example/gitlab",
		"https://gitlab.example/gitlab/":   "https://gitlab.example/gitlab",
		"https://gitlab.example:8443/a/b/": "https://gitlab.example:8443/a/b",
		"HTTPS://GitLab.Example/GitLab/":   "https://gitlab.example/GitLab",
	} {
		t.Run(in, func(t *testing.T) {
			setRequired(t)
			t.Setenv("GITLAB_URL", in)

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, want, cfg.GitLabURL.String())
		})
	}
}

func TestAgentImagePinned(t *testing.T) {
	for v, want := range map[string]bool{
		"epamedp/krci-agent-go@" + testDigest:     true,
		"epamedp/krci-agent-go:dev@" + testDigest: true,
		"localhost:5001/a/b:1.0@" + testDigest:    true,
		"epamedp/krci-agent-go:dev":               false,
		"epamedp/krci-agent-go@sha256:827f0a83":   false,
		"epamedp/krci-agent-go@sha256:827F0A832EAA27CF150C20CC8C54976D33E597FB9DE82332FC33CCB8699E4DF3": false,
		"epamedp/krci-agent-go@sha512:" + testDigest[len("sha256:"):]:                                   false,
		"epamedp/krci-agent-go @" + testDigest:                                                          false,
		"epamedp/krci-agent-go@" + testDigest + "\n":                                                    false,
	} {
		t.Run(v, func(t *testing.T) {
			setRequired(t)
			t.Setenv("AGENT_IMAGE", v)

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, want, cfg.AgentImagePinned())
		})
	}
}
