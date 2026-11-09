package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/api"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

const hookToken = "hook-secret-0123456789"

type recorder struct {
	mu   sync.Mutex
	reqs []intake.Request
}

func (r *recorder) Handle(req intake.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reqs = append(r.reqs, req)
}

func (r *recorder) got() []intake.Request {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]intake.Request(nil), r.reqs...)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("..", "services", "intake", "testdata", name))
	require.NoError(t, err)

	return b
}

func newServer(t *testing.T, logs *bytes.Buffer) (*httptest.Server, *recorder) {
	t.Helper()

	rec := &recorder{}
	h := api.NewRouter(rec, api.RouterConfig{
		HookToken: hookToken, Bot: "krci-agent", TriggerLabel: "agent", Logger: api.NewLogger(logs),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv, rec
}

func post(t *testing.T, srv *httptest.Server, event, token string, body []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/hook", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	if event != "" {
		req.Header.Set("X-Gitlab-Event", event)
	}

	if token != "" {
		req.Header.Set("X-Gitlab-Token", token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

func TestHookAccepted(t *testing.T) {
	srv, rec := newServer(t, &bytes.Buffer{})

	resp := post(t, srv, "Issue Hook", hookToken, fixture(t, "issue-open.json"))
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var body map[string]string

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, map[string]string{"kind": "start"}, body)

	want := intake.Request{Key: models.IssueKey{ProjectID: 12, IID: 7}, Kind: intake.Start, Author: "dev1"}
	assert.Equal(t, []intake.Request{want}, rec.got())

	resp = post(t, srv, "Note Hook", hookToken, fixture(t, "note-issue.json"))
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Len(t, rec.got(), 2)
}

func TestHookIgnoredEventAccepted(t *testing.T) {
	srv, rec := newServer(t, &bytes.Buffer{})

	for _, c := range []struct{ event, fixture string }{
		{"Push Hook", "issue-open.json"},
		{"Note Hook", "note-mr.json"},
		{"Note Hook", "note-bot.json"},
		{"Issue Hook", "issue-reopen-no-label.json"},
	} {
		resp := post(t, srv, c.event, hookToken, fixture(t, c.fixture))
		assert.Equal(t, http.StatusAccepted, resp.StatusCode, c)

		var body map[string]string

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "ignore", body["kind"], c)
	}

	assert.Empty(t, rec.got(), "ignored events reach no lane")
}

func TestHookBadInput(t *testing.T) {
	big := append([]byte(`{"object_kind":"issue","pad":"`), bytes.Repeat([]byte("a"), 1<<20)...)
	big = append(big, []byte(`"}`)...)

	cases := []struct {
		name   string
		token  string
		body   []byte
		status int
	}{
		{"body not JSON", hookToken, []byte("not json"), http.StatusBadRequest},
		{"body empty", hookToken, nil, http.StatusBadRequest},
		{"body a JSON array", hookToken, []byte(`[1,2]`), http.StatusBadRequest},
		{"body over 1 MiB", hookToken, big, http.StatusBadRequest},
		{"token missing", "", fixture(t, "issue-open.json"), http.StatusUnauthorized},
		{"token wrong", "hook-secret-wrong", fixture(t, "issue-open.json"), http.StatusUnauthorized},
		{"token wrong, body bad", "hook-secret-wrong", []byte("not json"), http.StatusUnauthorized},
	}

	srv, rec := newServer(t, &bytes.Buffer{})

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := post(t, srv, "Issue Hook", c.token, c.body)
			assert.Equal(t, c.status, resp.StatusCode)
			assert.Equal(t, "application/problem+json", resp.Header.Get("Content-Type"))

			var p map[string]any

			require.NoError(t, json.NewDecoder(resp.Body).Decode(&p))
			assert.InDelta(t, float64(c.status), p["status"], 0)
		})
	}

	assert.Empty(t, rec.got(), "no Handle call for bad input")
}

func TestHookTokenConstantTime(t *testing.T) {
	srv, rec := newServer(t, &bytes.Buffer{})

	for _, tok := range []string{
		hookToken[:len(hookToken)-1],
		hookToken + "x",
		strings.ToUpper(hookToken),
		strings.Replace(hookToken, "0", "1", 1),
	} {
		resp := post(t, srv, "Issue Hook", tok, fixture(t, "issue-open.json"))
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, tok)
	}

	assert.Empty(t, rec.got())
	assert.Equal(t, http.StatusAccepted, post(t, srv, "Issue Hook", hookToken, fixture(t, "issue-open.json")).StatusCode)
}

func TestHookNeverLogsToken(t *testing.T) {
	logs := &bytes.Buffer{}
	srv, _ := newServer(t, logs)

	post(t, srv, "Issue Hook", hookToken, fixture(t, "issue-open.json"))
	post(t, srv, "Issue Hook", "hook-secret-wrong-guess", fixture(t, "issue-open.json"))
	post(t, srv, "Issue Hook", hookToken, []byte("not json"))

	out := logs.String()
	assert.Contains(t, out, "/hook", "the request log lines are there")
	assert.NotContains(t, out, hookToken)
	assert.NotContains(t, out, "hook-secret-wrong-guess")
}

func TestHealthz(t *testing.T) {
	srv, _ := newServer(t, &bytes.Buffer{})

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestNewRouterRequiresToken(t *testing.T) {
	assert.Panics(t, func() { api.NewRouter(&recorder{}, api.RouterConfig{Logger: api.NewLogger(&bytes.Buffer{})}) })
}
