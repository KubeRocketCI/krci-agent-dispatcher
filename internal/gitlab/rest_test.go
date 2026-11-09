package gitlab_test

import (
	"context"
	"encoding/pem"
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
	f.AddIssue(key, "krci/test-go-app", models.IssueOpened, "agent")

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

	c, err := gitlab.NewREST(srv.URL, token, "", bot)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = c.Load(ctx, key)
	require.NoError(t, err)
	require.NoError(t, c.Write(ctx, key, models.Writes{Record: &models.Record{State: models.Waiting},
		Label: models.StateLabel(models.Waiting), Notes: []models.NewNote{{Body: "hi"}}}))

	_, err = c.AccessLevel(ctx, 12, "dev")
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

	c, err := gitlab.NewREST(srv.URL, token, "", bot)
	require.NoError(t, err)

	_, err = c.Load(context.Background(), key)
	require.Error(t, err)
	require.NotErrorIs(t, err, gitlab.ErrNotFound)
	assert.NotContains(t, err.Error(), token)

	_, err = c.AccessLevel(context.Background(), 12, "dev")
	require.Error(t, err, "a lookup error is an error, not level 0")
}

func TestRESTTrustsCAFile(t *testing.T) {
	f := gitlabtest.NewFake(bot)
	f.AddIssue(key, "krci/test-go-app", models.IssueOpened)

	srv := httptest.NewTLSServer(f.Handler(token))
	t.Cleanup(srv.Close)

	untrusted, err := gitlab.NewREST(srv.URL, token, "", bot)
	require.NoError(t, err)
	_, err = untrusted.Load(context.Background(), key)
	require.Error(t, err)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))

	trusted, err := gitlab.NewREST(srv.URL, token, caFile, bot)
	require.NoError(t, err)
	_, err = trusted.Load(context.Background(), key)
	require.NoError(t, err)
}

func TestRESTBadCAFile(t *testing.T) {
	dir := t.TempDir()

	_, err := gitlab.NewREST("https://gitlab.example", token, filepath.Join(dir, "missing.crt"), bot)
	require.Error(t, err)

	bad := filepath.Join(dir, "bad.crt")
	require.NoError(t, os.WriteFile(bad, []byte("not a pem"), 0o600))
	_, err = gitlab.NewREST("https://gitlab.example", token, bad, bot)
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

	c, err := gitlab.NewREST(srv.URL, "", "", bot, gitlab.WithTokenFile(file))
	require.NoError(t, err)

	_, err = c.AccessLevel(context.Background(), 12, "dev")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte("glpat-two"), 0o600))

	_, err = c.AccessLevel(context.Background(), 12, "dev")
	require.NoError(t, err)

	assert.Equal(t, []string{"glpat-one", "glpat-two"}, seen)

	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err = c.AccessLevel(context.Background(), 12, "dev")
	require.Error(t, err, "an empty token file is an error")
}
