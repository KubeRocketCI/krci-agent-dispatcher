package models

import "errors"

// Port errors. Adapters wrap them; callers test them with errors.Is.
var (
	// ErrNotFound: the issue or its project does not exist.
	ErrNotFound = errors.New("not found")
	// ErrForbidden: the Git provider refuses the bot user's token for the resource.
	ErrForbidden = errors.New("forbidden")
	// ErrUnauthorized: the Git provider does not accept the token: wrong, expired or revoked.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrActorNotFound: the actor does not exist.
	ErrActorNotFound = errors.New("actor not found")
	// ErrNoRoom: no worker has room for the actor.
	ErrNoRoom = errors.New("no worker has room for the actor")
	// ErrInvalidSpec: the actor spec does not render into an actor.
	ErrInvalidSpec = errors.New("invalid actor spec")
	// ErrAgentProfile: profile resolution failed; the error is a *ProfileError.
	ErrAgentProfile = errors.New("agent profile")
	// ErrStateCorrupt: the harness state store holds a state it cannot read.
	ErrStateCorrupt = errors.New("state corrupt")
	// ErrIdentityChanged: the Git provider token belongs to another user than the bot user in use.
	ErrIdentityChanged = errors.New("bot user changed")
	// ErrNoIdentity: the bot user is not read yet, or the Git provider names no user for the token.
	ErrNoIdentity = errors.New("bot user unknown")
)
