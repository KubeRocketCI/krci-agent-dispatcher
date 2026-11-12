# Agent image contract

What the dispatcher gives the agent image and what it reads back. The dispatcher does not build the image. Terms: `GLOSSARY.md`. Decisions: `docs/adr/0003-turn-marker-signal-actor-state-lock.md`, `docs/adr/0004-status-note-record-labels-projection.md`. Rules: `internal/services/decide/decide.go`.

Sources: `internal/substrate/templates/actortemplate.yaml.tmpl`, `internal/substrate/templates/egresspolicy.yaml.tmpl`, `internal/models/marker.go`, `internal/services/decide/decide.go`, `internal/services/dispatcher/loop.go`.

## Channels

- Dispatcher → agent: the actor template (image, command, env, volume), set once at create.
- Agent → dispatcher: the turn marker in an issue note.
- The dispatcher calls Substrate to inspect, list, create, resume, suspend, revert and delete the actor. It sends nothing into a running actor.

## Actor

One actor per issue.

| Item | Value |
|---|---|
| Actor template and actor name | `issue-<project-id>-<iid>` |
| Egress policy name | `default`, owned by the actor |
| Atespace | `ATESPACE` of the dispatcher, required; one dispatcher per atespace |
| Worker selector | `workload: <POOL>` |
| Container | one, named `claude` |
| Image | `AGENT_IMAGE` of the dispatcher (chart value `agentImage`) |
| Command | `/usr/local/bin/agent-loop.sh`, no arguments |
| CPU limit | 1000 millicores |
| Memory limit | 2 GiB |
| Snapshot fidelity | memory |
| Golden snapshot | disabled |

Create order: actor template, actor, egress policy. After create the dispatcher records waiting, then resumes the actor.

## Environment

| Variable | Value |
|---|---|
| `GITLAB_URL` | GitLab base URL, canonical: `https://<host>[:<port>][/<path>]`, lower-case host, no trailing slash |
| `PROJECT_PATH` | Project path with namespace |
| `ISSUE_IID` | Issue IID |
| `BOT_USERNAME` | Bot user name |
| `SESSION_ID` | UUID, new at each create; shown in the status note |
| `POLL_SECONDS` | `15` |
| `GOPROXY` | `https://proxy.golang.org` |
| `GOSUMDB` | `sum.golang.org` |
| `ANTHROPIC_API_KEY` | `placeholder` |
| `ANTHROPIC_MODEL` | `claude-haiku-5-5` |
| `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` | `1` |
| `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO` | `/run/ate/egress-trust-bundle/trust-bundle.pem` |
| `GIT_AUTHOR_NAME`, `GIT_COMMITTER_NAME` | `KRCI Agent` |
| `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_EMAIL` | `krci-agent@example.invalid` |
| `GIT_CONFIG_COUNT` | `1` |
| `GIT_CONFIG_KEY_0` | `http.<GITLAB_URL>/.extraHeader` |
| `GIT_CONFIG_VALUE_0` | `Authorization: Basic placeholder` |

## Volume

| Mount | Content |
|---|---|
| `/run/ate` | Substrate system info. `egress-trust-bundle/trust-bundle.pem`: trust bundle of `egress-mitm.ate.dev` and `system-roots.ate.dev`. |

## Credentials

- The actor template holds no secret value. `ANTHROPIC_API_KEY` and the Git `Authorization` header are placeholders.
- The egress policy sets the real headers on the way out, from `ate-secret://k8s.io/default/<SECRETS_NAMESPACE>/<path>`:

| Host | Header | Secret path |
|---|---|---|
| `api.anthropic.com` | `x-api-key` | `anthropic/api-key` |
| GitLab host | `Authorization: Basic <value>` | `gitlab/basic` |
| GitLab host | `PRIVATE-TOKEN` | `gitlab/token` |

- `gitlab/basic` and `gitlab/token` hold credentials of the bot user (`BOT_USERNAME`). The dispatcher reads a turn marker only from a note of that author.

## Egress

| Rule | Hosts | Port |
|---|---|---|
| HTTPS, headers replaced | `api.anthropic.com` | — |
| HTTPS, headers replaced | host of `GITLAB_URL` | — |
| TLS passthrough | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com` | 443 |

The policy has no other rule.

## Issue branch

- Name: `agent/issue-<iid>`.
- The merge request of the agent has this branch of the project itself (not of a fork) as its source branch.
- On stop the dispatcher posts "Agent stopped; this MR is no longer updated." once on each open merge request with that source branch.

## Turn marker

Format, one line, exact:

```text
<!-- krci-agent-turn n=<n> exit=<rc> last=<note-id> -->
```

| Field | Value |
|---|---|
| `n` | Turn number, decimal. Compared with the turn limit; a marker with a non-zero `exit` counts. |
| `exit` | Result of the agent run, integer, may be negative. Read, not used in a decision. |
| `last` | Highest note id of the comments the turn covered. Optional; without it the note id of the reply is used. |

Rules:

- The marker is in the reply: an issue note, not a system note, with the bot user as author.
- One reply per turn. The reply is posted at the end of the turn. A failed turn also ends in a reply with a marker.
- The last marker in the note body counts. Put the marker after the reply text.
- Markers are ordered by note id, not by `n`. A turn repeated after a revert has the same `n`.
- A marker in a note by any other author is ignored.

## Turn end

- A turn ends when a reply with a turn marker has a note id above the note id of the turn's previous marker.
- The dispatcher reads the notes every `POLL_INTERVAL` (default 10 s).
- After the marker, the first matching case:

| Case | Dispatcher |
|---|---|
| `n` ≥ turn limit (`TURN_LIMIT`, default 20) | Suspends the actor. Record error `turn limit`. |
| A comment by any person has a note id above `last` | Leaves the actor resumed and waits for the next marker; the turn timeout starts again. The dispatcher sends no signal: the agent starts the next turn itself. |
| No such comment | Suspends the actor. Record waiting. |

## Dispatcher actions on the actor

| Event | Action |
|---|---|
| Trigger label set on an open issue by a user with the trigger role or higher (`TRIGGER_MIN_ROLE`, default developer) | Create, then resume. |
| Trigger comment on a suspended actor | Resume. Record working. |
| Trigger comment on a crashed actor | Revert to the durable snapshot, then resume. |
| Trigger comment, last marker `n` ≥ turn limit | No resume. Record error `turn limit`. |
| No free worker at resume | No activation. Note "No free worker. Comment again later." |
| No marker within the turn timeout (`TURN_TIMEOUT`, default 15 m) from the turn start | Suspends a running actor. Record error `turn timeout`. |
| Actor crashed during an activation | Record error `actor crashed`. |
| Actor suspended by another caller during an activation | Record waiting. |
| Record error | No resume. A running actor is suspended. A person removes `agent::error` to clear it. Record error `turn limit` is not cleared: `agent::error` is set again and no activation starts. |
| Issue closed or trigger label removed | Deletes the actor in any actor state. Record done. During an activation: at the next marker, at the turn timeout, or when the actor is no longer running. |
| Record done, actor running (for example `agent::done` set by a person) | Deletes the actor on the next request, restart recovery or sweep. Record done. |
| Issue gone: deleted, its project readable | Discards the actor: deletes it in any actor state; nothing is written to the issue. |
| Issue unavailable: any other failed read | On a request: keeps the actor; a running actor, or one that cannot be read, is suspended; nothing is written to the issue. During an activation: polls on; at the turn timeout suspends the actor and records error `turn timeout`. |
| Actor cannot be read during an activation | Polls on; at the turn timeout suspends the actor and records error `turn timeout`. |
| Dispatcher restart | A running actor keeps running. Restart recovery continues its activation loop and reads the last marker again. |
| Actor running or transitional with no issue lane in two sweeps in a row (`SWEEP_INTERVAL`, default 5 m) | The recover request of restart recovery: the activation loop continues, or the actor is suspended or deleted. |

A suspend can come at any time after the marker is posted. A delete can come at any time.

## The agent must not

- Post a turn marker before the turn's work is complete. The actor can be suspended as soon as the marker is read.
- Post a note whose first line is `<!-- krci-agent-status state=... reason=... -->`. The first such note of the bot user is the status note.
- Edit or delete the status note.
- Post `<!-- krci-agent-refused -->`, `<!-- krci-agent-start-refused -->` or `<!-- krci-agent-stopped -->`. A note with one of these stops the dispatcher from posting its own.
- Close the issue or remove the trigger label. Either one by the bot user stops the agent and deletes the actor.
- Change a state label. The dispatcher ignores any other issue update by the bot user and sets the state label from the record.
- Depend on a host outside the egress policy.
- Open its merge request from a branch other than the issue branch. The stopped note goes only to merge requests of the issue branch.
