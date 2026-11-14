package harness

import (
	"context"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// GitLab is the harness's view of the Git provider. Load, Refresh and PostReply act on the issue key; the rules of
// the dispatcher's GitLab port apply: Refresh keeps a note deleted since the last read, Load does not. DefaultBranch
// gets the watchdog context, as for Agent.
//
// Errors: models.ErrNotFound when the issue or its project does not exist; models.ErrForbidden when the Git
// provider refuses the token. Any other error is a failed call; the harness retries it on the next poll.
type GitLab interface {
	Load(ctx context.Context, key models.IssueKey) (models.IssueView, error)
	Refresh(ctx context.Context, prev models.IssueView) (models.IssueView, error)
	PostReply(ctx context.Context, key models.IssueKey, body string) (models.Note, error)
	DefaultBranch(ctx context.Context, projectID int64) (string, error)
}

// Agent runs the agent once over a prompt. It returns the agent's result text and the exit code of the agent run;
// an error means the run gave no usable exit code.
//
// Every call gets the watchdog context. Its deadline comes from Clock.WithDeadline. An adapter sends TERM 30 s
// before the deadline and KILL at it. A call cut at TERM returns an error that wraps context.DeadlineExceeded; the
// turn loop maps it, and any error returned after the deadline, to exit 124.
type Agent interface {
	Run(ctx context.Context, prompt string) (text string, exitCode int, err error)
}

// Workspace is the checkout of the project and its publish steps.
//
// Every call gets the watchdog context, as for Agent. Prepare clones the project and runs the setup when fresh,
// replacing any existing checkout, else fetches. Verify runs the verify step. Publish pushes the issue branch and
// opens or updates its merge request to defaultBranch.
type Workspace interface {
	Prepare(ctx context.Context, fresh bool) error
	Verify(ctx context.Context) error
	Publish(ctx context.Context, defaultBranch string) error
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
