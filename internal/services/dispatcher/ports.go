package dispatcher

import (
	"context"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// GitLab is the dispatcher's view of the Git provider.
//
// CurrentUser returns the username of the token's user: the bot user. Its notes are never comments.
//
// Refresh returns prev with the issue fields read again and the notes created or edited since prev was read; a
// note deleted since then stays.
//
// Write finds the status note and the marker notes in known; nil makes it read them. A failed Write can have
// applied part of w; a retry passes nil.
//
// CanTrigger reports whether username has the trigger role or higher on the project. An empty username and a
// project whose members are not shown to the bot user mean false.
//
// Errors:
//   - Every call: models.ErrIdentityChanged when the token belongs to another user than the bot user.
//   - CurrentUser: models.ErrNoIdentity when the Git provider names no user; models.ErrUnauthorized when it does
//     not accept the token; models.ErrForbidden when it refuses the token.
//   - Load, Refresh, Write: models.ErrNotFound when the issue or its project does not exist;
//     models.ErrForbidden when the Git provider refuses the bot user's token.
//   - NoteMergeRequest, CheckProject: models.ErrNotFound when the project does not exist; models.ErrForbidden
//     when the Git provider refuses the bot user's token.
//   - Any other error is a failed call; the dispatcher retries it.
type GitLab interface {
	CurrentUser(ctx context.Context) (string, error)
	Load(ctx context.Context, key models.IssueKey) (models.IssueView, error)
	Refresh(ctx context.Context, prev models.IssueView) (models.IssueView, error)
	Write(ctx context.Context, key models.IssueKey, known *models.IssueView, w models.Writes) error
	CanTrigger(ctx context.Context, projectID int64, username string) (bool, error)
	NoteMergeRequest(ctx context.Context, projectID int64, sourceBranch string, n models.NewNote) error
	CheckProject(ctx context.Context, projectID int64) error
}

// Substrate is the dispatcher's view of the actor runtime. Create makes the actor of spec named name; parts
// that exist are kept.
//
// Errors:
//   - Inspect, List, Delete: a missing actor or part is not an error.
//   - Create: models.ErrInvalidSpec when the spec does not render into an actor; the dispatcher does not retry it.
//   - Resume: models.ErrActorNotFound when the actor does not exist; models.ErrNoRoom when no worker has room.
//   - Suspend, Revert: models.ErrActorNotFound when the actor does not exist.
//   - Any other error is a failed call; the dispatcher retries it.
type Substrate interface {
	Inspect(ctx context.Context, name string) (models.ActorView, error)
	List(ctx context.Context, prefix string) ([]models.ActorSummary, error)
	Create(ctx context.Context, name string, spec models.ActorSpec) error
	Resume(ctx context.Context, name string) error
	Suspend(ctx context.Context, name string) error
	Revert(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
}

// Profiles is profile resolution: the checked profile entry of a project, read from the profiles file at each call.
// Project paths match the project mapping case-insensitively; a project without one gets the default profile.
//
// Errors: a *models.ProfileError (models.ErrAgentProfile) when the file is missing or does not parse, the project
// has no profile, its profile is not defined or breaks a rule; the dispatcher does not retry it.
type Profiles interface {
	Resolve(projectPath string) (models.ProfileEntry, error)
}

// Clock is time for the dispatcher: polls, retries and turn timeouts.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}
