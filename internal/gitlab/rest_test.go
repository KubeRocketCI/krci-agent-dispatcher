package gitlab_test

import (
	"bytes"
	"context"
	"encoding/json"
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

const userPath = "/api/v4/user"

// identified returns a REST adapter with trigger role developer after its first CurrentUser.
func identified(t *testing.T, baseURL, tok, caFile string, opts ...gitlab.Option) *gitlab.REST {
	t.Helper()

	c, err := gitlab.NewREST(baseURL, tok, caFile, models.Developer, opts...)
	require.NoError(t, err)

	_, err = c.CurrentUser(context.Background())
	require.NoError(t, err)

	return c
}

// withUser answers GET /api/v4/user with user and passes every other request to h.
func withUser(user string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == userPath {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"username": user})

			return
		}

		h(w, r)
	}
}

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

	c := identified(t, srv.URL, token, "")

	ctx := context.Background()
	_, err := c.Load(ctx, key)
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
	srv := httptest.NewServer(withUser(bot, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := identified(t, srv.URL, token, "")

	_, err := c.Load(context.Background(), key)
	require.Error(t, err)
	require.NotErrorIs(t, err, models.ErrNotFound)
	assert.NotContains(t, err.Error(), token)

	_, err = c.CanTrigger(context.Background(), 12, "dev")
	require.Error(t, err, "a lookup error is an error, not false")
	assert.NotContains(t, err.Error(), token)
}

func TestCurrentUserErrorHasNoToken(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"unauthorized": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
		},
		"no username": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id": 1}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)

			c, err := gitlab.NewREST(srv.URL, token, "", models.Developer)
			require.NoError(t, err)

			_, err = c.CurrentUser(context.Background())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), token)
		})
	}
}

func TestCurrentUserRejectedToken(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		want   error
	}{
		"401": {http.StatusUnauthorized, models.ErrUnauthorized},
		"403": {http.StatusForbidden, models.ErrForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(srv.Close)

			c, err := gitlab.NewREST(srv.URL, token, "", models.Developer)
			require.NoError(t, err)

			_, err = c.CurrentUser(context.Background())
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// Every call before the first CurrentUser fails and sends nothing.
func TestRESTCallsBeforeCurrentUserFail(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		reqs++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c, err := gitlab.NewREST(srv.URL, token, "", models.Developer)
	require.NoError(t, err)

	_, err = c.Load(context.Background(), key)
	require.ErrorIs(t, err, models.ErrNoIdentity)

	_, err = c.PostReply(context.Background(), key, "hi")
	require.ErrorIs(t, err, models.ErrNoIdentity)

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, reqs)
}

func TestRESTTrustsCAFile(t *testing.T) {
	f := gitlabtest.NewFake(bot)
	f.AddIssue(key, "krci/test-go-app", models.IssueOpened)

	srv := httptest.NewTLSServer(f.Handler(token))
	t.Cleanup(srv.Close)

	untrusted, err := gitlab.NewREST(srv.URL, token, "", models.Developer)
	require.NoError(t, err)
	_, err = untrusted.CurrentUser(context.Background())
	require.Error(t, err)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))

	trusted := identified(t, srv.URL, token, caFile)
	_, err = trusted.Load(context.Background(), key)
	require.NoError(t, err)
}

func TestRESTBadCAFile(t *testing.T) {
	dir := t.TempDir()

	_, err := gitlab.NewREST("https://gitlab.example", token, filepath.Join(dir, "missing.crt"), models.Developer)
	require.Error(t, err)

	bad := filepath.Join(dir, "bad.crt")
	require.NoError(t, os.WriteFile(bad, []byte("not a pem"), 0o600))
	_, err = gitlab.NewREST("https://gitlab.example", token, bad, models.Developer)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), token))
}

// tokenServer maps each token to its user on GET /user and answers [] to every other request. It records
// "<path> <token>" of every request.
type tokenServer struct {
	users map[string]string

	mu   sync.Mutex
	seen []string
}

func (s *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := r.Header.Get("PRIVATE-TOKEN")

	s.mu.Lock()
	s.seen = append(s.seen, strings.TrimPrefix(r.URL.Path, "/api/v4")+" "+tok)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if r.URL.Path != userPath {
		_, _ = w.Write([]byte(`[]`))

		return
	}

	user, ok := s.users[tok]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))

		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"username": user})
}

// requests returns the recorded requests and forgets them.
func (s *tokenServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := s.seen
	s.seen = nil

	return out
}

// tokenFileREST returns a REST adapter that reads its token from a file holding first, after its first CurrentUser,
// and the function that writes the file.
func tokenFileREST(t *testing.T, s *tokenServer, first string, opts ...gitlab.Option) (*gitlab.REST, func(string)) {
	t.Helper()

	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	file := filepath.Join(t.TempDir(), "token")
	write := func(tok string) { require.NoError(t, os.WriteFile(file, []byte(tok+"\n"), 0o600)) }
	write(first)

	c := identified(t, srv.URL, "", "", append([]gitlab.Option{gitlab.WithTokenFile(file)}, opts...)...)

	return c, write
}

func TestTokenFileReadPerRequest(t *testing.T) {
	s := &tokenServer{users: map[string]string{"glpat-one": bot, "glpat-two": bot}}
	c, write := tokenFileREST(t, s, "glpat-one")
	ctx := context.Background()

	_, err := c.CanTrigger(ctx, 12, "dev")
	require.NoError(t, err)
	write("glpat-two")

	_, err = c.CanTrigger(ctx, 12, "dev")
	require.NoError(t, err)

	_, err = c.CanTrigger(ctx, 12, "dev")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"/user glpat-one", "/projects/12/members/all glpat-one",
		"/user glpat-two", "/projects/12/members/all glpat-two",
		"/projects/12/members/all glpat-two",
	}, s.requests(), "a new token of the same user is read once with GET /user")

	write("")

	_, err = c.CanTrigger(ctx, 12, "dev")
	require.Error(t, err, "an empty token file is an error")
}

// A token file that names another user fails the request before it is sent, and every later call; the identity
// change is reported once.
func TestTokenFileOfAnotherUserFailsClosed(t *testing.T) {
	s := &tokenServer{users: map[string]string{"glpat-one": "project_1_bot_x", "glpat-two": "project_2_bot_y"}}

	var changes []error

	c, write := tokenFileREST(t, s, "glpat-one", gitlab.WithIdentityChange(func(err error) {
		changes = append(changes, err)
	}))
	ctx := context.Background()

	s.requests()

	write("glpat-two")

	_, err := c.Load(ctx, key)
	require.ErrorIs(t, err, models.ErrIdentityChanged)
	assert.Contains(t, err.Error(), "project_1_bot_x")
	assert.Contains(t, err.Error(), "project_2_bot_y")
	assert.NotContains(t, err.Error(), "glpat-")
	assert.Equal(t, []string{"/user glpat-two"}, s.requests(), "no request of the other user but GET /user")

	write("glpat-one")

	_, err = c.Load(ctx, key)
	require.ErrorIs(t, err, models.ErrIdentityChanged, "the change stays")

	_, err = c.PostReply(ctx, key, "hi")
	require.ErrorIs(t, err, models.ErrIdentityChanged)

	_, err = c.CurrentUser(ctx)
	require.ErrorIs(t, err, models.ErrIdentityChanged)
	assert.Empty(t, s.requests())

	require.Len(t, changes, 1)
	require.ErrorIs(t, changes[0], models.ErrIdentityChanged)
}

// A new token whose user cannot be read fails the request; the next request reads it again.
func TestTokenFileWithUnreadableUserFailsTheRequest(t *testing.T) {
	s := &tokenServer{users: map[string]string{"glpat-one": bot}}
	c, write := tokenFileREST(t, s, "glpat-one")
	ctx := context.Background()

	s.requests()

	write("glpat-revoked")

	_, err := c.CanTrigger(ctx, 12, "dev")
	require.Error(t, err)
	require.NotErrorIs(t, err, models.ErrIdentityChanged)
	assert.NotContains(t, err.Error(), "glpat-")

	_, err = c.CanTrigger(ctx, 12, "dev")
	require.Error(t, err)
	assert.Equal(t, []string{"/user glpat-revoked", "/user glpat-revoked"}, s.requests())

	write("glpat-one")

	_, err = c.CanTrigger(ctx, 12, "dev")
	require.NoError(t, err)
	assert.Equal(t, []string{"/projects/12/members/all glpat-one"}, s.requests(), "a verified token is not read again")
}

// countingREST returns a REST adapter over f and the log of the requests it sent after its first CurrentUser, as
// "<method> <path>".
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

	c := identified(t, srv.URL, token, "")

	requests := func() []string {
		mu.Lock()
		defer mu.Unlock()

		out := reqs
		reqs = nil

		return out
	}
	requests()

	return c, requests
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
	srv := httptest.NewServer(withUser(bot, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	var logs bytes.Buffer

	c := identified(t, srv.URL, token, "", gitlab.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	err := c.CheckProject(context.Background(), 12)
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

// A merge request opened between the read and the open answers 409; the merge request exists, so that is success.
func TestEnsureMergeRequestOpenedMeanwhile(t *testing.T) {
	f := gitlabtest.NewFake(bot)
	f.AddIssue(key, "krci/test-go-app", models.IssueOpened, agentLabels.Trigger())

	inner := f.Handler(token)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge_requests") {
			f.AddMergeRequest(key.ProjectID, 9, "agent/issue-7", "opened")
		}

		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := identified(t, srv.URL, token, "")

	require.NoError(t, c.EnsureMergeRequest(context.Background(), key.ProjectID, models.NewMergeRequest{
		Source: "agent/issue-7", Target: "main", Title: "t",
	}))
	assert.Len(t, f.MergeRequests(key.ProjectID), 1)
}
