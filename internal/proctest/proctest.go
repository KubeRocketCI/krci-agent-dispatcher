// Package proctest holds process helpers for tests: whether a process has exited, and a process outside the
// caller's process group that keeps an output open.
package proctest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// HoldEnv names the pid file of Hold. A test binary whose TestMain sees it set runs Hold on its stdout and exits.
const HoldEnv = "KRCI_PROCTEST_HOLD_PID"

// RunHolder is the first call of TestMain in a package that starts its test binary as a holder: with HoldEnv set it
// runs Hold on stdout and exits the process, 0 or 1 when Hold fails; else it returns.
func RunHolder() {
	pidFile := os.Getenv(HoldEnv)
	if pidFile == "" {
		return
	}

	if Hold(os.Stdout, pidFile) != nil {
		os.Exit(1)
	}

	os.Exit(0)
}

// HolderCommand is the sh command that runs the test binary as a holder with pid file pidFile. The test binary's
// TestMain must call RunHolder.
func HolderCommand(t testing.TB, pidFile string) string {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}

	return HoldEnv + "=" + shellQuote(pidFile) + " " + shellQuote(exe)
}

// WriteExecutable writes content to path with mode 0700. It holds syscall.ForkLock while the file is open, so no
// process forked meanwhile inherits the write descriptor: on Linux an exec of path fails with ETXTBSY while any
// process holds it open for writing.
func WriteExecutable(t testing.TB, path, content string) {
	t.Helper()

	syscall.ForkLock.RLock()
	err := os.WriteFile(path, []byte(content), 0o700)
	syscall.ForkLock.RUnlock()

	if err != nil {
		t.Fatal(err)
	}
}

// shellQuote quotes s as one sh word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Gone reports whether pid has exited: it no longer exists, or it is a zombie that its parent has not reaped.
func Gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || zombie(pid)
}

// Hold starts sleep 30 in a process group of its own with out as its stdout, writes its pid to pidFile, then writes
// "done\n" to out. The sleep is left running; when the pid file cannot be written it is killed and reaped.
func Hold(out *os.File, pidFile string) error {
	holder := exec.Command("sleep", "30")
	holder.Stdout = out
	holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := holder.Start(); err != nil {
		return fmt.Errorf("start holder: %w", err)
	}

	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(holder.Process.Pid)), 0o600); err != nil {
		_ = holder.Process.Kill()
		_ = holder.Wait()

		return fmt.Errorf("write holder pid: %w", err)
	}

	_, _ = out.WriteString("done\n")

	return nil
}
