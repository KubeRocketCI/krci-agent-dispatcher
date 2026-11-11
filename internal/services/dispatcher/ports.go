package dispatcher

import (
	"context"
	"time"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// GitLab is the dispatcher's view of the Git provider.
// Refresh returns prev with the issue fields read again and the notes created or edited since prev was read; a
// note deleted since then stays. Write finds the status note and the marker notes in known; nil makes it read them.
//
// Errors:
//   - Load, Refresh, Write: models.ErrNotFound when the issue or its project does not exist;
//     models.ErrForbidden when the Git provider refuses the bot user's token.
//   - AccessLevel: models.ErrNotFound or models.ErrForbidden when the project members are not shown to the
//     bot user.
//   - NoteMergeRequest, CheckProject: models.ErrNotFound when the project does not exist; models.ErrForbidden
//     when the Git provider refuses the bot user's token.
//   - Any other error is a failed call; the dispatcher retries it.
type GitLab interface {
	Load(ctx context.Context, key models.IssueKey) (models.IssueView, error)
	Refresh(ctx context.Context, prev models.IssueView) (models.IssueView, error)
	Write(ctx context.Context, key models.IssueKey, known *models.IssueView, w models.Writes) error
	AccessLevel(ctx context.Context, projectID int64, username string) (int, error)
	NoteMergeRequest(ctx context.Context, projectID int64, sourceBranch string, n models.NewNote) error
	CheckProject(ctx context.Context, projectID int64) error
}

// Substrate is the dispatcher's view of the actor runtime.
//
// Errors:
//   - Inspect, List, Delete: a missing actor or part is not an error.
//   - Resume: models.ErrActorNotFound when the actor does not exist; models.ErrNoRoom when no worker has room.
//   - Suspend, Revert: models.ErrActorNotFound when the actor does not exist.
//   - Any other error is a failed call; the dispatcher retries it.
type Substrate interface {
	Inspect(ctx context.Context, name string) (models.ActorView, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Create(ctx context.Context, name string, tmpl *ateapi.ActorTemplate, pol *ateapi.EgressPolicy) error
	Resume(ctx context.Context, name string) error
	Suspend(ctx context.Context, name string) error
	Revert(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
}

// Clock is time for the dispatcher: polls, retries and turn timeouts.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}
