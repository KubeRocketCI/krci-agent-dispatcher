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
	"github.com/go-chi/httplog/v2"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/api"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/config"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/dispatcher"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate"
)

const shutdownTimeout = 30 * time.Second

// clock is the real time source of the dispatcher.
type clock struct{}

func (clock) Now() time.Time { return time.Now() }

func (clock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func main() {
	logger := api.NewLogger(os.Stdout)
	slog.SetDefault(logger.Logger)

	if err := run(logger.Logger, logger); err != nil {
		logger.Error("krci-agent-dispatcher stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, httpLogger *httplog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	info := buildInfo.Get()
	log.Info("Starting krci-agent-dispatcher",
		"version", info.Version, "git-commit", info.GitCommit, "git-tag", info.GitTag,
		"build-date", info.BuildDate, "go-version", info.Go, "platform", info.Platform,
		"atespace", cfg.Atespace, "pool", cfg.Pool, "gitlab", cfg.GitLabURL,
	)

	var glOpts []gitlab.Option
	if cfg.GitLabTokenFile != "" {
		glOpts = append(glOpts, gitlab.WithTokenFile(cfg.GitLabTokenFile))
	}

	gl, err := gitlab.NewREST(cfg.GitLabURL, cfg.GitLabToken, cfg.GitLabCAFile, cfg.BotUsername, glOpts...)
	if err != nil {
		return err
	}

	sb, err := substrate.NewGRPC(cfg.ATEAPIAddr, cfg.ATETokenFile, cfg.ATECAFile, cfg.Atespace)
	if err != nil {
		return err
	}

	defer func() { _ = sb.Close() }()

	d := dispatcher.New(gl, sb, clock{}, dispatcher.Config{
		Atespace: cfg.Atespace, Pool: cfg.Pool, SecretsNamespace: cfg.SecretsNamespace, Image: cfg.AgentImage,
		GitLabURL: cfg.GitLabURL, Bot: cfg.BotUsername, TriggerLabel: cfg.TriggerLabel,
		MaxTurns: cfg.MaxTurnsPerIssue, TurnTimeout: cfg.TurnTimeout, PollInterval: cfg.PollInterval,
		Logger: log,
	})

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	server := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: api.NewRouter(d, api.RouterConfig{
			HookToken: cfg.HookToken, Bot: cfg.BotUsername, TriggerLabel: cfg.TriggerLabel, Logger: httpLogger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	runDone := make(chan error, 1)

	go func() { runDone <- d.Run(ctx) }()

	serveErr := serve(ctx, server)

	stop()

	if err := <-runDone; err != nil {
		return err
	}

	return serveErr
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
