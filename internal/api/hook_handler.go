package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v2"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/intake"
)

const (
	healthPath     = "/healthz"
	maxBodyBytes   = 1 << 20
	requestTimeout = 10 * time.Second
	hookTokenName  = "X-Gitlab-Token"
)

var errEOF = io.EOF

// RequestHandler takes requests for issue lanes; it must return at once.
type RequestHandler interface {
	Handle(r intake.Request)
}

// RouterConfig configures NewRouter. HookToken is required.
type RouterConfig struct {
	HookToken string
	Bot       string
	Labels    models.AgentLabels
	Logger    *httplog.Logger
}

// NewLogger returns the JSON request and application logger. Request headers are never logged.
func NewLogger(w io.Writer) *httplog.Logger {
	l := httplog.NewLogger("krci-agent-dispatcher", httplog.Options{
		JSON:            true,
		LogLevel:        slog.LevelInfo,
		Concise:         true,
		RequestHeaders:  false,
		TimeFieldFormat: time.RFC3339,
		Writer:          w,
	})

	return l
}

// NewRouter returns the HTTP handler: POST /hook (token check, then the generated strict server) and
// GET /healthz. It panics when cfg.HookToken is empty.
func NewRouter(h RequestHandler, cfg RouterConfig) http.Handler {
	if cfg.HookToken == "" {
		panic("api: empty hook token")
	}

	r := chi.NewMux()
	r.Use(middleware.RequestID)
	r.Use(httplog.RequestLogger(cfg.Logger, []string{healthPath}))
	r.Use(middleware.Timeout(requestTimeout))
	r.Use(middleware.Heartbeat(healthPath))
	r.Use(requireToken(cfg.HookToken))
	r.Use(limitBody)

	strict := NewStrictHandlerWithOptions(&hookServer{h: h, bot: cfg.Bot, labels: cfg.Labels}, nil,
		StrictHTTPServerOptions{RequestErrorHandlerFunc: writeProblem, ResponseErrorHandlerFunc: writeProblem})

	return HandlerWithOptions(strict, ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: writeProblem})
}

// requireToken rejects requests whose X-Gitlab-Token differs from token. The SHA-256 digests are compared in
// constant time, so neither the value nor the length leaks.
func requireToken(token string) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := sha256.Sum256([]byte(r.Header.Get(hookTokenName)))
			if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
				writeProblem(w, r, errUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

type hookServer struct {
	h      RequestHandler
	bot    string
	labels models.AgentLabels
}

// PostHook maps the event to a request, hands it to the lanes and replies 202 at once.
func (s *hookServer) PostHook(ctx context.Context, req PostHookRequestObject) (PostHookResponseObject, error) {
	event := ""
	if req.Params.XGitlabEvent != nil {
		event = *req.Params.XGitlabEvent
	}

	r, err := intake.Parse(event, *req.Body, s.bot, s.labels)
	if err != nil {
		return nil, errBadPayload
	}

	if r.Kind != intake.Ignore {
		s.h.Handle(r)
	}

	httplog.LogEntrySetFields(ctx, map[string]any{"event": event, "kind": r.Kind.String(), "issue": r.Key.String()})

	return PostHook202JSONResponse{Kind: AcceptedKind(r.Kind.String())}, nil
}
