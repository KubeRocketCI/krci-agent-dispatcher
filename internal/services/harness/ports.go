package harness

import (
	"context"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// GitLab is the harness's view of the Git provider. Load, Refresh and PostReply act on the issue key; the rules of
// the dispatcher's GitLab port apply: Refresh keeps a note deleted since the last read, Load does not. DefaultBranch
// and EnsureMergeRequest get the watchdog context, as for Agent. EnsureMergeRequest opens the merge request of
// mr.Source in the project, or leaves an open one of that source branch as it is.
//
// Errors: models.ErrNotFound when the issue or its project does not exist; models.ErrForbidden when the Git
// provider refuses the token. Any other error is a failed call; the harness retries it on the next poll.
type GitLab interface {
	Load(ctx context.Context, key models.IssueKey) (models.IssueView, error)
	Refresh(ctx context.Context, prev models.IssueView) (models.IssueView, error)
	PostReply(ctx context.Context, key models.IssueKey, body string) (models.Note, error)
	DefaultBranch(ctx context.Context, projectID int64) (string, error)
	EnsureMergeRequest(ctx context.Context, projectID int64, mr models.NewMergeRequest) error
}

// Agent runs the agent once over a prompt. It returns the agent's result text and the exit code of the agent run;
// an error means the run gave no usable exit code.
//
// Every call gets the watchdog context. Its deadline comes from Clock.WithDeadline. An adapter sends TERM 30 s
// before the deadline and KILL at it. A call cut at TERM returns an error that wraps context.DeadlineExceeded; the
// turn loop maps it, and any error returned after the deadline, to exit 124.
//
// Fresh reports whether the next Run starts a new conversation: no run of the session started one yet.
type Agent interface {
	Fresh() bool
	Run(ctx context.Context, prompt string) (text string, exitCode int, err error)
}

// BranchState is the issue branch after the agent run. NewCommits: HEAD has commits that no remote branch has.
type BranchState struct {
	NewCommits bool
}

// Prepared is what Prepare moved out of the way of the update and what it left, each empty when none. Aborted names the
// git operations left unfinished in the checkout that Prepare aborted, such as "merge". Stashed is the commit id of the
// stash entry that holds the changed tracked files. SetAside names the local branch that holds unpushed commits Prepare
// moved off the issue branch. Untracked names the untracked paths that .gitignore does not exclude, an untracked
// directory as one path, after a fetch.
type Prepared struct {
	Aborted   []string
	Stashed   string
	SetAside  string
	Untracked []string
}

// Workspace is the checkout of the project on the issue branch: prepare, the new-commits check, verify and push.
//
// Every call gets the watchdog context, as for Agent. Prepare clones the project when fresh, replacing any existing
// checkout, checks out the issue branch (the remote one when it exists, else a new one from the default branch) and
// runs the setup step. Else it aborts an unfinished rebase, merge, cherry-pick or revert, stashes changed tracked
// files, switches a checkout on another branch back to the issue branch, fetches and brings the issue branch up to
// the remote one: fast-forward, else a rebase of the local commits; a rebase that conflicts is aborted, the local
// commits are kept on a set-aside branch and the issue branch is reset to the remote one; then it names the untracked
// paths. NewCommitsCheck fails on another branch and on changed tracked files, and on untracked files when there are
// new commits; its error names the first untracked paths. Verify runs the verify step. Push pushes the issue branch,
// never forced; it fails on another branch.
type Workspace interface {
	Prepare(ctx context.Context, fresh bool) (Prepared, error)
	NewCommitsCheck(ctx context.Context) (BranchState, error)
	Verify(ctx context.Context) error
	Push(ctx context.Context) error
}

// Clock is time for the harness: polls and deadlines. WithDeadline returns a child of ctx that ends with
// context.DeadlineExceeded at t.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
	WithDeadline(ctx context.Context, t time.Time) (context.Context, context.CancelFunc)
}

// StateStore keeps the harness state of one session. Load returns the zero state when nothing is stored or the
// stored state belongs to another session. Save replaces the stored state.
//
// Errors: Load returns models.ErrStateCorrupt for a state it cannot read; any other error is a failed call.
type StateStore interface {
	Load(session string) (models.HarnessState, error)
	Save(session string, s models.HarnessState) error
}
