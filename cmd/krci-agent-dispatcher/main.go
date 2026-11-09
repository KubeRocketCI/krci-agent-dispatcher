package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	buildInfo "github.com/epam/edp-common/pkg/config"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v2"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/config"
)

const (
	healthPath      = "/healthz"
	shutdownTimeout = 30 * time.Second
)

func main() {
	logger := initLogger()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("Configuration is not valid", "error", err)
		os.Exit(1)
	}

	info := buildInfo.Get()
	logger.Info("Starting krci-agent-dispatcher",
		"version", info.Version,
		"git-commit", info.GitCommit,
		"git-tag", info.GitTag,
		"build-date", info.BuildDate,
		"go-version", info.Go,
		"platform", info.Platform,
	)

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           newRouter(logger),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if err := serve(ctx, server); err != nil {
		logger.Error("Server stopped with an error", "error", err)
		os.Exit(1)
	}
}

// serve runs the server until ctx ends, then shuts it down within shutdownTimeout.
func serve(ctx context.Context, server *http.Server) error {
	errCh := make(chan error, 1)

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}

		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	return server.Shutdown(shutdownCtx)
}

func newRouter(logger *httplog.Logger) *chi.Mux {
	r := chi.NewMux()
	r.Use(httplog.RequestLogger(logger, []string{healthPath}))
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(middleware.Heartbeat(healthPath))

	return r
}

func initLogger() *httplog.Logger {
	l := httplog.NewLogger("krci-agent-dispatcher", httplog.Options{
		JSON:            true,
		LogLevel:        slog.LevelInfo,
		Concise:         true,
		RequestHeaders:  false,
		TimeFieldFormat: time.RFC3339,
	})

	slog.SetDefault(l.Logger)

	return l
}
