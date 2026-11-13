package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validEnv() map[string]string {
	return map[string]string{
		"GITLAB_URL":             "https://gitlab.example.com",
		"PROJECT_ID":             "12",
		"ISSUE_IID":              "7",
		"PROJECT_PATH":           "group/project",
		"BOT_USERNAME":           "krci-agent",
		"SESSION_ID":             "session-1",
		"AGENT_CONTRACT_VERSION": "1",
		"TURN_TIMEOUT_SECONDS":   "900",
		"POLL_SECONDS":           "10",
		"TURN_LIMIT":             "5",
		"AGENT_STEPS":            "40",
		"ANTHROPIC_MODEL":        "model-x",
	}
}

// runProcess runs `krci-agent-harness run` as a process with exactly env as its environment.
func runProcess(t *testing.T, env map[string]string) (int, string) {
	t.Helper()

	var stderr bytes.Buffer

	cmd := harnessProcess(t, "main", "run")
	// An instrumented test binary warns on stderr at exit when GOCOVERDIR is unset.
	cmd.Env = []string{processEnv + "=main", processArgs + "=run", "GOCOVERDIR=" + t.TempDir()}

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	cmd.Stderr = &stderr

	return exitCode(t, cmd.Run()), stderr.String()
}

// logLine returns the only line of stderr as a JSON object without its time.
func logLine(t *testing.T, stderr string) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	require.Len(t, lines, 1, "exactly one log line: %q", stderr)

	var rec map[string]any

	require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec), "JSON log line")
	delete(rec, "time")

	return rec
}

func TestProcessRunHandshakeFailureExitsConfig(t *testing.T) {
	const leak = "leak-0d5e"

	tests := []struct{ item, value string }{
		{item: "GITLAB_URL", value: "http://" + leak},
		{item: "PROJECT_ID", value: leak},
		{item: "ISSUE_IID", value: leak},
		{item: "PROJECT_PATH", value: ""},
		{item: "BOT_USERNAME", value: ""},
		{item: "SESSION_ID", value: "s_" + leak},
		{item: "AGENT_CONTRACT_VERSION", value: leak},
		{item: "TURN_TIMEOUT_SECONDS", value: leak},
		{item: "POLL_SECONDS", value: leak},
		{item: "TURN_LIMIT", value: leak},
		{item: "AGENT_STEPS", value: leak},
		{item: "ANTHROPIC_MODEL", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.item, func(t *testing.T) {
			env := validEnv()
			env[tt.item] = tt.value

			code, stderr := runProcess(t, env)
			assert.Equal(t, exitConfig, code)

			rec := logLine(t, stderr)
			assert.Contains(t, rec["error"], tt.item)
			assert.NotContains(t, stderr, leak)
		})
	}
}

func TestProcessRunRefusesAWatchdogLimitBelowOneMinute(t *testing.T) {
	env := validEnv()
	env["TURN_TIMEOUT_SECONDS"], env["POLL_SECONDS"] = "129", "10"

	code, stderr := runProcess(t, env)
	assert.Equal(t, exitConfig, code)
	assert.Contains(t, logLine(t, stderr)["error"], "watchdog limit")
}

func TestProcessRunAfterTheHandshakeExitsUnavailable(t *testing.T) {
	env := validEnv()
	env["HOME"] = t.TempDir()

	code, stderr := runProcess(t, env)
	assert.Equal(t, exitUnavailable, code)
	assert.Equal(t, "agent and workspace adapters not built", logLine(t, stderr)["msg"])
}

func TestProcessRunWithoutHomeExitsConfig(t *testing.T) {
	code, stderr := runProcess(t, validEnv())
	assert.Equal(t, exitConfig, code)
	assert.Contains(t, logLine(t, stderr)["error"], "HOME")
}
