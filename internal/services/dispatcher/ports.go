package dispatcher

import (
	"context"
	"time"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// GitLab is the dispatcher's view of the Git provider.
type GitLab interface {
	Load(ctx context.Context, key models.IssueKey) (models.IssueView, error)
	Write(ctx context.Context, key models.IssueKey, w models.Writes) error
	AccessLevel(ctx context.Context, projectID int64, username string) (int, error)
	NoteMergeRequest(ctx context.Context, projectID int64, sourceBranch string, n models.NewNote) error
	CheckProject(ctx context.Context, projectID int64) error
}

// Substrate is the dispatcher's view of the actor runtime.
type Substrate interface {
	Inspect(ctx context.Context, name string) (models.ActorView, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Create(ctx context.Context, name string, tmpl *ateapi.ActorTemplate, pol *ateapi.EgressPolicy) error
	Resume(ctx context.Context, name string) error
	Suspend(ctx context.Context, name string) error
	Revert(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
}

// Clock is time for the dispatcher: polls, retries and turn deadlines.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}
