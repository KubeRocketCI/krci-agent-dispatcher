package models

import (
	"errors"
	"slices"
)

// ActorState is the Substrate actor state as the dispatcher sees it.
type ActorState string

// Actor states. Transitional covers resuming, suspending, deleting and reverting.
const (
	None         ActorState = "none"
	Running      ActorState = "running"
	Suspended    ActorState = "suspended"
	Crashed      ActorState = "crashed"
	Transitional ActorState = "transitional"
)

var actorStates = []ActorState{None, Running, Suspended, Crashed, Transitional}

// ActorStates returns every actor state.
func ActorStates() []ActorState { return slices.Clone(actorStates) }

// ActorView is the Substrate side of one issue: which parts exist and the actor state.
type ActorView struct {
	Template     bool
	Actor        bool
	EgressPolicy bool
	State        ActorState
	UID          string
	Pod          string
}

// Complete reports whether the actor and its egress policy exist; the policy is created last.
func (v ActorView) Complete() bool { return v.Actor && v.EgressPolicy }

// Partial reports a create that stopped before the egress policy.
func (v ActorView) Partial() bool { return (v.Template || v.Actor) && !v.EgressPolicy }

// Exists reports whether any part exists.
func (v ActorView) Exists() bool { return v.Template || v.Actor || v.EgressPolicy }

// ActorSpec is what the actor of one issue is created with: the issue, the session and the bot user.
type ActorSpec struct {
	ProjectPath string
	IID         int64
	SessionID   string
	BotUsername string
}

// Validate returns an error naming the first empty field of s; IID must be positive.
func (s ActorSpec) Validate() error {
	switch {
	case s.ProjectPath == "":
		return errors.New("project path is empty")
	case s.IID <= 0:
		return errors.New("issue IID is not positive")
	case s.SessionID == "":
		return errors.New("session ID is empty")
	case s.BotUsername == "":
		return errors.New("bot username is empty")
	}

	return nil
}
