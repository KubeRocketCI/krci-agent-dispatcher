# Agent profiles

An agent profile describes one kind of agent. What the sandbox needs from outside (agent image, egress hosts, env) is dispatcher config, because the dispatcher creates the actor and its egress policy; what happens inside the sandbox (setup, verify, prompt) is a profile file in the agent image. The dispatcher config maps each project to a profile, with one default profile. Alternatives were a profile label per issue and a profile file in the target repo.

## Consequences

- A new stack is a new agent image plus a profile entry; the dispatcher and the harness do not change.
- Egress stays under the platform operator's control.
- One project has one profile.
- A sandbox need is met on the sandbox side (agent image, profile, egress, env), not by dispatcher logic.
