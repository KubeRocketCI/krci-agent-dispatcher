// Command krci-agent-harness runs inside every actor, mounted read-only at /opt/krci-agent.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/selftest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
)

// Exit codes; 64 and 74 follow sysexits.h.
const (
	exitOK    = 0
	exitGap   = 1
	exitUsage = 64
	exitIO    = 74
)

const usage = `usage: krci-agent-harness <command>

commands:
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
