// Package config reads the dispatcher configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

// Config holds the resolved dispatcher settings.
type Config struct {
	Port             string
	GitLabURL        models.GitLabURL
	GitLabToken      string
	GitLabTokenFile  string
	GitLabCAFile     string
	HookToken        string
	BotUsername      string
	TriggerMinRole   models.Role
	ATEAPIAddr       string
	ATETokenFile     string
	ATECAFile        string
	Atespace         string
	WorkerSelector   models.WorkerSelector
	SecretsNamespace string
	ProfilesFile     string
	HarnessImage     string
	AgentSteps       int
	AgentModel       string
	TurnLimit        int
	TurnTimeout      time.Duration
	PollInterval     time.Duration
	// HarnessPollInterval is the poll interval of the harness in the actor; PollInterval is the dispatcher's.
	HarnessPollInterval time.Duration
	SweepInterval       time.Duration
}

// Load reads the environment, applies defaults and validates the values.
// Errors name the variable, never its value.
func Load() (*Config, error) {
	var errs []error

	required := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}

		return v
	}

	duration := func(name string, def time.Duration, allowZero bool) time.Duration {
		d, err := envDuration(name, def, allowZero)
		if err != nil {
			errs = append(errs, err)
		}

		return d
	}

	positiveInt := func(name string, def int) int {
		v := os.Getenv(name)
		if v == "" {
			return def
		}

		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive integer", name))
		}

		return n
	}

	role := func(name, def string) models.Role {
		r, err := models.ParseRole(orDefault(name, def))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %w", name, err))
		}

		return r
	}

	workerSelector := func(name, def string) models.WorkerSelector {
		s, err := models.ParseWorkerSelector(orDefault(name, def))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %w", name, err))
		}

		return s
	}

	gitLabURL := func(name string) models.GitLabURL {
		v := required(name)
		if v == "" {
			return models.GitLabURL{}
		}

		u, err := models.ParseGitLabURL(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %w", name, err))
		}

		return u
	}

	cfg := &Config{
		Port:             orDefault("PORT", "8080"),
		GitLabURL:        gitLabURL("GITLAB_URL"),
		GitLabToken:      os.Getenv("GITLAB_TOKEN"),
		GitLabTokenFile:  os.Getenv("GITLAB_TOKEN_FILE"),
		GitLabCAFile:     os.Getenv("GITLAB_CA_FILE"),
		HookToken:        required("HOOK_TOKEN"),
		BotUsername:      orDefault("BOT_USERNAME", "krci-agent"),
		TriggerMinRole:   role("TRIGGER_MIN_ROLE", "developer"),
		ATEAPIAddr:       orDefault("ATE_API_ADDR", "api.ate-system.svc:443"),
		ATETokenFile:     orDefault("ATE_TOKEN_FILE", "/var/run/ate/token"),
		ATECAFile:        orDefault("ATE_CA_FILE", "/var/run/ate/ca.crt"),
		Atespace:         required("ATESPACE"),
		WorkerSelector:   workerSelector("WORKER_SELECTOR", "workload=workers"),
		SecretsNamespace: orDefault("SECRETS_NAMESPACE", "krci-agents"),
		ProfilesFile:     orDefault("PROFILES_FILE", "/etc/krci-agent-dispatcher/profiles.yaml"),
		HarnessImage:     required("HARNESS_IMAGE"),
		AgentSteps:       positiveInt("AGENT_STEPS", 40),
		AgentModel:       orDefault("AGENT_MODEL", "claude-haiku-5-5"),
		TurnLimit:        positiveInt("TURN_LIMIT", 20),
		TurnTimeout:      duration("TURN_TIMEOUT", 15*time.Minute, false),
		PollInterval:     duration("POLL_INTERVAL", 10*time.Second, false),

		HarnessPollInterval: duration("HARNESS_POLL_INTERVAL", 15*time.Second, false),
		SweepInterval:       duration("SWEEP_INTERVAL", 5*time.Minute, true),
	}

	if cfg.GitLabToken == "" && cfg.GitLabTokenFile == "" {
		errs = append(errs, errors.New("GITLAB_TOKEN or GITLAB_TOKEN_FILE is required"))
	}

	errs = append(errs, cfg.harnessErrors()...)

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %w", errors.Join(errs...))
	}

	return cfg, nil
}

// harnessErrors checks the values the actor template passes to the harness: they must pass its handshake.
func (c *Config) harnessErrors() []error {
	var errs []error

	if c.HarnessImage != "" && !models.DigestPinned(c.HarnessImage) {
		errs = append(errs, errors.New("HARNESS_IMAGE must be pinned by digest: <repo>[:<tag>]@sha256:<digest>"))
	}

	for _, w := range []struct {
		name string
		d    time.Duration
	}{{"TURN_TIMEOUT", c.TurnTimeout}, {"HARNESS_POLL_INTERVAL", c.HarnessPollInterval}} {
		if w.d%time.Second != 0 {
			errs = append(errs, fmt.Errorf("%s must be whole seconds", w.name))
		}
	}

	if c.TurnTimeout > 0 && c.HarnessPollInterval > 0 && c.TurnTimeout < turn.MinTurnTimeout(c.HarnessPollInterval) {
		errs = append(errs, fmt.Errorf("TURN_TIMEOUT must be at least HARNESS_POLL_INTERVAL plus %s",
			turn.MinTurnTimeout(0)))
	}

	return errs
}

// envDuration reads the duration in name; empty is def. A duration is positive, or 0 or more with allowZero.
func envDuration(name string, def time.Duration, allowZero bool) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}

	d, err := time.ParseDuration(v)
	if err == nil && (d > 0 || d == 0 && allowZero) {
		return d, nil
	}

	if allowZero {
		return d, fmt.Errorf("%s must be a duration of 0 or more", name)
	}

	return d, fmt.Errorf("%s must be a positive duration", name)
}

func orDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}

	return def
}
