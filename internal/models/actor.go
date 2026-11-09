package models

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
