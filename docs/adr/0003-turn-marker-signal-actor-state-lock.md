# The turn marker is the completion signal; the actor state is the only lock

The dispatcher learns that a turn ended by polling the issue notes for the hidden turn marker that the agent loop posts in its reply, and it resumes only an actor whose Substrate actor state is suspended. Labels and the status note are display only. The alternatives were an actor-initiated signal (Substrate `RequestActorSuspend` is not wired for callers, and there is no state-change stream) or dispatcher-side state (a store or CRD, rejected for Phase A, see ADR 0001).

## Consequences

- The marker format is a protocol between the agent image and the dispatcher; changing it means changing both and every live actor.
- The agent shares the bot user, so it can forge a marker and only end its own activation early.
- After a revert a turn can repeat with the same number, so markers are ordered by note id.
