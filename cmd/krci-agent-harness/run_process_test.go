package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
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
		"HOME":                   "/home/agent",
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
			assert.Equal(t, 78, code)

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
	assert.Equal(t, 78, code)
	assert.Contains(t, logLine(t, stderr)["error"], "watchdog limit")
}

// gitlabEnv returns validEnv for a GitLab whose token user is user, served over TLS by the GitLab fake, with the
// CA file of the server in processCA. The fake has no issue: every poll fails and is retried.
func gitlabEnv(t *testing.T, user string) map[string]string {
	t.Helper()

	return tlsEnv(t, gitlabtest.NewFake(user).Handler(placeholderToken))
}

// tlsEnv returns validEnv for a GitLab served by h over TLS, with the CA file of the server in processCA.
func tlsEnv(t *testing.T, h http.Handler) map[string]string {
	t.Helper()

	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)

	ca := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}),
		0o600))

	env := validEnv()
	env["GITLAB_URL"], env["HOME"], env[processCA] = srv.URL, t.TempDir(), ca

	return env
}

// lastLogLine returns the last line of stderr as a JSON object without its time.
func lastLogLine(t *testing.T, stderr string) map[string]any {
	t.Helper()

	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")

	return logLine(t, lines[len(lines)-1])
}

// runProcessFor is runProcess bounded by 20 s: a harness that does not exit by then is killed and fails the test.
func runProcessFor(t *testing.T, env map[string]string) (int, string) {
	t.Helper()

	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = []string{processEnv + "=main", processArgs + "=run", "GOCOVERDIR=" + t.TempDir()}

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	cmd.Stderr = &stderr
	err := cmd.Run()

	require.NoError(t, ctx.Err(), "the harness did not exit; stderr %q", stderr.String())

	return exitCode(t, err), stderr.String()
}

// After the handshake the harness polls until SIGTERM, then exits 0.
func TestProcessRunPollsUntilTerminated(t *testing.T) {
	env := gitlabEnv(t, "krci-agent")

	cmd := harnessProcess(t, "main", "run")
	cmd.Env = []string{processEnv + "=main", processArgs + "=run", "GOCOVERDIR=" + t.TempDir()}

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stderr, err := cmd.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	polling := make(chan struct{})

	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"msg":"Cannot read the issue notes"`) {
				close(polling)

				break
			}
		}

		_, _ = io.Copy(io.Discard, stderr)
	}()

	select {
	case <-polling:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()

		t.Fatal("no poll log line")
	}

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	assert.Equal(t, 0, exitCode(t, cmd.Wait()))
}

// A token user other than BOT_USERNAME is a handshake failure: exit 78 before any turn.
func TestProcessRunExitsConfigWhenTheTokenUserIsNotTheBotUser(t *testing.T) {
	code, stderr := runProcessFor(t, gitlabEnv(t, "project_1_bot_x"))

	assert.Equal(t, 78, code)

	rec := lastLogLine(t, stderr)
	assert.Equal(t, "handshake failed", rec["msg"])
	assert.Equal(t, "BOT_USERNAME: is not the user of the GitLab token", rec["error"])
	assert.NotContains(t, stderr, "Turn started")
	assert.NotContains(t, stderr, placeholderToken)
}

// A token that GitLab rejects is a handshake failure: exit 78 before any turn, no retry; the log adds the cause.
func TestProcessRunExitsConfigWhenTheTokenIsRejected(t *testing.T) {
	code, stderr := runProcessFor(t, tlsEnv(t, gitlabtest.NewFake("krci-agent").Handler("another-token")))

	assert.Equal(t, 78, code)

	rec := lastLogLine(t, stderr)
	assert.Equal(t, "handshake failed", rec["msg"])
	assert.Contains(t, rec["error"], "BOT_USERNAME")
	assert.Contains(t, rec["cause"], "/user")
	assert.Contains(t, rec["cause"], "unauthorized")
	assert.NotContains(t, stderr, "retrying")
	assert.NotContains(t, stderr, "Turn started")
}

func TestProcessRunWithoutHomeExitsConfig(t *testing.T) {
	env := validEnv()
	delete(env, "HOME")

	code, stderr := runProcess(t, env)
	assert.Equal(t, 78, code)
	assert.Contains(t, logLine(t, stderr)["error"], "HOME")
}
