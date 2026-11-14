package procgroup_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/procgroup"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

const settleBy = 5 * time.Second

// Scripts that a signal must end loop in the foreground instead of ending on wait: TERM to the group also ends the
// child, and sh can leave wait and exit 0 before it handles its own TERM.

func sh(script string) *exec.Cmd { return exec.Command("sh", "-c", script) }

func readPID(t *testing.T, path string) int {
	t.Helper()

	var raw []byte

	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		raw = b

		return err == nil && len(bytes.TrimSpace(b)) > 0
	}, settleBy, 10*time.Millisecond, "%s not written", path)

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)

	return pid
}

func awaitFile(t *testing.T, path string) {
	t.Helper()

	require.Eventually(t, func() bool {
		_, err := os.Stat(path)

		return err == nil
	}, settleBy, 10*time.Millisecond, "%s not written", path)
}

// assertGone fails the test, and kills pid, when pid still runs after settleBy. A gone pid is never signalled: it
// may belong to another process by then.
func assertGone(t *testing.T, pid int) {
	t.Helper()

	if !assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}, settleBy, 10*time.Millisecond, "process %d still runs", pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestWaitCopiesStdinStdoutAndStderr(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer

	cmd := sh(`cat; echo oops >&2`)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader("hello\n"), &out, &errOut

	p, err := procgroup.Start(cmd, procgroup.Options{})
	require.NoError(t, err)
	require.NoError(t, p.Wait())

	assert.Equal(t, "hello\n", out.String())
	assert.Equal(t, "oops\n", errOut.String())
}

func TestWaitKeepsTheOrderOfOneWriterForStdoutAndStderr(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	cmd := sh(`echo a; echo b >&2; echo c`)
	cmd.Stdout, cmd.Stderr = &out, &out

	p, err := procgroup.Start(cmd, procgroup.Options{})
	require.NoError(t, err)
	require.NoError(t, p.Wait())

	assert.Equal(t, "a\nb\nc\n", out.String())
}

func TestWaitReturnsTheExitError(t *testing.T) {
	t.Parallel()

	cmd := sh(`exit 3`)

	p, err := procgroup.Start(cmd, procgroup.Options{})
	require.NoError(t, err)

	var exit *exec.ExitError

	require.ErrorAs(t, p.Wait(), &exit)
	assert.Equal(t, 3, exit.ExitCode())
	assert.Equal(t, 3, cmd.ProcessState.ExitCode())
}

func TestStartFailsForAMissingCommand(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	cmd := exec.Command(filepath.Join(t.TempDir(), "missing"))
	cmd.Stdin, cmd.Stdout = strings.NewReader("x"), &out

	_, err := procgroup.Start(cmd, procgroup.Options{})

	require.Error(t, err)
}

func TestWaitKillsProcessesLeftInTheGroup(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := sh(`sleep 30 & echo $! > ` + pidFile)
	cmd.Stdout, cmd.Stderr = &out, &out

	// Wait returns before the drain bound only when the kill ends the group.
	const drain = 20 * time.Second

	p, err := procgroup.Start(cmd, procgroup.Options{Drain: drain})
	require.NoError(t, err)

	begin := time.Now()

	require.NoError(t, p.Wait())
	assert.Less(t, time.Since(begin), drain)
	assertGone(t, readPID(t, pidFile))
}

func TestWaitReturnsWhileAProcessOutsideTheGroupHoldsStdout(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := sh(`set -m; sleep 30 & echo $! > ` + pidFile + `; echo done`)
	cmd.Stdout = &out

	p, err := procgroup.Start(cmd, procgroup.Options{Drain: 200 * time.Millisecond})
	require.NoError(t, err)

	t.Cleanup(func() { _ = syscall.Kill(readPID(t, pidFile), syscall.SIGKILL) })

	begin := time.Now()

	require.NoError(t, p.Wait())
	assert.Less(t, time.Since(begin), 2*time.Second)
	assert.Equal(t, "done\n", out.String())
}

func TestSignalReachesTheGroupAndExitedCloses(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := sh(`sleep 30 & echo $! > ` + pidFile + `; while :; do sleep 1; done`)

	p, err := procgroup.Start(cmd, procgroup.Options{})
	require.NoError(t, err)

	child := readPID(t, pidFile)

	select {
	case <-p.Exited():
		t.Fatal("Exited closed before the leader exited")
	default:
	}

	p.Signal(syscall.SIGTERM)
	require.Error(t, p.Wait())

	select {
	case <-p.Exited():
	default:
		t.Fatal("Exited open after Wait")
	}

	assertGone(t, child)
}

// ignoreTerm records TERM in <dir>/term and goes on; its child ignores TERM and writes its pid to <dir>/child once
// it does.
const ignoreTerm = `trap 'echo term >> "$dir/term"' TERM
sh -c 'trap "" TERM; echo $$ > "$dir/child"; exec sleep 30' &
child=$!
while kill -0 $child 2>/dev/null; do wait $child; done`

// inDir is sh running script with $dir set to dir.
func inDir(script, dir string) *exec.Cmd {
	cmd := sh(script)
	env := os.Environ()
	cmd.Env = append(env, "dir="+dir)

	return cmd
}

func ignoringTerm(dir string) *exec.Cmd { return inDir(ignoreTerm, dir) }

func TestRunReturnsTheExitOfACommandThatEnds(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	cmd := sh(`echo ran`)
	cmd.Stdout = &out

	require.NoError(t, procgroup.Run(t.Context(), cmd, procgroup.Options{}))
	assert.Equal(t, "ran\n", out.String())

	var exit *exec.ExitError

	require.ErrorAs(t, procgroup.Run(t.Context(), sh(`exit 4`), procgroup.Options{}), &exit)
	assert.Equal(t, 4, exit.ExitCode())
}

func TestRunDoesNotStartOnAnEndedContext(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "ran")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := procgroup.Run(ctx, sh(`touch `+marker), procgroup.Options{})

	require.ErrorIs(t, err, context.Canceled)
	assert.NoFileExists(t, marker)
}

func TestRunTermsTheGroupWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cmd := inDir(`trap 'echo term > "$dir/term"; exit 0' TERM; : > "$dir/ready"; while :; do sleep 1; done`, dir)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		awaitFile(t, filepath.Join(dir, "ready"))
		cancel()
	}()

	require.NoError(t, procgroup.Run(ctx, cmd, procgroup.Options{Grace: time.Minute}))

	raw, err := os.ReadFile(filepath.Join(dir, "term"))
	require.NoError(t, err)
	assert.Equal(t, "term\n", string(raw))
}

func TestRunKillsTheGroupGraceAfterTerm(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	grace := 300 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cancelled := make(chan time.Time, 1)

	go func() {
		awaitFile(t, filepath.Join(dir, "child"))

		cancelled <- time.Now()

		cancel()
	}()

	require.Error(t, procgroup.Run(ctx, ignoringTerm(dir), procgroup.Options{Grace: grace}))

	assert.GreaterOrEqual(t, time.Since(<-cancelled), grace)

	raw, err := os.ReadFile(filepath.Join(dir, "term"))
	require.NoError(t, err)
	assert.Equal(t, "term\n", string(raw))
	assertGone(t, readPID(t, filepath.Join(dir, "child")))
}

func TestRunTimesTheGraceOnTheClock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	clk := systemtest.NewClock(time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- procgroup.Run(ctx, ignoringTerm(dir), procgroup.Options{Clock: clk, Grace: time.Minute})
	}()

	awaitFile(t, filepath.Join(dir, "child"))
	cancel()
	awaitFile(t, filepath.Join(dir, "term"))

	select {
	case err := <-done:
		t.Fatalf("Run returned %v before the grace passed on the clock", err)
	case <-time.After(200 * time.Millisecond):
	}

	clk.Advance(time.Minute)

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(settleBy):
		t.Fatal("Run did not return after the grace")
	}

	assertGone(t, readPID(t, filepath.Join(dir, "child")))
}
