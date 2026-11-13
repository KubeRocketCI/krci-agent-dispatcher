// Command krci-agent-harness runs inside every actor, mounted read-only at /opt/krci-agent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/harness/testbedprobe"
)

const (
	exitOK    = 0
	exitGap   = 1
	exitUsage = 64
)

const usage = `usage: krci-agent-harness <command>

commands:
  selftest       check the agent image contract; exit 1 on a gap
  testbed-probe  serve GET /state on :8080 for the image volume mount check on the testbed
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], harness.OS(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, sys harness.System, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprint(stderr, usage)

		return exitUsage
	}

	switch args[0] {
	case "selftest":
		return selftest(sys, stdout)
	case "testbed-probe":
		return probe(ctx, sys, stdout, stderr)
	default:
		_, _ = fmt.Fprint(stderr, usage)

		return exitUsage
	}
}

func selftest(sys harness.System, stdout io.Writer) int {
	checks := harness.ContractChecks(sys)

	enc := json.NewEncoder(stdout)
	for _, c := range checks {
		_ = enc.Encode(c)
	}

	if !harness.AllOK(checks) {
		return exitGap
	}

	return exitOK
}

func probe(ctx context.Context, sys harness.System, stdout, stderr io.Writer) int {
	p, err := testbedprobe.New(sys, harness.WorkDir, testbedprobe.TrustFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)

		return exitGap
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", testbedprobe.Addr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)

		return exitGap
	}

	if err := testbedprobe.Run(ctx, p, ln, testbedprobe.Tick, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, err)

		return exitGap
	}

	return exitOK
}
