package proctest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/proctest"
)

func TestMain(m *testing.M) {
	proctest.RunHolder()
	os.Exit(m.Run())
}

func TestGoneIsFalseWhileTheProcessRuns(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	assert.False(t, proctest.Gone(cmd.Process.Pid))
}

func TestGoneIsTrueForAnUnreapedAndAReapedProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())

	pid := cmd.Process.Pid

	require.NoError(t, cmd.Process.Kill())
	assert.Eventually(t, func() bool { return proctest.Gone(pid) }, 5*time.Second, 10*time.Millisecond, "unreaped")

	_ = cmd.Wait()

	assert.True(t, proctest.Gone(pid), "reaped")
}

func TestHoldLeavesSleepInItsOwnGroupOnOut(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")

	out, err := os.Create(filepath.Join(dir, "out"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = out.Close() })

	require.NoError(t, proctest.Hold(out, pidFile))

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)

	pid, err := strconv.Atoi(string(raw))
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_, _ = syscall.Wait4(pid, nil, 0, nil)
	})

	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	assert.Equal(t, pid, pgid)
	assert.False(t, proctest.Gone(pid))

	got, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	assert.Equal(t, "done\n", string(got))
}

func TestHolderCommandStartsTheTestBinaryAsAHolder(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "it's a pid")

	out, err := os.Create(filepath.Join(dir, "out"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = out.Close() })

	cmd := exec.Command("sh", "-c", proctest.HolderCommand(t, pidFile))
	cmd.Stdout = out
	require.NoError(t, cmd.Run())

	got, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	assert.Equal(t, "done\n", string(got))

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)

	pid, err := strconv.Atoi(string(raw))
	require.NoError(t, err)

	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	assert.Equal(t, pid, pgid)
	assert.False(t, proctest.Gone(pid))
}

func TestHoldFailsWhenThePidFileCannotBeWritten(t *testing.T) {
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = out.Close() })

	require.Error(t, proctest.Hold(out, filepath.Join(t.TempDir(), "missing", "pid")))
}
