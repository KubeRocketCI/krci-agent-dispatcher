// Package workspace holds the Git adapter of the harness Workspace port: a git checkout of the project on the issue
// branch, driven by the git and sh commands of the running process.
//
// Every command runs with procgroup.Run under the call's context: when the context ends, the group gets TERM, then
// KILL after the grace, and the call returns an error that wraps the context's error; when the command exits, the
// rest of its group is killed. Errors name the step and end with a tail of the command's output.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/procgroup"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
)

// DefaultGrace is the time between TERM and KILL of a cut command when Options.Grace is zero.
const DefaultGrace = 5 * time.Second

// tailBytes is the most command output an error carries.
const tailBytes = 512

// Options configure a Git workspace.
type Options struct {
	// RepoURL is the clone URL, without credentials.
	RepoURL string
	// Dir is the checkout directory. A fresh Prepare removes it first.
	Dir string
	// Branch is the issue branch.
	Branch string
	// Setup runs with sh -c in Dir after every clone; empty is no step.
	Setup string
	// Verify runs with sh -c in Dir on Verify; empty is no step.
	Verify string
	// Stderr gets the output of every command; nil is io.Discard.
	Stderr io.Writer
	// Grace is the time between TERM and KILL of a command's process group when the context ends; zero is
	// DefaultGrace.
	Grace time.Duration
	// AuthHeader is the git config entry of the Git provider credentials; zero is none.
	AuthHeader AuthHeader
}

// AuthHeader is one git config entry that every git command of the adapter gets through GIT_CONFIG_COUNT,
// GIT_CONFIG_KEY_<n> and GIT_CONFIG_VALUE_<n>, after the entries of the inherited environment. The Setup and Verify
// steps do not get it. Set when ConfigKey is not empty.
type AuthHeader struct {
	// ConfigKey is the config key, such as http.https://gitlab.example.com/.extraHeader.
	ConfigKey string
	// ConfigValue is the config value, such as "Authorization: Basic placeholder".
	ConfigValue string
}

// Git is the Workspace of one checkout. Its methods are not safe for concurrent use.
type Git struct {
	opts Options
}

// New returns the Git workspace of opts.
func New(opts Options) *Git {
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	if opts.Grace <= 0 {
		opts.Grace = DefaultGrace
	}

	return &Git{opts: opts}
}

// Prepare with fresh, or without a checkout in Dir, removes Dir, clones RepoURL into it, checks out Branch (tracking
// origin/Branch when it exists, else new from the cloned HEAD) and runs Setup. Otherwise it tidies the checkout:
// aborts an unfinished rebase, merge, cherry-pick or revert (Prepared.Aborted), stashes modified and staged tracked
// files (Prepared.Stashed, the stash commit id) and checks out Branch when HEAD is another branch or detached.
// Untracked files stay. Then it fetches origin and, when origin/Branch exists, fast-forwards Branch to it, or rebases
// the local commits onto it when both have commits the other lacks. A rebase that conflicts is aborted; HEAD is kept
// on the new branch <Branch>-unpushed-<n>, n the smallest free from 1, Branch is reset to origin/Branch and
// Prepared.SetAside names that branch. A rebase that does not start fails Prepare. Then Prepared.Untracked lists the
// untracked paths that .gitignore does not exclude. The committer of the stash and the rebase comes from the
// environment.
func (g *Git) Prepare(ctx context.Context, fresh bool) (harness.Prepared, error) {
	if !fresh {
		ok, err := g.hasCheckout()
		if err != nil {
			return harness.Prepared{}, err
		}

		if ok {
			return g.refresh(ctx)
		}
	}

	return harness.Prepared{}, g.clone(ctx)
}

// refresh tidies the checkout, then updates Branch.
func (g *Git) refresh(ctx context.Context) (harness.Prepared, error) {
	aborted, err := g.abortOperations(ctx)
	if err != nil {
		return harness.Prepared{}, err
	}

	stashed, err := g.stash(ctx)
	if err != nil {
		return harness.Prepared{}, err
	}

	if err := g.switchBack(ctx); err != nil {
		return harness.Prepared{}, err
	}

	prep, err := g.update(ctx)
	prep.Aborted, prep.Stashed = aborted, stashed

	if err != nil {
		return prep, err
	}

	prep.Untracked, err = g.untracked(ctx)

	return prep, err
}

// operations are the git operations a checkout can be left in, each with a path under the git dir that exists
// while it is unfinished. Each is aborted with git <name> --abort.
var operations = []struct{ name, state string }{
	{"rebase", "rebase-merge"},
	{"rebase", "rebase-apply"},
	{"merge", "MERGE_HEAD"},
	{"cherry-pick", "CHERRY_PICK_HEAD"},
	{"revert", "REVERT_HEAD"},
}

// abortOperations aborts every unfinished operation and returns their names.
func (g *Git) abortOperations(ctx context.Context) ([]string, error) {
	var aborted []string

	for _, op := range operations {
		on, err := g.gitPathExists(ctx, op.state)
		if err != nil {
			return aborted, err
		}

		if !on {
			continue
		}

		if err := g.git(ctx, op.name, "--abort"); err != nil {
			return aborted, err
		}

		aborted = append(aborted, op.name)
	}

	return aborted, nil
}

// stash stashes modified and staged tracked files and returns the stash commit id; empty when none changed.
func (g *Git) stash(ctx context.Context) (string, error) {
	changed, err := g.trackedChanged(ctx)
	if err != nil || changed == 0 {
		return "", err
	}

	if err := g.git(ctx, "stash", "push", "-q", "-m", "krci-agent: uncommitted changes before a turn"); err != nil {
		return "", err
	}

	return g.gitOutput(ctx, "rev-parse", "stash@{0}")
}

// switchBack checks out Branch when HEAD is another branch or detached.
func (g *Git) switchBack(ctx context.Context) error {
	cur, err := g.current(ctx)
	if err != nil || cur == g.opts.Branch {
		return err
	}

	return g.git(ctx, "checkout", "-q", g.opts.Branch, "--")
}

// NewCommitsCheck is the new-commits check. It fails when the checkout is not on Branch, detached HEAD included, when
// tracked files are modified or staged, and, with new commits, when there are untracked files that .gitignore does not
// exclude; that error names the first namedPaths of them. NewCommits: HEAD has commits that no remote-tracking ref has.
func (g *Git) NewCommitsCheck(ctx context.Context) (harness.BranchState, error) {
	st, err := g.newCommits(ctx)
	if err != nil {
		return harness.BranchState{}, fmt.Errorf("new-commits check: %w", err)
	}

	return st, nil
}

func (g *Git) newCommits(ctx context.Context) (harness.BranchState, error) {
	if err := g.onBranch(ctx); err != nil {
		return harness.BranchState{}, err
	}

	changed, err := g.trackedChanged(ctx)
	if err != nil {
		return harness.BranchState{}, err
	}

	if changed > 0 {
		return harness.BranchState{}, fmt.Errorf("tracked files changed in %d paths", changed)
	}

	out, err := g.gitOutput(ctx, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return harness.BranchState{}, err
	}

	n, err := strconv.Atoi(out)
	if err != nil {
		return harness.BranchState{}, fmt.Errorf("rev-list: count %q: %w", out, err)
	}

	if n > 0 {
		paths, err := g.untracked(ctx)
		if err != nil {
			return harness.BranchState{}, err
		}

		if len(paths) > 0 {
			return harness.BranchState{}, fmt.Errorf("untracked files in %d paths: %s", len(paths), firstPaths(paths))
		}
	}

	return harness.BranchState{NewCommits: n > 0}, nil
}

// untracked lists the untracked paths that .gitignore does not exclude, an untracked directory as one path.
func (g *Git) untracked(ctx context.Context) ([]string, error) {
	out, err := g.gitOutput(ctx, "ls-files", "--others", "--exclude-standard", "--directory", "--no-empty-directory")
	if err != nil || out == "" {
		return nil, err
	}

	return strings.Split(out, "\n"), nil
}

// namedPaths is the most paths an error names.
const namedPaths = 5

// firstPaths joins the first namedPaths of paths with ", "; more paths end it with ", …".
func firstPaths(paths []string) string {
	if len(paths) <= namedPaths {
		return strings.Join(paths, ", ")
	}

	return strings.Join(paths[:namedPaths], ", ") + ", …"
}

// trackedChanged is the number of tracked paths that are modified or staged.
func (g *Git) trackedChanged(ctx context.Context) (int, error) {
	out, err := g.gitOutput(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return 0, err
	}

	return lines(out), nil
}

// lines is the number of lines of trimmed out.
func lines(out string) int {
	if out == "" {
		return 0
	}

	return strings.Count(out, "\n") + 1
}

// Verify runs the Verify step; it fails on a non-zero exit.
func (g *Git) Verify(ctx context.Context) error {
	return g.shell(ctx, "verify", g.opts.Verify)
}

// Push fails when the checkout is not on Branch, then pushes HEAD to Branch on origin, never forced: a remote Branch
// with commits HEAD lacks rejects the push. The working tree is not checked.
func (g *Git) Push(ctx context.Context) error {
	if err := g.onBranch(ctx); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	return g.git(ctx, "push", "origin", "HEAD:refs/heads/"+g.opts.Branch)
}

func (g *Git) clone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clone: %w", err)
	}

	if err := os.RemoveAll(g.opts.Dir); err != nil {
		return fmt.Errorf("clone: remove checkout: %w", err)
	}

	if err := g.run(ctx, "clone", g.gitCmd("", "clone", "--", g.opts.RepoURL, g.opts.Dir), nil); err != nil {
		return err
	}

	exists, err := g.remoteBranch(ctx)
	if err != nil {
		return err
	}

	args := []string{"checkout", "-q", "-b", g.opts.Branch}
	if exists {
		args = append(args, "--track", "origin/"+g.opts.Branch)
	}

	if err := g.git(ctx, args...); err != nil {
		return err
	}

	return g.shell(ctx, "setup", g.opts.Setup)
}

// update fetches origin and brings Branch up to origin/Branch when it exists: fast-forward when HEAD is behind,
// nothing when HEAD is ahead or equal, else a rebase of the local commits.
func (g *Git) update(ctx context.Context) (harness.Prepared, error) {
	if err := g.git(ctx, "fetch", "origin"); err != nil {
		return harness.Prepared{}, err
	}

	exists, err := g.remoteBranch(ctx)
	if err != nil || !exists {
		return harness.Prepared{}, err
	}

	remote := "origin/" + g.opts.Branch

	behind, err := g.ancestor(ctx, "HEAD", remote)
	if err != nil {
		return harness.Prepared{}, err
	}

	if behind {
		return harness.Prepared{}, g.gitStep(ctx, "fast-forward", "merge", "--ff-only", remote)
	}

	ahead, err := g.ancestor(ctx, remote, "HEAD")
	if err != nil || ahead {
		return harness.Prepared{}, err
	}

	return g.rebase(ctx, remote)
}

// rebase rebases the local commits onto remote. A rebase that stops is aborted, HEAD is kept on the set-aside branch
// and Branch is reset to remote. A rebase that does not start, such as on untracked files it would overwrite, fails
// Prepare.
func (g *Git) rebase(ctx context.Context, remote string) (harness.Prepared, error) {
	err := g.git(ctx, "rebase", "-q", remote)
	if err == nil {
		return harness.Prepared{}, nil
	}

	if ctx.Err() != nil {
		return harness.Prepared{}, err
	}

	stopped, serr := g.rebasing(ctx)
	if serr != nil || !stopped {
		return harness.Prepared{}, errors.Join(err, serr)
	}

	if err := g.git(ctx, "rebase", "--abort"); err != nil {
		return harness.Prepared{}, err
	}

	name, err := g.setAsideName(ctx)
	if err != nil {
		return harness.Prepared{}, err
	}

	if err := g.gitStep(ctx, "set aside", "branch", "--", name, "HEAD"); err != nil {
		return harness.Prepared{}, err
	}

	if err := g.gitStep(ctx, "reset", "reset", "-q", "--hard", remote); err != nil {
		return harness.Prepared{}, err
	}

	return harness.Prepared{SetAside: name}, nil
}

// ancestor reports whether a is an ancestor of b or equal to it.
func (g *Git) ancestor(ctx context.Context, a, b string) (bool, error) {
	err := g.git(ctx, "merge-base", "--is-ancestor", a, b)

	var exit *exec.ExitError
	if err != nil && ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}

	return err == nil, err
}

// rebasing reports whether a rebase is in progress in the checkout.
func (g *Git) rebasing(ctx context.Context) (bool, error) {
	for _, state := range []string{"rebase-merge", "rebase-apply"} {
		on, err := g.gitPathExists(ctx, state)
		if err != nil || on {
			return on, err
		}
	}

	return false, nil
}

// gitPathExists reports whether the path name under the git dir exists.
func (g *Git) gitPathExists(ctx context.Context, name string) (bool, error) {
	path, err := g.gitOutput(ctx, "rev-parse", "--git-path", name)
	if err != nil {
		return false, err
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(g.opts.Dir, path)
	}

	_, err = os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("git state %s: %w", name, err)
	}

	return true, nil
}

// setAsideName is <Branch>-unpushed-<n> with the smallest n >= 1 whose branch does not exist.
func (g *Git) setAsideName(ctx context.Context) (string, error) {
	prefix := "refs/heads/" + g.opts.Branch + "-unpushed-"

	out, err := g.gitOutput(ctx, "for-each-ref", "--format=%(refname)", prefix+"*")
	if err != nil {
		return "", err
	}

	taken := map[string]bool{}
	for line := range strings.Lines(out) {
		taken[strings.TrimSpace(line)] = true
	}

	n := 1
	for taken[prefix+strconv.Itoa(n)] {
		n++
	}

	return g.opts.Branch + "-unpushed-" + strconv.Itoa(n), nil
}

func (g *Git) hasCheckout() (bool, error) {
	_, err := os.Stat(filepath.Join(g.opts.Dir, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("checkout: %w", err)
	}

	return true, nil
}

// onBranch fails unless HEAD is Branch.
func (g *Git) onBranch(ctx context.Context) error {
	cur, err := g.current(ctx)
	if err != nil {
		return err
	}

	switch cur {
	case g.opts.Branch:
		return nil
	case "":
		return fmt.Errorf("checkout has a detached HEAD, not %q", g.opts.Branch)
	default:
		return fmt.Errorf("checkout is on %q, not %q", cur, g.opts.Branch)
	}
}

// current is the branch of HEAD, empty for a detached HEAD.
func (g *Git) current(ctx context.Context) (string, error) {
	return g.gitOutput(ctx, "branch", "--show-current")
}

func (g *Git) remoteBranch(ctx context.Context) (bool, error) {
	ref := "refs/remotes/origin/" + g.opts.Branch

	out, err := g.gitOutput(ctx, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return false, err
	}

	for line := range strings.Lines(out) {
		if strings.TrimSpace(line) == ref {
			return true, nil
		}
	}

	return false, nil
}

func (g *Git) shell(ctx context.Context, step, command string) error {
	if command == "" {
		return nil
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = g.opts.Dir
	cmd.Env = baseEnv()

	return g.run(ctx, step, cmd, nil)
}

// git runs git args in Dir; the step is the subcommand.
func (g *Git) git(ctx context.Context, args ...string) error {
	return g.gitStep(ctx, args[0], args...)
}

func (g *Git) gitStep(ctx context.Context, step string, args ...string) error {
	return g.run(ctx, step, g.gitCmd(g.opts.Dir, args...), nil)
}

// gitOutput returns the trimmed stdout of git args in Dir; the step is the subcommand.
func (g *Git) gitOutput(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer

	if err := g.run(ctx, args[0], g.gitCmd(g.opts.Dir, args...), &out); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

// gitCmd is git args in dir, with AuthHeader appended to the inherited GIT_CONFIG_* entries when set.
func (g *Git) gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = baseEnv()

	if h := g.opts.AuthHeader; h.ConfigKey != "" {
		n, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
		if err != nil || n < 0 {
			n = 0
		}

		i := strconv.Itoa(n)
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_KEY_"+i+"="+h.ConfigKey,
			"GIT_CONFIG_VALUE_"+i+"="+h.ConfigValue,
			"GIT_CONFIG_COUNT="+strconv.Itoa(n+1),
		)
	}

	return cmd
}

// baseEnv is the inherited environment with git prompts off.
func baseEnv() []string { return append(os.Environ(), "GIT_TERMINAL_PROMPT=0") }

// run runs cmd with procgroup.Run; stdout nil sends stdout with stderr to Options.Stderr. Errors name step.
func (g *Git) run(ctx context.Context, step string, cmd *exec.Cmd, stdout io.Writer) error {
	tail := &tailWriter{}
	output := io.MultiWriter(g.opts.Stderr, tail)

	if stdout == nil {
		stdout = output
	}

	cmd.Stdout = stdout
	cmd.Stderr = output

	err := procgroup.Run(ctx, cmd, procgroup.Options{Grace: g.opts.Grace})
	if err == nil {
		return nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}

	return fmt.Errorf("%s: %w%s", step, err, tail.suffix())
}

// tailWriter keeps the last tailBytes written.
type tailWriter struct {
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > tailBytes {
		w.buf = w.buf[len(w.buf)-tailBytes:]
	}

	return len(p), nil
}

// suffix is the tail as one line after ": ", or empty without output.
func (w *tailWriter) suffix() string {
	text := strings.TrimSpace(strings.ToValidUTF8(string(w.buf), ""))
	if text == "" {
		return ""
	}

	return ": " + strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " | ")), " ")
}
