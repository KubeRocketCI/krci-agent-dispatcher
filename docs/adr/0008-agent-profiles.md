# Agent profiles

An agent profile describes one kind of agent. What the sandbox needs from outside (agent image, egress hosts, env) is dispatcher config, because the dispatcher creates the actor and its egress policy; what happens inside the sandbox (setup, verify, prompt) is a profile file in the agent image. The dispatcher config maps each project to a profile, with one default profile. Alternatives were a profile label per issue and a profile file in the target repo.

The dispatcher side is one raw YAML file, `profiles.yaml`, in a ConfigMap mounted as a directory. The dispatcher reads it at each actor create: no cache, no watcher, no dispatcher restart. Alternatives were a cache reloaded on change (watcher edge cases, a stale cache) and a restart on change (restart recovery over every actor for a config edit).

A profile that cannot be resolved does not stop the dispatcher: the issue goes to error, reason `agent profile`, and the status note names the profile, when known, and the rule.

## Consequences

- A new stack is a new agent image plus a profile entry; the dispatcher and the harness do not change.
- Egress stays under the platform operator's control.
- One project has one profile.
- A sandbox need is met on the sandbox side (agent image, profile, egress, env), not by dispatcher logic.
- A profile change applies to actors created after it, each with a new session. An open actor keeps its image, env, egress and profile file; a re-enable moves the issue to the current profile.
- One broken profile entry affects only the projects that resolve to it.

## Limitations

Every actor freezes its settings at create; a suspend saves the harness process in a memory snapshot, so the handshake does not run again on resume or revert.

1. A turn limit raised in the dispatcher config: open actors stop at the old `TURN_LIMIT`, and the dispatcher records `turn timeout`.
2. A turn timeout lowered in the dispatcher config: the dispatcher can record `turn timeout` while the harness of an open actor still works under the old watchdog. The harness poll interval splits the same way.
3. A dispatcher upgrade that bumps the contract version: open actors keep the old harness and turn protocol.
4. Security rollback of an image, an egress host or a prompt waits for a stop or re-enable of each open issue. Substrate can replace an egress policy in place (`UpdateActorEgressPolicy`); the dispatcher does not.
5. The status note rows Profile and Image show what an open actor runs.
6. A resume or revert on a node without the image pulls it again: a profile image stays in the registry while an actor uses it.
7. Setup runs once per clone; a changed setup reaches new sessions only.
8. A re-enable ends the session: unpushed commits are lost, the turn limit counts again; pushed work and the turn history stay.
9. Substrate templates are immutable. A suspended actor can move to another template, with a cold boot that keeps only `durableDir` volumes; the actor template has none, so a template swap loses the workspace, the harness state and the conversation.
10. Profile resolution runs in the create effect, after the plan's delete: a re-enable with an unresolved profile deletes the old actor and leaves the issue in error without one.
