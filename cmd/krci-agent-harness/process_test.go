package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The test binary re-runs itself as the harness process when processEnv is set.
const (
	processEnv  = "KRCI_HARNESS_TEST_PROCESS"
	processArgs = "KRCI_HARNESS_TEST_ARGS"
)

func TestMain(m *testing.M) {
	switch os.Getenv(processEnv) {
	case "main":
		os.Args = append([]string{"krci-agent-harness"}, strings.Fields(os.Getenv(processArgs))...)

		main()
	case "child-pipe":
		handleSIGPIPE()

		// yes writes into a pipe head closes; with the default SIGPIPE disposition it ends silently.
		child := exec.Command("sh", "-c", "yes | head -n 1 >/dev/null")
		child.Stderr = os.Stderr

		if err := child.Run(); err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

func harnessProcess(t *testing.T, mode, args string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), processEnv+"="+mode, processArgs+"="+args)

	return cmd
}

func exitCode(t *testing.T, err error) int {
	t.Helper()

	if err == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run: %v", err)
	}

	if !exitErr.Exited() {
		t.Fatalf("process ended by %v, want an exit code", exitErr.ProcessState)
	}

	return exitErr.ExitCode()
}

func TestProcessClosedStdoutExitsIO(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	_ = r.Close()

	defer func() { _ = w.Close() }()

	var stderr bytes.Buffer

	cmd := harnessProcess(t, "main", "selftest")
	cmd.Stdout, cmd.Stderr = w, &stderr

	if got := exitCode(t, cmd.Run()); got != exitIO {
		t.Fatalf("exit = %d, want %d; stderr %q", got, exitIO, stderr.String())
	}

	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Fatalf("stderr %q, want broken pipe", stderr.String())
	}
}

func TestProcessChildKeepsDefaultSIGPIPE(t *testing.T) {
	var stderr bytes.Buffer

	cmd := harnessProcess(t, "child-pipe", "")
	cmd.Stderr = &stderr

	if got := exitCode(t, cmd.Run()); got != 0 || stderr.Len() != 0 {
		t.Fatalf("child pipeline: exit %d, stderr %q; want 0 and no output", got, stderr.String())
	}
}
