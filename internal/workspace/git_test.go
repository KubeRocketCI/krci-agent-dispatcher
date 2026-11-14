package workspace_test

import (
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

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/workspace"
)

var _ harness.Workspace = (*workspace.Git)(nil)

const issueBranch = "agent/issue-7"

// inheritedIgnored is the suffix of files that the inherited git config entry GIT_CONFIG_KEY_0 excludes.
const inheritedIgnored = ".inherited"

// TestMain keeps git away from the user's and the system's config for the whole process, so tests run in parallel.
// The process inherits one git config entry through GIT_CONFIG_COUNT.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	home, err := os.MkdirTemp("", "workspace-home-")
	if err != nil {
		panic(err)
	}

	defer func() { _ = os.RemoveAll(home) }()

	global := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		panic(err)
	}

	excludes := filepath.Join(home, "excludes")
	if err := os.WriteFile(excludes, []byte("*"+inheritedIgnored+"\n"), 0o600); err != nil {
		panic(err)
	}

	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   global,
		"GIT_CONFIG_COUNT":    "1",
		"GIT_CONFIG_KEY_0":    "core.excludesFile",
		"GIT_CONFIG_VALUE_0":  excludes,
		"GIT_AUTHOR_NAME":     "Test Agent",
		"GIT_AUTHOR_EMAIL":    "agent@example.com",
		"GIT_COMMITTER_NAME":  "Test Agent",
		"GIT_COMMITTER_EMAIL": "agent@example.com",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}

	return m.Run()
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)

	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file string) {
	t.Helper()

	commitContent(t, dir, file, file)
}

// commitContent commits file with content.
func commitContent(t *testing.T, dir, file, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600))
	git(t, dir, "add", file)
	git(t, dir, "commit", "-q", "-m", "write "+file)
}

// remote is a bare origin with one commit on main and a seed checkout pushing to it.
type remote struct {
	url  string
	seed string
}

func newRemote(t *testing.T) remote {
	t.Helper()
	t.Parallel()

	url := filepath.Join(t.TempDir(), "origin.git")
	git(t, ".", "init", "-q", "--bare", "-b", "main", url)

	seed := filepath.Join(t.TempDir(), "seed")
	git(t, ".", "clone", "-q", url, seed)
	git(t, seed, "checkout", "-q", "-b", "main")
	commit(t, seed, "README")
	git(t, seed, "push", "-q", "origin", "main")

	return remote{url: url, seed: seed}
}

// pushBranch pushes a commit adding file on the issue branch of the remote.
func (r remote) pushBranch(t *testing.T, file string) {
	t.Helper()

	r.pushContent(t, file, file)
}

// pushContent pushes a commit writing content to file on the issue branch of the remote.
func (r remote) pushContent(t *testing.T, file, content string) {
	t.Helper()

	git(t, r.seed, "fetch", "-q", "origin")

	start := "origin/main"
	if exec.Command("git", "-C", r.seed, "rev-parse", "-q", "--verify", "origin/"+issueBranch).Run() == nil {
		start = "origin/" + issueBranch
	}

	git(t, r.seed, "checkout", "-q", "-B", issueBranch, start)
	commitContent(t, r.seed, file, content)
	git(t, r.seed, "push", "-q", "origin", issueBranch)
}

func (r remote) hasBranchFile(t *testing.T, file string) bool {
	t.Helper()

	cmd := exec.Command("git", "-C", r.url, "cat-file", "-e", issueBranch+":"+file)

	return cmd.Run() == nil
}

func newGit(t *testing.T, r remote, opts workspace.Options) (*workspace.Git, string) {
	t.Helper()

	opts.RepoURL = r.url
	if opts.Dir == "" {
		opts.Dir = filepath.Join(t.TempDir(), "repo")
	}

	opts.Branch = issueBranch

	return workspace.New(opts), opts.Dir
}

func TestPrepareFreshCreatesBranchFromDefault(t *testing.T) {
	r := newRemote(t)
	w, dir := newGit(t, r, workspace.Options{})

	prepare(t, w, true)

	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, git(t, r.url, "rev-parse", "main"), git(t, dir, "rev-parse", "HEAD"))
}

func TestPrepareFreshChecksOutRemoteBranch(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "work.txt")
	w, dir := newGit(t, r, workspace.Options{})

	prepare(t, w, true)

	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, git(t, r.url, "rev-parse", issueBranch), git(t, dir, "rev-parse", "HEAD"))
	assert.FileExists(t, filepath.Join(dir, "work.txt"))
	assert.Equal(t, "origin/"+issueBranch, git(t, dir, "rev-parse", "--abbrev-ref", "@{upstream}"))
}

func TestPrepareRunsSetupOnFreshOnly(t *testing.T) {
	r := newRemote(t)
	count := filepath.Join(t.TempDir(), "count")
	w, _ := newGit(t, r, workspace.Options{Setup: "echo run >> " + count})

	prepare(t, w, true)
	prepare(t, w, false)

	runs, err := os.ReadFile(count)
	require.NoError(t, err)
	assert.Equal(t, "run\n", string(runs))
}

func TestPrepareRunsSetupInCheckout(t *testing.T) {
	r := newRemote(t)
	w, dir := newGit(t, r, workspace.Options{Setup: "touch setup-ran"})

	prepare(t, w, true)

	assert.FileExists(t, filepath.Join(dir, "setup-ran"))
}

func TestPrepareFailsWhenSetupFails(t *testing.T) {
	r := newRemote(t)
	w, _ := newGit(t, r, workspace.Options{Setup: "echo broken toolchain >&2; exit 3"})

	_, err := w.Prepare(t.Context(), true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup")
	assert.Contains(t, err.Error(), "broken toolchain")
}

func TestPrepareFreshReplacesExistingDir(t *testing.T) {
	r := newRemote(t)
	dir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stale"), nil, 0o600))
	w, _ := newGit(t, r, workspace.Options{Dir: dir})

	prepare(t, w, true)

	assert.NoFileExists(t, filepath.Join(dir, "stale"))
	assert.FileExists(t, filepath.Join(dir, "README"))
}

func TestPrepareFetchFastForwards(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	w, dir := newGit(t, r, workspace.Options{})
	prepare(t, w, true)
	r.pushBranch(t, "two.txt")

	prepare(t, w, false)

	assert.Equal(t, git(t, r.url, "rev-parse", issueBranch), git(t, dir, "rev-parse", "HEAD"))
	assert.FileExists(t, filepath.Join(dir, "two.txt"))
}

func TestPrepareFetchKeepsLocalCommits(t *testing.T) {
	r := newRemote(t)
	w, dir := newGit(t, r, workspace.Options{})
	prepare(t, w, true)
	commit(t, dir, "local.txt")
	local := git(t, dir, "rev-parse", "HEAD")

	prepare(t, w, false)

	assert.Equal(t, local, git(t, dir, "rev-parse", "HEAD"))
}

func TestPrepareFetchRebasesLocalCommitsOntoTheRemote(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	w, dir := prepared(t, r, workspace.Options{})
	commit(t, dir, "local.txt")
	r.pushBranch(t, "remote.txt")

	prepare(t, w, false)

	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, git(t, r.url, "rev-parse", issueBranch), git(t, dir, "rev-parse", "HEAD~1"))
	assert.Equal(t, "write local.txt", git(t, dir, "log", "-1", "--format=%s"))
	assert.FileExists(t, filepath.Join(dir, "local.txt"))
	assert.FileExists(t, filepath.Join(dir, "remote.txt"))

	got, err := w.NewCommitsCheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{NewCommits: true}, got)
}

// requireSetAside requires Prepare to set the local commits aside on name, with Branch reset to the remote one.
func requireSetAside(t *testing.T, w *workspace.Git, r remote, dir, name string) harness.Prepared {
	t.Helper()

	got, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)
	assert.Equal(t, name, got.SetAside)
	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, git(t, r.url, "rev-parse", issueBranch), git(t, dir, "rev-parse", "HEAD"))
	assert.Empty(t, git(t, dir, "status", "--porcelain"))

	st, err := w.NewCommitsCheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, st)

	return got
}

func TestPrepareFetchSetsAsideConflictingLocalCommits(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	w, dir := prepared(t, r, workspace.Options{})
	commitContent(t, dir, "one.txt", "local")
	first := git(t, dir, "rev-parse", "HEAD")
	r.pushContent(t, "one.txt", "remote")

	requireSetAside(t, w, r, dir, issueBranch+"-unpushed-1")
	assert.Equal(t, first, git(t, dir, "rev-parse", issueBranch+"-unpushed-1"))

	commitContent(t, dir, "one.txt", "local again")
	second := git(t, dir, "rev-parse", "HEAD")
	r.pushContent(t, "one.txt", "remote again")

	requireSetAside(t, w, r, dir, issueBranch+"-unpushed-2")
	assert.Equal(t, second, git(t, dir, "rev-parse", issueBranch+"-unpushed-2"))
	assert.Equal(t, first, git(t, dir, "rev-parse", issueBranch+"-unpushed-1"))
}

func TestPrepareFetchAbortsARebaseLeftStopped(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	w, dir := prepared(t, r, workspace.Options{})
	commitContent(t, dir, "one.txt", "local")
	local := git(t, dir, "rev-parse", "HEAD")
	r.pushContent(t, "one.txt", "remote")
	git(t, dir, "fetch", "-q", "origin")
	require.Error(t, exec.Command("git", "-C", dir, "rebase", "-q", "origin/"+issueBranch).Run())

	got := requireSetAside(t, w, r, dir, issueBranch+"-unpushed-1")
	assert.Equal(t, []string{"rebase"}, got.Aborted)
	assert.Equal(t, local, git(t, dir, "rev-parse", issueBranch+"-unpushed-1"))
}

func TestPrepareFetchAfterAnAmendedPushedCommit(t *testing.T) {
	r := newRemote(t)
	w, dir := prepared(t, r, workspace.Options{})
	commit(t, dir, "fix.txt")
	require.NoError(t, w.Push(t.Context()))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("amended"), 0o600))
	git(t, dir, "commit", "-q", "-a", "--amend", "--no-edit")

	_, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)

	_, err = w.NewCommitsCheck(t.Context())
	require.NoError(t, err)
	git(t, dir, "merge-base", "--is-ancestor", "origin/"+issueBranch, "HEAD")
}

// requireStashed requires got to name a stash entry whose apply restores file with content.
func requireStashed(t *testing.T, got harness.Prepared, dir, file, content string) {
	t.Helper()

	require.NotEmpty(t, got.Stashed)
	assert.Empty(t, git(t, dir, "status", "--porcelain", "--untracked-files=no"))
	git(t, dir, "stash", "apply", "-q", got.Stashed)
	assert.Equal(t, content, readFile(t, filepath.Join(dir, file)))
}

func TestPrepareFetchStashesChangedTrackedFilesThenRebases(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	w, dir := prepared(t, r, workspace.Options{})
	commit(t, dir, "local.txt")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("edited"), 0o600))
	r.pushBranch(t, "remote.txt")

	got, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)

	assert.Empty(t, got.Aborted)
	assert.Empty(t, got.SetAside)
	git(t, dir, "merge-base", "--is-ancestor", "origin/"+issueBranch, "HEAD")
	assert.FileExists(t, filepath.Join(dir, "local.txt"))
	requireStashed(t, got, dir, "README", "edited")
}

func TestPrepareFetchFailsWhenTheRebaseWouldOverwriteUntrackedFiles(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	w, dir := prepared(t, r, workspace.Options{})
	commit(t, dir, "local.txt")
	local := git(t, dir, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "remote.txt"), []byte("mine"), 0o600))
	r.pushBranch(t, "remote.txt")

	_, err := w.Prepare(t.Context(), false)

	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "rebase: "), "error %q names the rebase", err)
	assert.Equal(t, local, git(t, dir, "rev-parse", "HEAD"))
	assert.Equal(t, "mine", readFile(t, filepath.Join(dir, "remote.txt")))
}

func TestPrepareFetchStashesAChangeToAFileThePushChanged(t *testing.T) {
	r := newRemote(t)
	r.pushContent(t, "handler.go", "agent v1\n")
	w, dir := prepared(t, r, workspace.Options{})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "handler.go"), []byte("agent v2 wip\n"), 0o600))
	r.pushContent(t, "handler.go", "person suggestion\n")

	got, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)

	assert.Equal(t, git(t, r.url, "rev-parse", issueBranch), git(t, dir, "rev-parse", "HEAD"))
	assert.Equal(t, "person suggestion\n", readFile(t, filepath.Join(dir, "handler.go")))
	require.NotEmpty(t, got.Stashed)
	assert.Equal(t, "agent v2 wip\n", git(t, dir, "show", got.Stashed+":handler.go")+"\n")

	again, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)
	assert.Equal(t, harness.Prepared{}, again, "a tidy checkout moves nothing")
}

func TestPrepareFetchAbortsAnUnfinishedMerge(t *testing.T) {
	r := newRemote(t)
	r.pushContent(t, "handler.go", "agent v1\n")
	w, dir := prepared(t, r, workspace.Options{})
	head := git(t, dir, "rev-parse", "HEAD")
	git(t, r.seed, "checkout", "-q", "main")
	git(t, r.seed, "pull", "-q", "origin", "main")
	commitContent(t, r.seed, "handler.go", "main v2\n")
	git(t, r.seed, "push", "-q", "origin", "main")
	git(t, dir, "fetch", "-q", "origin")
	require.Error(t, exec.Command("git", "-C", dir, "merge", "origin/main").Run(), "the merge conflicts")

	got, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)

	assert.Equal(t, harness.Prepared{Aborted: []string{"merge"}}, got)
	assert.Equal(t, head, git(t, dir, "rev-parse", "HEAD"))
	assert.Empty(t, git(t, dir, "status", "--porcelain"))
}

func TestPrepareFetchAbortsAnUnfinishedCherryPickAndStashesWhatRemains(t *testing.T) {
	r := newRemote(t)
	r.pushContent(t, "handler.go", "agent v1\n")
	w, dir := prepared(t, r, workspace.Options{})
	git(t, dir, "checkout", "-q", "-b", "side")
	commitContent(t, dir, "handler.go", "side\n")
	git(t, dir, "checkout", "-q", issueBranch)
	commitContent(t, dir, "handler.go", "agent v2\n")
	require.NoError(t, w.Push(t.Context()))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("edited"), 0o600))
	require.Error(t, exec.Command("git", "-C", dir, "cherry-pick", "side").Run(), "the cherry-pick conflicts")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "handler.go"), []byte("resolved\n"), 0o600))
	git(t, dir, "add", "handler.go")

	got, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)

	assert.Equal(t, []string{"cherry-pick"}, got.Aborted)
	assert.Equal(t, git(t, r.url, "rev-parse", issueBranch), git(t, dir, "rev-parse", "HEAD"))
	assert.Equal(t, "agent v2\n", readFile(t, filepath.Join(dir, "handler.go")))
	requireStashed(t, got, dir, "README", "edited")
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	require.NoError(t, err)

	return string(b)
}

func setupRuns(t *testing.T, count string) int {
	t.Helper()

	runs, err := os.ReadFile(count)
	require.NoError(t, err)

	return strings.Count(string(runs), "run\n")
}

func TestPrepareFetchSwitchesBackFromAnotherBranch(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	count := filepath.Join(t.TempDir(), "count")
	w, dir := prepared(t, r, workspace.Options{Setup: "echo run >> " + count})
	git(t, dir, "checkout", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600))
	r.pushBranch(t, "two.txt")

	got, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)
	assert.Equal(t, harness.Prepared{Untracked: []string{"notes.txt"}}, got)

	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, git(t, r.url, "rev-parse", issueBranch), git(t, dir, "rev-parse", "HEAD"))
	assert.FileExists(t, filepath.Join(dir, "notes.txt"))
	assert.Equal(t, 1, setupRuns(t, count))
}

func TestPrepareFetchSwitchesBackFromADetachedHead(t *testing.T) {
	r := newRemote(t)
	w, dir := prepared(t, r, workspace.Options{})
	commit(t, dir, "local.txt")
	local := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "--detach", "HEAD~1")

	prepare(t, w, false)

	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, local, git(t, dir, "rev-parse", "HEAD"))
}

func TestPrepareFetchStashesChangedTrackedFilesOnAnotherBranchAndSwitchesBack(t *testing.T) {
	r := newRemote(t)
	count := filepath.Join(t.TempDir(), "count")
	w, dir := prepared(t, r, workspace.Options{Setup: "echo run >> " + count})
	git(t, dir, "checkout", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("edited"), 0o600))

	got, err := w.Prepare(t.Context(), false)
	require.NoError(t, err)

	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, 1, setupRuns(t, count), "no clone")
	requireStashed(t, got, dir, "README", "edited")
}

func TestPrepareFetchClonesMissingCheckout(t *testing.T) {
	r := newRemote(t)
	count := filepath.Join(t.TempDir(), "count")
	w, dir := newGit(t, r, workspace.Options{Setup: "echo run >> " + count})

	prepare(t, w, false)

	assert.Equal(t, issueBranch, git(t, dir, "symbolic-ref", "--short", "HEAD"))

	runs, err := os.ReadFile(count)
	require.NoError(t, err)
	assert.Equal(t, "run\n", string(runs))
}

func TestPrepareFailsOnMissingRemote(t *testing.T) {
	t.Parallel()

	w := workspace.New(workspace.Options{
		RepoURL: filepath.Join(t.TempDir(), "missing.git"),
		Dir:     filepath.Join(t.TempDir(), "repo"),
		Branch:  issueBranch,
	})

	_, err := w.Prepare(t.Context(), true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "clone")
}

// prepare requires Prepare to succeed with nothing set aside.
func prepare(t *testing.T, w harness.Workspace, fresh bool) {
	t.Helper()

	got, err := w.Prepare(t.Context(), fresh)
	require.NoError(t, err)
	assert.Equal(t, harness.Prepared{}, got)
}

// prepared returns a workspace prepared fresh on r and its checkout.
func prepared(t *testing.T, r remote, opts workspace.Options) (*workspace.Git, string) {
	t.Helper()

	w, dir := newGit(t, r, opts)
	prepare(t, w, true)

	return w, dir
}

func requireCheckFails(t *testing.T, w *workspace.Git, want string) {
	t.Helper()

	_, err := w.NewCommitsCheck(t.Context())

	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "new-commits check: "), "error %q names the step", err)
	assert.Contains(t, err.Error(), want)
}

func TestBranchFailsOnAnotherBranch(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	git(t, dir, "checkout", "-q", "main")

	requireCheckFails(t, w, issueBranch)
}

func TestBranchFailsOnDetachedHead(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	git(t, dir, "checkout", "-q", "--detach")

	requireCheckFails(t, w, issueBranch)
}

func TestBranchFailsOnModifiedTrackedFiles(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("edited"), 0o600))

	requireCheckFails(t, w, "tracked")
}

func TestBranchFailsOnStagedFiles(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "staged.txt"), nil, 0o600))
	git(t, dir, "add", "staged.txt")

	requireCheckFails(t, w, "tracked")
}

func TestBranchFailsOnUntrackedFilesWithNewCommits(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	commit(t, dir, "fix.txt")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), nil, 0o600))

	requireCheckFails(t, w, "untracked files in 1 paths: new.txt")
}

func TestBranchNamesTheFirstFiveUntrackedPaths(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	commit(t, dir, "fix.txt")

	for _, f := range []string{"a", "b", "c", "d", "e", "f"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
	}

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "out", "bin"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "out", "bin", "app"), nil, 0o600))

	requireCheckFails(t, w, "untracked files in 7 paths: a, b, c, d, e, …")
}

func TestPrepareFetchNamesUntrackedPathsAnUntrackedDirectoryAsOne(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gin-gonic"), nil, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "out", "bin"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "out", "bin", "app"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x"+inheritedIgnored), nil, 0o600))

	got, err := w.Prepare(t.Context(), false)

	require.NoError(t, err)
	assert.Equal(t, harness.Prepared{Untracked: []string{"gin-gonic", "out/"}}, got)
}

func TestBranchIgnoresUntrackedFilesWithoutNewCommits(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), nil, 0o600))

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, got)
}

func TestBranchIgnoresIgnoredFilesWithNewCommits(t *testing.T) {
	w, _ := prepared(t, newRemote(t), workspace.Options{Setup: "echo build/ > .gitignore && git add .gitignore && " +
		"git commit -q -m ignore && mkdir build && touch build/out"})

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{NewCommits: true}, got)
}

func TestBranchWithoutCommits(t *testing.T) {
	w, _ := prepared(t, newRemote(t), workspace.Options{})

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, got)
}

func TestBranchNewCommitsBeforeTheFirstPush(t *testing.T) {
	w, dir := prepared(t, newRemote(t), workspace.Options{})
	commit(t, dir, "fix.txt")

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{NewCommits: true}, got)
}

func TestBranchCommitsOfTheRemoteIssueBranchAreNotNew(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "pushed.txt")
	w, _ := prepared(t, r, workspace.Options{})

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, got)
}

func TestBranchNewCommitsOnTheRemoteIssueBranch(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "pushed.txt")
	w, dir := prepared(t, r, workspace.Options{})
	commit(t, dir, "more.txt")

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{NewCommits: true}, got)
}

func TestBranchCommitsOfAnotherRemoteBranchAreNotNew(t *testing.T) {
	r := newRemote(t)
	git(t, r.seed, "checkout", "-q", "-b", "other")
	commit(t, r.seed, "other.txt")
	git(t, r.seed, "push", "-q", "origin", "other")
	w, dir := prepared(t, r, workspace.Options{})
	git(t, dir, "reset", "-q", "--hard", "origin/other")

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, got)
}

func TestVerifyPasses(t *testing.T) {
	r := newRemote(t)
	w, _ := newGit(t, r, workspace.Options{Verify: "test -f README"})
	prepare(t, w, true)

	require.NoError(t, w.Verify(t.Context()))
}

func TestVerifyFails(t *testing.T) {
	r := newRemote(t)
	w, _ := newGit(t, r, workspace.Options{Verify: "echo 2 tests failed >&2; exit 1"})
	prepare(t, w, true)

	err := w.Verify(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "verify")
	assert.Contains(t, err.Error(), "2 tests failed")
}

func TestVerifyEmptyPasses(t *testing.T) {
	r := newRemote(t)
	w, _ := newGit(t, r, workspace.Options{})
	prepare(t, w, true)

	require.NoError(t, w.Verify(t.Context()))
}

func TestPushPushesBranch(t *testing.T) {
	r := newRemote(t)
	w, dir := newGit(t, r, workspace.Options{})
	prepare(t, w, true)
	commit(t, dir, "fix.txt")

	require.NoError(t, w.Push(t.Context()))

	assert.True(t, r.hasBranchFile(t, "fix.txt"))

	got, err := w.NewCommitsCheck(t.Context())
	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{}, got)
}

func TestPushRejectedWhenRemoteAhead(t *testing.T) {
	r := newRemote(t)
	r.pushBranch(t, "one.txt")
	w, dir := newGit(t, r, workspace.Options{})
	prepare(t, w, true)
	commit(t, dir, "local.txt")
	r.pushBranch(t, "remote.txt")

	err := w.Push(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "push")
	assert.False(t, r.hasBranchFile(t, "local.txt"))
	assert.True(t, r.hasBranchFile(t, "remote.txt"))
}

func TestPushPushesCommitsWithAChangedWorkingTree(t *testing.T) {
	r := newRemote(t)
	w, dir := prepared(t, r, workspace.Options{})
	commit(t, dir, "fix.txt")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("edited"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), nil, 0o600))

	require.NoError(t, w.Push(t.Context()))

	assert.True(t, r.hasBranchFile(t, "fix.txt"))
	assert.False(t, r.hasBranchFile(t, "new.txt"))
}

func TestPushFailsOnAnotherBranch(t *testing.T) {
	r := newRemote(t)
	w, dir := newGit(t, r, workspace.Options{})
	prepare(t, w, true)
	git(t, dir, "checkout", "-q", "main")

	err := w.Push(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), issueBranch)
}

func TestDeadlineCutsSetupAndKillsChildren(t *testing.T) {
	r := newRemote(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	w, _ := newGit(t, r, workspace.Options{
		Setup: "trap '' TERM; sleep 60 & echo $! > " + pidFile + "; while :; do sleep 1; done",
		Grace: 100 * time.Millisecond,
	})

	clk := systemtest.NewClock(time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC))

	ctx, cancel := clk.WithDeadline(t.Context(), clk.Now().Add(time.Minute))
	defer cancel()

	cut := make(chan time.Time, 1)

	go func() {
		assert.Eventually(t, func() bool {
			raw, err := os.ReadFile(pidFile)

			return err == nil && len(strings.TrimSpace(string(raw))) > 0
		}, 30*time.Second, 10*time.Millisecond, "setup did not start")

		cut <- time.Now()

		clk.Advance(time.Minute)
	}()

	_, err := w.Prepare(ctx, true)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "setup")
	assert.Less(t, time.Since(<-cut), 5*time.Second)
	assertGone(t, readPID(t, pidFile))
}

// aliasURL is a clone URL that only the AuthHeader config entry of authed rewrites to the remote.
const aliasURL = "https://git.example.invalid/group/project.git"

// authKey is the git config key that rewrites aliasURL to r.
func authKey(r remote) string { return "url." + r.url + ".insteadOf" }

// authed returns opts with a RepoURL that resolves only through AuthHeader.
func authed(r remote, opts workspace.Options) workspace.Options {
	opts.RepoURL = aliasURL
	opts.Branch = issueBranch
	opts.AuthHeader = workspace.AuthHeader{ConfigKey: authKey(r), ConfigValue: aliasURL}

	return opts
}

func TestAuthHeaderReachesEveryGitCommand(t *testing.T) {
	r := newRemote(t)
	opts := authed(r, workspace.Options{Dir: filepath.Join(t.TempDir(), "repo")})
	w := workspace.New(opts)

	prepare(t, w, true)
	commit(t, opts.Dir, "fix.txt")
	require.NoError(t, w.Push(t.Context()))
	r.pushBranch(t, "remote.txt")
	prepare(t, w, false)

	assert.True(t, r.hasBranchFile(t, "fix.txt"))
	assert.FileExists(t, filepath.Join(opts.Dir, "remote.txt"))
}

func TestAuthHeaderIsNotInSetupOrVerify(t *testing.T) {
	r := newRemote(t)
	out := t.TempDir()
	probe := func(name string) string {
		return "{ git config --get '" + authKey(r) + "' || echo none; } > " + filepath.Join(out, name)
	}
	opts := authed(r, workspace.Options{
		Dir: filepath.Join(t.TempDir(), "repo"), Setup: probe("setup"), Verify: probe("verify"),
	})
	w := workspace.New(opts)

	prepare(t, w, true)
	require.NoError(t, w.Verify(t.Context()))

	for _, name := range []string{"setup", "verify"} {
		got, err := os.ReadFile(filepath.Join(out, name))
		require.NoError(t, err)
		assert.Equal(t, "none\n", string(got), name)
	}
}

func TestAuthHeaderKeepsInheritedGitConfig(t *testing.T) {
	r := newRemote(t)
	opts := authed(r, workspace.Options{Dir: filepath.Join(t.TempDir(), "repo")})
	w := workspace.New(opts)
	prepare(t, w, true)
	commit(t, opts.Dir, "fix.txt")
	require.NoError(t, os.WriteFile(filepath.Join(opts.Dir, "build"+inheritedIgnored), nil, 0o600))

	got, err := w.NewCommitsCheck(t.Context())

	require.NoError(t, err)
	assert.Equal(t, harness.BranchState{NewCommits: true}, got)
}

func readPID(t *testing.T, path string) int {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)

	return pid
}

func TestPrepareKillsWhatSetupLeftRunning(t *testing.T) {
	r := newRemote(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	w, _ := newGit(t, r, workspace.Options{Setup: "sleep 20 & echo $! > " + pidFile, Grace: 3 * time.Second})

	start := time.Now()

	prepare(t, w, true)
	assert.Less(t, time.Since(start), 2*time.Second)

	assertGone(t, readPID(t, pidFile))
}

// assertGone fails the test, and kills pid, when pid still runs after 2 s. A gone pid is never signalled: it may
// belong to another process by then.
func assertGone(t *testing.T, pid int) {
	t.Helper()

	if !assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}, 2*time.Second, 10*time.Millisecond, "process %d still runs", pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestPrepareReturnsWhileAProcessOutsideTheGroupHoldsTheOutput(t *testing.T) {
	r := newRemote(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	w, _ := newGit(t, r, workspace.Options{Setup: "set -m; sleep 30 & echo $! > " + pidFile, Grace: 30 * time.Second})

	t.Cleanup(func() { _ = syscall.Kill(readPID(t, pidFile), syscall.SIGKILL) })

	start := time.Now()

	prepare(t, w, true)
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestCancelledContextFailsBeforeCommand(t *testing.T) {
	r := newRemote(t)
	w, dir := newGit(t, r, workspace.Options{})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := w.Prepare(ctx, true)

	require.ErrorIs(t, err, context.Canceled)
	assert.NoDirExists(t, dir)
}
