# Isolated Agents

AI coding agents for KubeRocketCI that work on GitLab issues inside isolated Agent Substrate actors. A person talks to the agent through issue comments; the agent answers and opens a merge request.

## Work on an issue

**Agent**:
Claude working on one issue inside an actor.
_Avoid_: bot (for the agent), worker

**Turn**:
One round on an issue: the comments since the last reply, then one agent run, then one reply. A run that fails still ends in a reply and is a turn.
_Avoid_: run, iteration, round

**Activation**:
One span of a resumed actor, from resume to suspend; it covers one or more turns.
_Avoid_: turn (for the span), wake, run, pass

**Session**:
The agent's conversation memory for one issue, kept across turns and ended by stop.
_Avoid_: context, chat

**Error**:
The agent state where the agent waits for a person to clear it; its actor is kept.
_Avoid_: failed, stopped

**Agent steps**:
The tool calls and model calls the agent makes inside one turn.
_Avoid_: turns (for these)

**Turn limit**:
The number of turns after which an issue's agent is put in error instead of resumed.
_Avoid_: max turns, cap

**Turn timeout**:
The longest time the dispatcher waits for one turn's reply before it puts the agent in error.
_Avoid_: watchdog (for this), deadline

**Watchdog**:
The in-actor limit on one agent run; a run that hits it still ends in a reply.
_Avoid_: turn timeout (for this)

**Publish**:
Pushing the issue branch and opening or updating its merge request.
_Avoid_: push, deliver

**Issue branch**:
The branch the agent works on for one issue.
_Avoid_: agent branch, feature branch

**Stop**:
The end of the agent for an issue; its actor is deleted.
_Avoid_: suspend, pause, done

**Re-enable**:
Asking for the agent again on an issue whose agent was stopped, by setting the trigger label again or by reopening the issue with it.
_Avoid_: restart

## GitLab

**Trigger label**:
The issue label that asks for an agent (`agent`).
_Avoid_: agent label

**State label**:
The issue label that shows the agent state: `agent::working` (an activation runs), `agent::waiting` (no activation runs), `agent::done` (stopped), `agent::error` (error).
_Avoid_: status label

**Agent state**:
Where the agent of an issue is, as shown by its state label.
_Avoid_: state (alone), status

**Issue state**:
Whether the GitLab issue is open or closed.
_Avoid_: state (alone)

**Bot user**:
The GitLab user that the dispatcher and the agent act as.
_Avoid_: agent (for the user), service account

**Comment**:
An issue note written by a person. The agent reads every comment.
_Avoid_: human note, message

**Trigger comment**:
A comment by a project member with Developer access or higher; only these resume a suspended actor.
_Avoid_: authorized comment, developer comment

**Reply**:
The note the bot user posts at the end of a turn.
_Avoid_: answer, response

**Turn marker**:
The hidden line in a reply that records the turn number, its result and the last comment it covered.

**Status note**:
The one note on an issue that the bot user edits to show the agent state and, in error, the reason. It holds the record.

**Record**:
The bot user's statement of an issue's agent state and error reason; it is the truth for stopped and error, and state labels follow it.
_Avoid_: status, state label (for this)

**Canonical state**:
One of Idle, Working, Waiting, Error, Done: the agent state that the facts of an issue and its actor resolve to.
_Avoid_: status

**Hook**:
The project configuration that sends events to the dispatcher.
_Avoid_: webhook (for the event)

**Event**:
One delivery from GitLab with its payload.
_Avoid_: hook, webhook

## Substrate

**Actor**:
The isolated Substrate sandbox that hosts one agent and keeps its state between turns.
_Avoid_: agent, container, pod

**Suspend**:
Saving an actor to a snapshot and freeing its worker.
_Avoid_: stop, pause

**Actor state**:
The Substrate lifecycle state of an actor, such as running, suspended or crashed.
_Avoid_: state (alone)

**Snapshot**:
The saved state of an actor; the durable snapshot is the one a resume or revert restores.
_Avoid_: checkpoint, image

**Crashed**:
The actor state after its sandbox died on its own; the actor is reverted before its next activation.
_Avoid_: failed, dead

**Revert**:
Returning a crashed or running actor to its durable snapshot, suspended.
_Avoid_: rollback, restore

**Resume**:
Restoring a suspended actor on a free worker.
_Avoid_: start, wake

**Worker**:
A pre-started Substrate pod in the pool that can host a restored actor.
_Avoid_: runner, slot

**Pool**:
The set of workers that actors run on.

**No room**:
The condition where no worker in the pool can host the actor, so an activation does not start.
_Avoid_: capacity error, busy pool

## Dispatcher

**Dispatcher**:
The service that receives events and runs turns.
_Avoid_: waker, loop

**Issue lane**:
The dispatcher's sequence of actions for one issue, which runs one action at a time.
_Avoid_: worker (for this)

**Dispatcher restart**:
A new start of the dispatcher process.
_Avoid_: restart (alone)

**Restart recovery**:
The dispatcher's one-time pass after a dispatcher restart that settles actors left running by an activation in progress.
_Avoid_: reconcile, sync

## Planning

**Phase**:
One step of the Isolated Agents epic (A–E).
_Avoid_: stage
