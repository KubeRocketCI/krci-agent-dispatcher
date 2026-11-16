package models

import (
	"errors"
	"regexp"
	"slices"
)

// ActorState is the Substrate actor state as the dispatcher sees it.
type ActorState string

// Actor states. Resuming, Suspending, Reverting, Deleting and Unspecified are transitional: only the call that
// started them, a delete, or Substrate moves them on. Unspecified: Substrate named no state, or one the dispatcher
// does not list. Unknown: the actor read failed; it is never taken for None.
const (
	None        ActorState = "none"
	Running     ActorState = "running"
	Suspended   ActorState = "suspended"
	Crashed     ActorState = "crashed"
	Resuming    ActorState = "resuming"
	Suspending  ActorState = "suspending"
	Reverting   ActorState = "reverting"
	Deleting    ActorState = "deleting"
	Unspecified ActorState = "unspecified"
	Unknown     ActorState = "unknown"
)

var actorStates = []ActorState{
	None, Running, Suspended, Crashed, Resuming, Suspending, Reverting, Deleting, Unspecified, Unknown,
}

// ActorStates returns every actor state.
func ActorStates() []ActorState { return slices.Clone(actorStates) }

// Settled reports whether s is none, suspended or crashed: the actor holds no worker and none is coming.
func (s ActorState) Settled() bool { return s == None || s == Suspended || s == Crashed }

// Transitional reports whether s is resuming, suspending, reverting, deleting or unspecified.
func (s ActorState) Transitional() bool {
	return s == Resuming || s == Suspending || s == Reverting || s == Deleting || s == Unspecified
}

// ActorSummary is one actor of a list: its name and actor state.
type ActorSummary struct {
	Name  string
	State ActorState
}

// ActorView is the Substrate side of one issue: which parts exist and the actor state. Session, Profile and Image are
// the SESSION_ID, the AGENT_PROFILE and the image of the actor template; empty without a template.
type ActorView struct {
	Template     bool
	Actor        bool
	EgressPolicy bool
	State        ActorState
	UID          string
	Pod          string
	Session      string
	Profile      string
	Image        string
}

// Complete reports whether the actor and its egress policy exist; the policy is created last.
func (v ActorView) Complete() bool { return v.Actor && v.EgressPolicy }

// Partial reports a create that stopped before the egress policy.
func (v ActorView) Partial() bool { return (v.Template || v.Actor) && !v.EgressPolicy }

// Exists reports whether any part exists.
func (v ActorView) Exists() bool { return v.Template || v.Actor || v.EgressPolicy }

// ActorSpec is what the actor of one issue is created with: the issue, the session, the bot user and the profile
// entry of the project.
type ActorSpec struct {
	ProjectID   int64
	ProjectPath string
	IID         int64
	SessionID   string
	BotUsername string
	Profile     ProfileEntry
}

var sessionIDRe = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// ValidSessionID reports whether id matches [0-9A-Za-z-]+, the session of a turn marker.
func ValidSessionID(id string) bool { return sessionIDRe.MatchString(id) }

// Validate returns an error naming the first empty or malformed field of s; ProjectID and IID must be positive,
// SessionID a valid session ID, Profile a valid profile entry.
func (s ActorSpec) Validate() error {
	if rules := s.Profile.Check(); len(rules) > 0 {
		return errors.New("profile " + rules[0])
	}

	switch {
	case s.ProjectID <= 0:
		return errors.New("project ID is not positive")
	case s.ProjectPath == "":
		return errors.New("project path is empty")
	case s.IID <= 0:
		return errors.New("issue IID is not positive")
	case s.SessionID == "":
		return errors.New("session ID is empty")
	case !ValidSessionID(s.SessionID):
		return errors.New("session ID is not [0-9A-Za-z-]+")
	case s.BotUsername == "":
		return errors.New("bot username is empty")
	}

	return nil
}
