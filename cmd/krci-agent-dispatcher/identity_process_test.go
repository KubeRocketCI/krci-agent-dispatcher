package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/substratetest"
)

const (
	tokenOne  = "glpat-one-0d5e"
	tokenTwo  = "glpat-two-7f3a"
	hookToken = "hook-token-1b2c"
	userOne   = "project_1_bot_x"
	userTwo   = "project_2_bot_y"
)

// testbed is a GitLab fake whose token tokenOne is userOne and tokenTwo is userTwo, and a Substrate fake over gRPC,
// both over TLS with one test CA.
type testbed struct {
	gitlab    *httptest.Server
	substrate string
	ca        string
	tokenFile string
}

func newTestbed(t *testing.T) *testbed {
	t.Helper()

	gl := gitlabtest.NewFake(userOne)
	gl.AddIssue(models.IssueKey{ProjectID: 12, IID: 7}, "krci/test-go-app", models.IssueOpened)
	inner := gl.Handler(tokenOne)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == tokenTwo && r.URL.Path == "/api/v4/user" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"username": userTwo})

			return
		}

		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: srv.TLS.Certificates, MinVersion: tls.VersionTLS12,
	})))
	ateapi.RegisterControlServer(gs, substratetest.NewFake("krci-agents").Server())

	go func() { _ = gs.Serve(lis) }()

	t.Cleanup(gs.Stop)

	dir := t.TempDir()
	tb := &testbed{
		gitlab: srv, substrate: lis.Addr().String(),
		ca: filepath.Join(dir, "ca.crt"), tokenFile: filepath.Join(dir, "gitlab-token"),
	}
	require.NoError(t, os.WriteFile(tb.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE",
		Bytes: srv.Certificate().Raw}), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ate-token"), []byte("ate"), 0o600))
	tb.writeToken(t, tokenOne)

	return tb
}

func (tb *testbed) writeToken(t *testing.T, tok string) {
	t.Helper()
	require.NoError(t, os.WriteFile(tb.tokenFile, []byte(tok+"\n"), 0o600))
}

// env is the dispatcher env of the testbed with the HTTP port port.
func (tb *testbed) env(port int) []string {
	return []string{
		"GITLAB_URL=" + tb.gitlab.URL, "GITLAB_CA_FILE=" + tb.ca, "GITLAB_TOKEN_FILE=" + tb.tokenFile,
		"HOOK_TOKEN=" + hookToken, "ATESPACE=krci-agents", "ATE_API_ADDR=" + tb.substrate, "ATE_CA_FILE=" + tb.ca,
		"ATE_TOKEN_FILE=" + filepath.Join(filepath.Dir(tb.tokenFile), "ate-token"),
		"HARNESS_IMAGE=epamedp/krci-agent-harness@sha256:" + strings.Repeat("0", 64),
		"PORT=" + strconv.Itoa(port), "SWEEP_INTERVAL=0",
	}
}

func freePort(t *testing.T) int {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close())

	return port
}

// output is the goroutine-safe stdout and stderr of a process.
type output struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.b.Write(p)
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.b.String()
}

// startDispatcher starts the dispatcher process; it is killed at the end of the test. The channel gets its exit code.
func startDispatcher(t *testing.T, env []string) (*output, chan int) {
	t.Helper()

	out := &output{}

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(append(os.Environ(), processEnv+"=main", processArgs+"=", "GOCOVERDIR="+t.TempDir()), env...)
	cmd.Stdout, cmd.Stderr = out, out
	require.NoError(t, cmd.Start())

	done := make(chan int, 1)

	go func() {
		err := cmd.Wait()
		if err == nil {
			done <- 0

			return
		}

		done <- cmd.ProcessState.ExitCode()
	}()

	t.Cleanup(func() { _ = cmd.Process.Kill() })

	return out, done
}

// waitServing waits until the dispatcher at base answers /healthz and its restart recovery is queued.
func waitServing(t *testing.T, base string, out *output) {
	t.Helper()

	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/healthz", nil)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}

		_ = resp.Body.Close()

		return resp.StatusCode == http.StatusOK && strings.Contains(out.String(), "Restart recovery queued")
	}, 20*time.Second, 50*time.Millisecond, "the dispatcher did not start")
}

// postNoteHook posts a Note Hook of a comment by dev1 on issue 12/7.
func postNoteHook(t *testing.T, base string) {
	t.Helper()

	body := `{"user": {"username": "dev1"}, "project": {"id": 12}, "object_attributes": {"noteable_type": "Issue"},
		"issue": {"iid": 7, "labels": [{"title": "krci-agent"}]}}`

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/hook", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Gitlab-Event", "Note Hook")
	req.Header.Set("X-Gitlab-Token", hookToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func waitExit(t *testing.T, done chan int, out *output) int {
	t.Helper()

	select {
	case code := <-done:
		return code
	case <-time.After(20 * time.Second):
		t.Fatalf("the dispatcher did not exit; output %q", out.String())

		return 0
	}
}

// A token whose user cannot be read stops the dispatcher at start: exit 1, no hook served, no token in the output.
func TestProcessStartStopsWhenTheTokenUserCannotBeRead(t *testing.T) {
	tb := newTestbed(t)
	tb.writeToken(t, "glpat-revoked-9c4d")

	out, done := startDispatcher(t, tb.env(freePort(t)))

	assert.Equal(t, 1, waitExit(t, done, out))
	assert.Contains(t, out.String(), "read the bot user, the user of the GitLab token")
	assert.Contains(t, out.String(), "401")
	assert.NotContains(t, out.String(), "retrying", "a rejected token is not retried")
	assert.NotContains(t, out.String(), "glpat-revoked-9c4d")
	assert.NotContains(t, out.String(), "Restart recovery")
}

// A token file that changes to another user's token stops the dispatcher at its next GitLab call: exit 1, an error
// that names both users, no decision.
func TestProcessTokenOfAnotherUserStopsTheDispatcher(t *testing.T) {
	tb := newTestbed(t)
	port := freePort(t)
	out, done := startDispatcher(t, tb.env(port))
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	waitServing(t, base, out)
	assert.Contains(t, out.String(), `"bot-user":"`+userOne+`"`)

	tb.writeToken(t, tokenTwo)
	postNoteHook(t, base)

	assert.Equal(t, 1, waitExit(t, done, out))
	assert.Contains(t, out.String(), "bot user changed")
	assert.Contains(t, out.String(), userOne)
	assert.Contains(t, out.String(), userTwo)
	assert.NotContains(t, out.String(), `"rule"`, "no decision runs")
	assert.NotContains(t, out.String(), tokenOne)
	assert.NotContains(t, out.String(), tokenTwo)
	assert.NotContains(t, out.String(), hookToken)
}
