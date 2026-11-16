package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const (
	testDigest  = "sha256:827f0a832eaa27cf150c20cc8c54976d33e597fb9de82332fc33ccb8699e4df3"
	testHarness = "docker.io/epamedp/krci-agent-harness:0.1.0@" + testDigest
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("GITLAB_URL", "https://gitlab.example")
	t.Setenv("GITLAB_TOKEN", "glpat-test")
	t.Setenv("HOOK_TOKEN", "hook-test")
	t.Setenv("HARNESS_IMAGE", testHarness)
	t.Setenv("ATESPACE", "krci-agents")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "8080", cfg.Port)
	assert.Equal(t, 15*time.Minute, cfg.TurnTimeout)
	assert.Equal(t, 10*time.Second, cfg.PollInterval)
	assert.Equal(t, 5*time.Minute, cfg.SweepInterval)
	assert.Equal(t, 20, cfg.TurnLimit)
	assert.Equal(t, "krci-agents", cfg.Atespace)
	assert.Equal(t, "workers", cfg.Pool)
	assert.Equal(t, "krci-agents", cfg.SecretsNamespace)
	assert.Equal(t, "krci-agent", cfg.BotUsername)
	assert.Equal(t, models.Developer, cfg.TriggerMinRole)
	assert.Equal(t, "api.ate-system.svc:443", cfg.ATEAPIAddr)
	assert.Equal(t, "/var/run/ate/token", cfg.ATETokenFile)
	assert.Equal(t, "/var/run/ate/ca.crt", cfg.ATECAFile)
	assert.Empty(t, cfg.GitLabCAFile)
	assert.Equal(t, "https://gitlab.example", cfg.GitLabURL.String())
	assert.Equal(t, "glpat-test", cfg.GitLabToken)
	assert.Equal(t, "hook-test", cfg.HookToken)
	assert.Equal(t, "/etc/krci-agent-dispatcher/profiles.yaml", cfg.ProfilesFile)
	assert.Equal(t, testHarness, cfg.HarnessImage)
	assert.Equal(t, 40, cfg.AgentSteps)
	assert.Equal(t, "claude-haiku-5-5", cfg.AgentModel)
	assert.Equal(t, 15*time.Second, cfg.HarnessPollInterval)
}

func TestLoadOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "9090")
	t.Setenv("TURN_TIMEOUT", "3m")
	t.Setenv("TURN_LIMIT", "3")
	t.Setenv("AGENT_STEPS", "12")
	t.Setenv("AGENT_MODEL", "claude-sonnet-5-5")
	t.Setenv("HARNESS_POLL_INTERVAL", "30s")
	t.Setenv("GITLAB_CA_FILE", "/etc/gitlab-ca/ca.crt")
	t.Setenv("SWEEP_INTERVAL", "0")
	t.Setenv("PROFILES_FILE", "/etc/profiles/profiles.yaml")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.Port)
	assert.Equal(t, 3*time.Minute, cfg.TurnTimeout)
	assert.Equal(t, 3, cfg.TurnLimit)
	assert.Equal(t, 12, cfg.AgentSteps)
	assert.Equal(t, "claude-sonnet-5-5", cfg.AgentModel)
	assert.Equal(t, 30*time.Second, cfg.HarnessPollInterval)
	assert.Equal(t, "/etc/gitlab-ca/ca.crt", cfg.GitLabCAFile)
	assert.Zero(t, cfg.SweepInterval, "0 turns the sweep off")
	assert.Equal(t, "/etc/profiles/profiles.yaml", cfg.ProfilesFile)
}

func TestLoadMissingRequired(t *testing.T) {
	for _, name := range []string{"GITLAB_URL", "HOOK_TOKEN", "HARNESS_IMAGE", "ATESPACE"} {
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
	for _, name := range []string{"TURN_TIMEOUT", "POLL_INTERVAL", "SWEEP_INTERVAL", "HARNESS_POLL_INTERVAL"} {
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

	t.Run("negative sweep interval", func(t *testing.T) {
		setRequired(t)
		t.Setenv("SWEEP_INTERVAL", "-1m")

		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SWEEP_INTERVAL must be a duration of 0 or more")
	})
}

func TestLoadBadInt(t *testing.T) {
	for _, name := range []string{"TURN_LIMIT", "AGENT_STEPS"} {
		for _, v := range []string{"many", "0", "-1"} {
			t.Run(name+"="+v, func(t *testing.T) {
				setRequired(t)
				t.Setenv(name, v)

				_, err := Load()
				require.Error(t, err)
				assert.Contains(t, err.Error(), name)
			})
		}
	}
}

func TestLoadRefusesAHarnessImageNotPinnedByDigest(t *testing.T) {
	for _, v := range []string{"docker.io/epamedp/krci-agent-harness:0.1.0", "krci-agent-harness@sha256:abc"} {
		t.Run(v, func(t *testing.T) {
			setRequired(t)
			t.Setenv("HARNESS_IMAGE", v)

			_, err := Load()
			require.ErrorContains(t, err, "HARNESS_IMAGE must be pinned by digest")
		})
	}
}

// The harness watchdog limit is the turn timeout minus the harness poll interval minus 60 s, at least 60 s.
func TestLoadRefusesATurnTimeoutBelowHarnessPollPlusTwoMinutes(t *testing.T) {
	setRequired(t)
	t.Setenv("HARNESS_POLL_INTERVAL", "20s")
	t.Setenv("TURN_TIMEOUT", "2m19s")

	_, err := Load()
	require.ErrorContains(t, err, "TURN_TIMEOUT must be at least HARNESS_POLL_INTERVAL plus 2m")

	t.Setenv("TURN_TIMEOUT", "2m20s")

	_, err = Load()
	require.NoError(t, err)
}

func TestLoadRefusesSecondsFractions(t *testing.T) {
	for _, name := range []string{"TURN_TIMEOUT", "HARNESS_POLL_INTERVAL"} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(name, "20500ms")

			_, err := Load()
			require.ErrorContains(t, err, name+" must be whole seconds")
		})
	}
}

func TestLoadErrorHasNoSecret(t *testing.T) {
	setRequired(t)
	t.Setenv("ATESPACE", "")

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

func TestLoadTriggerMinRole(t *testing.T) {
	for name, want := range map[string]models.Role{
		"developer": models.Developer, "maintainer": models.Maintainer, "owner": models.Owner,
		"Developer": models.Developer, "Maintainer": models.Maintainer, " maintainer ": models.Maintainer,
	} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("TRIGGER_MIN_ROLE", name)

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, want, cfg.TriggerMinRole)
		})
	}
}

func TestLoadRejectsTriggerMinRole(t *testing.T) {
	for _, v := range []string{"guest", "reporter", "Reporter", "admin", "30"} {
		t.Run(v, func(t *testing.T) {
			setRequired(t)
			t.Setenv("TRIGGER_MIN_ROLE", v)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "TRIGGER_MIN_ROLE must be one of developer, maintainer, owner")
		})
	}
}
