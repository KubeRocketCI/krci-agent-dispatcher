# Isolated Agents

AI coding agents for KubeRocketCI that work on GitLab issues inside isolated Agent Substrate actors. A person talks to the agent through issue comments; the agent answers and opens a merge request.

## Work on an issue

**Agent**:
Claude working on one issue inside an actor.
_Avoid_: bot (for the agent), worker

**Turn**:
One round on an issue: the comments no turn of the session covered yet, then one agent run, then one reply. A run that fails still ends in a reply and is a turn.
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
The harness's time limit on one turn inside the actor, derived from the turn timeout; it covers clone or fetch, setup, the agent run, verify and publish. A turn that hits it still ends in a reply.
_Avoid_: turn timeout (for this), budget, deadline

**Publish**:
Pushing the issue branch and opening or updating its merge request.
_Avoid_: push, deliver

**Issue branch**:
The branch the agent works on for one issue.
_Avoid_: agent branch, feature branch

**Stop**:
The end of the agent for an issue; its actor is deleted.
_Avoid_: suspend, pause, done

**Discard**:
Deleting the actor of an issue that no longer exists; nothing is written to the issue.
_Avoid_: stop (for this), cleanup

**Gone**:
An issue that GitLab no longer has while its project is still readable; its actor is discarded.
_Avoid_: deleted, missing (for the read outcome)

**Unavailable**:
An issue that cannot be read for any other reason. On a request its actor is kept; during an activation the dispatcher polls on until the turn timeout.
_Avoid_: forbidden, down (for the read outcome)

**Keep**:
Leaving an actor in place, suspended when it runs, with nothing written to the issue.
_Avoid_: hold, park

**Re-enable**:
Asking for the agent again on an issue whose agent was stopped, by setting the trigger label again or by reopening the issue with it.
_Avoid_: restart

**Re-enable cut**:
A re-enable whose record writes did not land: the status note still records done, and the actor's template names a new session. The done record then counts as none, and the issue goes on from the actor.
_Avoid_: stalled re-enable

## GitLab

**Trigger label**:
The issue label that asks for an agent (`krci-agent`).
_Avoid_: agent label

**State label**:
The issue label that shows the agent state: `krci::agent::working` (an activation runs), `krci::agent::waiting` (no activation runs), `krci::agent::done` (stopped), `krci::agent::error` (error).
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

**Trigger role**:
The lowest Git provider role whose users can trigger the agent; on GitLab `developer`, `maintainer` or `owner`, default `developer`.
_Avoid_: access level, min role

**Trigger comment**:
A comment by a user with the trigger role or higher. Only these resume a suspended actor; any comment keeps a resumed actor resumed for the next turn.
_Avoid_: authorized comment, developer comment

**Reply**:
The note the bot user posts at the end of a turn.
_Avoid_: answer, response

**Turn marker**:
The hidden line in a reply that records the session, the turn number, its result and the last comment it covered.

**Covered comment**:
A comment at or below the `last` of a turn marker of the current session; a turn covers the comments it read when it started.
_Avoid_: answered comment, seen comment

**Turn history**:
The replies with turn markers of the current session and the comments on an issue, ordered by note id.
_Avoid_: history (alone), timeline

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

## Harness and agent images

**Harness**:
The agent loop that runs inside every actor: handshake, turns, replies, publish. One binary from this repo, mounted into every actor by the dispatcher.
_Avoid_: agent loop (for the binary), runner, wrapper

**Turn protocol**:
The rules the dispatcher and the harness share: turn marker, comment, turn history, next turn, watchdog limit and contract version.
_Avoid_: agent contract, protocol (alone)

**Handshake**:
The harness's check at start of its env, the contract version, the profile file and the watchdog limit; a failed handshake ends the harness before any turn.
_Avoid_: preflight, init

**Turn loop**:
The harness's repeated poll that runs one turn after another, each under the watchdog.
_Avoid_: activation loop (for this), main loop

**Pending reply**:
A reply the harness built for a turn and has not seen posted; kept until a turn marker with its session and turn number is in the turn history.
_Avoid_: unsent reply, queued reply

**Agent image**:
The image an actor runs: the tools of one kind of agent and the image side of its agent profile. It holds no harness.
_Avoid_: agent (for the image)

**Harness image**:
The OCI image the harness ships in: the static binary at `/krci-agent-harness` and nothing else, mounted read-only at `/opt/krci-agent`. Built from `Dockerfile.harness`.
_Avoid_: agent image (for this), harness container

**Agent image contract**:
What an agent image provides for the harness: `claude`, `git` and `sh` on `PATH`; uid 1000 with writable `HOME` and `/work`; `/opt/krci-agent` free; the profile file, optional. `krci-agent-harness selftest` checks it, except the profile file, with the binary outside `/opt/krci-agent`; the handshake checks the profile file.
_Avoid_: image contract, harness contract

**Agent profile**:
The description of one kind of agent: image, egress and env in the dispatcher config; setup, verify and prompt in the agent image.
_Avoid_: flavour, template (for this)

**Contract version**:
The integer version of the turn protocol, set by the dispatcher in the actor template and checked by the harness at start.
_Avoid_: API version, protocol version

## Substrate

**Actor**:
The isolated Substrate sandbox that hosts one agent and keeps its state between turns.
_Avoid_: agent, container, pod

**Suspend**:
Saving an actor to a snapshot and freeing its worker.
_Avoid_: stop, pause

**Actor state**:
The Substrate lifecycle state of an actor: running, suspended, crashed, or transitional (resuming, suspending, reverting, deleting, or a state Substrate does not name); unknown when the actor cannot be read. An unknown actor is never taken for a missing one.
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

**Plan**:
The ordered effects the dispatcher carries out for one decision on an issue.
_Avoid_: recipe, script, action (for this)

**Effect**:
One read, write or Substrate call in a plan.
_Avoid_: step (for this), action, command

**Issue lane**:
The dispatcher's sequence of actions for one issue, which runs one action at a time.
_Avoid_: worker (for this)

**Activation loop**:
The dispatcher's polling during one activation: it reads the issue and the actor state, waits for the next turn marker and resumes, keeps or suspends the actor.
_Avoid_: poll loop, turn loop (for this)

**Dispatcher restart**:
A new start of the dispatcher process.
_Avoid_: restart (alone)

**Restart recovery**:
The dispatcher's one-time pass after a dispatcher restart that sends a recover request for every actor of an issue, settling actors left running by an activation in progress and re-enable cuts.
_Avoid_: reconcile, sync

**Sweep**:
The dispatcher's periodic pass over actors that settles an actor that is running or transitional with no issue lane, as restart recovery does.
_Avoid_: reconcile, resync, scan, garbage collection
