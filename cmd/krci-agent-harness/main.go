// Command krci-agent-harness runs inside every actor, mounted read-only at /opt/krci-agent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"syscall"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/claude"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/selftest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/workspace"
)

// Exit codes; 64, 70, 74 and 78 follow sysexits.h.
const (
	exitOK       = 0
	exitGap      = 1
	exitUsage    = 64
	exitSoftware = 70
	exitIO       = 74
	exitConfig   = 78
)

const usage = `usage: krci-agent-harness <command>

commands:
  run       run the turn loop; env contract in docs/agent-harness.md; exit 78 on a handshake failure
  selftest  check the agent image contract; run the binary outside /opt/krci-agent; exit 1 on a gap
  help      print this text
`

func main() {
	handleSIGPIPE()

	os.Exit(run(os.Args[1:], system.OS{}, os.Stdout, os.Stderr))
}

// handleSIGPIPE turns a write to a closed stdout or stderr pipe into EPIPE, not death by SIGPIPE.
// Notify, not Ignore: an ignored signal stays ignored in child processes, a handled one resets on exec.
func handleSIGPIPE() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
}

func run(args []string, sys selftest.System, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "run":
			return runTurnLoop(sys, stderr)
		case "selftest":
			return runSelftest(sys, stdout, stderr)
		case "help", "-h", "--help":
			_, _ = fmt.Fprint(stdout, usage)

			return exitOK
		}
	}

	_, _ = fmt.Fprint(stderr, usage)

	return exitUsage
}

// placeholderToken is sent as PRIVATE-TOKEN; the egress gateway replaces it with the bot user's token.
const placeholderToken = "placeholder"

// stateDirName is the harness state directory under $HOME.
const stateDirName = ".krci-agent"

// gitAuthPlaceholder is the auth header of the harness's git commands; the egress gateway replaces the placeholder.
const gitAuthPlaceholder = "Authorization: Basic placeholder"

func runTurnLoop(sys selftest.System, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))

	cfg, err := harness.Handshake(sys.Getenv)
	if err != nil {
		log.Error("handshake failed", "error", err)

		return exitConfig
	}

	gl, err := gitlab.NewREST(cfg.GitLabURL.String(), placeholderToken, "", cfg.Bot, models.Developer,
		gitlab.WithLogger(log))
	if err != nil {
		log.Error("cannot build the GitLab client", "error", err)

		return exitSoftware
	}

	profile, err := harness.ReadProfile(sys.ReadFile)
	if err != nil {
		log.Error("handshake failed", "error", err)

		return exitConfig
	}

	stateDir := filepath.Join(cfg.Home, stateDirName)
	dir := path.Join(selftest.WorkDir, path.Base(cfg.ProjectPath))

	agentOut, wsOut := &lineLog{log: log, source: "claude"}, &lineLog{log: log, source: "workspace"}
	defer agentOut.Flush()
	defer wsOut.Flush()

	agent, err := claude.New(claude.Options{
		Dir: dir, Model: cfg.Model, Steps: cfg.AgentSteps, SystemPrompt: harness.SystemPrompt(cfg, dir, profile),
		Session: cfg.Session, StateDir: stateDir, Clock: system.Clock{}, Stderr: agentOut,
	})
	if err != nil {
		log.Error("cannot build the agent", "error", err)

		return exitSoftware
	}

	ws := workspace.New(workspace.Options{
		RepoURL: cfg.GitLabURL.String() + "/" + cfg.ProjectPath + ".git", Dir: dir, Branch: cfg.Key.IssueBranch(),
		Setup: profile.Setup, Verify: profile.Verify, Stderr: wsOut,
		AuthHeader: workspace.AuthHeader{
			ConfigKey: "http." + cfg.GitLabURL.GitConfigScope() + ".extraHeader", ConfigValue: gitAuthPlaceholder,
		},
	})

	h := harness.New(cfg, harness.Ports{
		GitLab: gl, Agent: agent, Workspace: ws, Clock: system.Clock{}, State: state.NewFile(stateDir), Log: log,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Info("Harness started", "issue", cfg.Key.IID, "project", cfg.Key.ProjectID, "session", cfg.Session)

	_ = h.Run(ctx)

	log.Info("Harness stopped")

	return exitOK
}

func runSelftest(sys selftest.System, stdout, stderr io.Writer) int {
	checks := selftest.AgentImageChecks(sys)

	enc := json.NewEncoder(stdout)
	for _, c := range checks {
		if err := enc.Encode(c); err != nil {
			_, _ = fmt.Fprintln(stderr, "write selftest result:", err)

			return exitIO
		}
	}

	if !selftest.AllOK(checks) {
		return exitGap
	}

	return exitOK
}
