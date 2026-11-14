// Command krci-agent-harness runs inside every actor, mounted read-only at /opt/krci-agent.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/selftest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/state"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
)

// Exit codes; 64, 69, 70, 74 and 78 follow sysexits.h.
const (
	exitOK          = 0
	exitGap         = 1
	exitUsage       = 64
	exitUnavailable = 69
	exitSoftware    = 70
	exitIO          = 74
	exitConfig      = 78
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

	_ = harness.New(cfg, harness.Ports{
		GitLab: gl, Clock: system.Clock{}, State: state.NewFile(filepath.Join(cfg.Home, stateDirName)), Log: log,
	})

	log.Error("agent and workspace adapters not built")

	return exitUnavailable
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
