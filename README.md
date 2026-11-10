# krci-agent-dispatcher

Dispatcher for KubeRocketCI issue agents. A GitLab project hook sends issue and comment events to `POST /hook`. The dispatcher runs one isolated Agent Substrate actor per issue:

- The trigger label `agent` on an open issue starts the agent.
- A comment by a Developer or higher starts the next turn.
- Closing the issue, or removing the trigger label, stops the agent and deletes its actor.

The state label (`agent::working`, `agent::waiting`, `agent::done`, `agent::error`) and the status note show the agent state.

## Limitations

Confidential issues are not supported:

- `hack/register-hook.sh` keeps `confidential_issues_events` and `confidential_note_events` off, so GitLab sends no event for a confidential issue or its comments.
- The dispatcher ignores `Confidential Issue Hook` and `Confidential Note Hook` events.
- The trigger label on a confidential issue starts no agent.
- Do not enable the confidential events on the hook by hand.

## Build

```
make lint test build
docker build -t <registry>/krci-agent-dispatcher:<tag> .
```

## Deploy

```
helm upgrade --install krci-agent-dispatcher deploy-templates -n <namespace> -f <values.yaml>
hack/register-hook.sh add <project-path> https://<ingress-host>/hook
```

Chart values: `deploy-templates/README.md`. Configuration (env): `internal/config/config.go`.

## Documentation

| Document | Content |
|---|---|
| [GLOSSARY.md](GLOSSARY.md) | Terms |
| [docs/adr/](docs/adr/) | Decisions |
| [docs/agent-image-contract.md](docs/agent-image-contract.md) | What the agent image (`agentImage`) receives and what the dispatcher reads back |
