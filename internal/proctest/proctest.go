// Package proctest holds process helpers for tests: whether a process has exited, and a process outside the
// caller's process group that keeps an output open.
package proctest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// HoldEnv names the pid file of Hold. A test binary whose TestMain sees it set runs Hold on its stdout and exits.
const HoldEnv = "KRCI_PROCTEST_HOLD_PID"

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
