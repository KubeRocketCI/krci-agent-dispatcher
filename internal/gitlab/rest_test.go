package gitlab_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

func TestTokenHeaderNotInURL(t *testing.T) {
	f := gitlabtest.NewFake(bot)
	f.AddIssue(key, "krci/test-go-app", models.IssueOpened, agentLabels.Trigger())

	var (
		mu      sync.Mutex
		urls    []string
		headers []string
	)

	inner := f.Handler(token)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		urls = append(urls, r.URL.String())
		headers = append(headers, r.Header.Get("PRIVATE-TOKEN"))
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := gitlab.NewREST(srv.URL, token, "", bot, models.Developer)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = c.Load(ctx, key)
	require.NoError(t, err)
	require.NoError(t, c.Write(ctx, key, nil, models.Writes{Record: &models.Record{State: models.Waiting},
		Label: models.Waiting, Notes: []models.NewNote{{Body: "hi"}}}))

	_, err = c.CanTrigger(ctx, 12, "dev")
	require.NoError(t, err)

	require.NotEmpty(t, urls)

	for i := range urls {
		assert.NotContains(t, urls[i], token)
		assert.Equal(t, token, headers[i])
	}
}

func TestRESTErrorHasNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c, err := gitlab.NewREST(srv.URL, token, "", bot, models.Developer)
	require.NoError(t, err)

	_, err = c.Load(context.Background(), key)
	require.Error(t, err)
	require.NotErrorIs(t, err, models.ErrNotFound)
	assert.NotContains(t, err.Error(), token)

	_, err = c.CanTrigger(context.Background(), 12, "dev")
	require.Error(t, err, "a lookup error is an error, not false")
	assert.NotContains(t, err.Error(), token)
}

func TestRESTTrustsCAFile(t *testing.T) {
	f := gitlabtest.NewFake(bot)
	f.AddIssue(key, "krci/test-go-app", models.IssueOpened)

	srv := httptest.NewTLSServer(f.Handler(token))
	t.Cleanup(srv.Close)

	untrusted, err := gitlab.NewREST(srv.URL, token, "", bot, models.Developer)
	require.NoError(t, err)
	_, err = untrusted.Load(context.Background(), key)
	require.Error(t, err)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))

	trusted, err := gitlab.NewREST(srv.URL, token, caFile, bot, models.Developer)
	require.NoError(t, err)
	_, err = trusted.Load(context.Background(), key)
	require.NoError(t, err)
}

func TestRESTBadCAFile(t *testing.T) {
	dir := t.TempDir()

	_, err := gitlab.NewREST("https://gitlab.example", token, filepath.Join(dir, "missing.crt"), bot, models.Developer)
	require.Error(t, err)

	bad := filepath.Join(dir, "bad.crt")
	require.NoError(t, os.WriteFile(bad, []byte("not a pem"), 0o600))
	_, err = gitlab.NewREST("https://gitlab.example", token, bad, bot, models.Developer)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), token))
}

func TestTokenFileReadPerRequest(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("PRIVATE-TOKEN"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte("glpat-one\n"), 0o600))

	c, err := gitlab.NewREST(srv.URL, "", "", bot, models.Developer, gitlab.WithTokenFile(file))
	require.NoError(t, err)

	_, err = c.CanTrigger(context.Background(), 12, "dev")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("glpat-two"), 0o600))

	_, err = c.CanTrigger(context.Background(), 12, "dev")
	require.NoError(t, err)

	assert.Equal(t, []string{"glpat-one", "glpat-two"}, seen)

	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err = c.CanTrigger(context.Background(), 12, "dev")
	require.Error(t, err, "an empty token file is an error")
}

// countingREST returns a REST adapter over f and the log of the requests it sent, as "<method> <path>".
func countingREST(t *testing.T, f *gitlabtest.Fake) (*gitlab.REST, func() []string) {
	t.Helper()

	var (
		mu   sync.Mutex
		reqs []string
	)

	inner := f.Handler(token)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/api/v4/projects/12/issues/7"))
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := gitlab.NewREST(srv.URL, token, "", bot, models.Developer)
	require.NoError(t, err)

	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()

		out := reqs
		reqs = nil

		return out
	}
}

func longIssue(t *testing.T) *gitlabtest.Fake {
	t.Helper()

	f := gitlabtest.NewFake(bot)
	f.AddIssue(key, "krci/test-go-app", models.IssueOpened, agentLabels.Trigger())
	f.AddNote(key, models.Note{Author: bot, Body: models.StatusBody(models.Record{State: models.Working},
		models.StatusView{Session: "s1"})})

	for range 499 {
		f.AddNote(key, models.Note{Body: "comment", Author: "owner"})
	}

	return f
}

func TestRefreshRequestsDoNotGrowWithNotes(t *testing.T) {
	f := longIssue(t)
	c, requests := countingREST(t, f)
	ctx := context.Background()

	prev, err := c.Load(ctx, key)
	require.NoError(t, err)
	assert.Len(t, requests(), 6, "a full read: the issue and five pages of notes")

	got, err := c.Refresh(ctx, prev)
	require.NoError(t, err)
	assert.Equal(t, prev, got)
	assert.Equal(t, []string{"GET ", "GET /notes"}, requests())

	f.AddNote(key, models.Note{Body: "<!-- krci-agent-turn s=s1 n=1 exit=0 last=1 -->", Author: bot})

	got, err = c.Refresh(ctx, got)
	require.NoError(t, err)
	assert.Len(t, got.Notes, 501)
	assert.Equal(t, []string{"GET ", "GET /notes"}, requests())
}

func TestWriteWithKnownViewReadsNoNotes(t *testing.T) {
	f := longIssue(t)
	c, requests := countingREST(t, f)
	ctx := context.Background()
	w := models.Writes{Record: &models.Record{State: models.Waiting}, Label: models.Waiting}

	require.NoError(t, c.Write(ctx, key, nil, w))
	assert.Len(t, requests(), 7, "without a known view: five pages of notes, the status note, the label")

	known, err := c.Load(ctx, key)
	require.NoError(t, err)
	requests()

	require.NoError(t, c.Write(ctx, key, &known, w))
	assert.Equal(t, []string{"PUT /notes/101", "PUT "}, requests())

	require.NoError(t, c.Write(ctx, key, nil, models.Writes{Notes: []models.NewNote{{Body: models.NoRoomText}}}))
	assert.Equal(t, []string{"POST /notes"}, requests(), "a note without a marker needs no read")
}

func TestRESTForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	var logs bytes.Buffer

	c, err := gitlab.NewREST(srv.URL, token, "", bot, models.Developer,
		gitlab.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	require.NoError(t, err)

	err = c.CheckProject(context.Background(), 12)
	require.ErrorIs(t, err, models.ErrForbidden)
	assert.NotContains(t, err.Error(), token)

	ok, err := c.CanTrigger(context.Background(), 12, "dev")
	require.NoError(t, err)
	assert.False(t, ok, "members the token cannot see mean false")
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), "Project members not shown to the bot user")
	assert.Contains(t, logs.String(), "forbidden")
	assert.NotContains(t, logs.String(), token)
}
