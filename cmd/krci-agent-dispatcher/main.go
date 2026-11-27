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
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/profiles"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/dispatcher"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
)

const shutdownTimeout = 30 * time.Second

func main() {
	switch args := os.Args[1:]; {
	case len(args) == 2 && args[0] == "check-profiles":
		os.Exit(checkProfiles(args[1], os.Getenv("GITLAB_URL"), os.Stdout, os.Stderr))
	case len(args) != 0:
		_, _ = os.Stderr.WriteString(usage)
		os.Exit(exitUsage)
	}

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
		"atespace", cfg.Atespace, "worker-selector", cfg.WorkerSelector.String(), "gitlab", cfg.GitLabURL.String(),
		"trigger-role", cfg.TriggerMinRole.String(), "sweep-interval", cfg.SweepInterval.String(),
		"harness-image", cfg.HarnessImage, "agent-model", cfg.AgentModel, "agent-steps", cfg.AgentSteps,
		"profiles-file", cfg.ProfilesFile,
	)

	var labels models.AgentLabels

	sigCtx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	ctx, cancel := context.WithCancelCause(sigCtx)
	defer cancel(nil)

	glOpts := []gitlab.Option{
		gitlab.WithLogger(log), gitlab.WithAgentLabels(labels), gitlab.WithIdentityChange(cancel),
	}
	if cfg.GitLabTokenFile != "" {
		glOpts = append(glOpts, gitlab.WithTokenFile(cfg.GitLabTokenFile))
	}

	gl, err := gitlab.NewREST(cfg.GitLabURL.String(), cfg.GitLabToken, cfg.GitLabCAFile, cfg.TriggerMinRole,
		glOpts...)
	if err != nil {
		return err
	}

	deploy := substrate.Deployment{
		Atespace: cfg.Atespace, WorkerSelector: cfg.WorkerSelector, SecretsNamespace: cfg.SecretsNamespace,
		HarnessImage: cfg.HarnessImage, GitLabURL: cfg.GitLabURL, Model: cfg.AgentModel, AgentSteps: cfg.AgentSteps,
		TurnLimit: cfg.TurnLimit, TurnTimeout: cfg.TurnTimeout, HarnessPoll: cfg.HarnessPollInterval,
	}

	sb, err := substrate.NewGRPC(cfg.ATEAPIAddr, cfg.ATETokenFile, cfg.ATECAFile, deploy)
	if err != nil {
		return err
	}

	defer func() { _ = sb.Close() }()

	pr := profiles.NewFile(cfg.ProfilesFile, deploy)

	d, err := dispatcher.New(ctx, gl, sb, pr, system.Clock{}, dispatcher.Config{
		Labels: labels, TriggerRole: cfg.TriggerMinRole,
		TurnLimit: cfg.TurnLimit, TurnTimeout: cfg.TurnTimeout, PollInterval: cfg.PollInterval,
		SweepInterval: cfg.SweepInterval,
		Logger:        log,
	})
	if err != nil {
		if sigCtx.Err() != nil {
			log.Info("Stopped before the bot user was read")

			return nil
		}

		return err
	}

	log.Info("Bot user read from the GitLab token", "bot-user", d.Bot())

	server := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: api.NewRouter(d, api.RouterConfig{
			HookToken: cfg.HookToken, Bot: d.Bot(), Labels: labels, Logger: httpLogger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	runDone := make(chan error, 1)

	go func() { runDone <- d.Run(ctx) }()

	serveErr := serve(ctx, server)

	cancel(nil)

	if err := <-runDone; err != nil {
		return err
	}

	if cause := context.Cause(ctx); errors.Is(cause, models.ErrIdentityChanged) {
		return cause
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
