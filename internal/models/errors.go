package models

import "errors"

// Port errors. Adapters wrap them; callers test them with errors.Is.
var (
	// ErrNotFound: the issue or its project does not exist.
	ErrNotFound = errors.New("not found")
	// ErrForbidden: the Git provider refuses the bot user's token for the resource.
	ErrForbidden = errors.New("forbidden")
	// ErrActorNotFound: the actor does not exist.
	ErrActorNotFound = errors.New("actor not found")
	// ErrNoRoom: no worker has room for the actor.
	ErrNoRoom = errors.New("no worker has room for the actor")
	// ErrInvalidSpec: the actor spec does not render into an actor.
	ErrInvalidSpec = errors.New("invalid actor spec")
)
