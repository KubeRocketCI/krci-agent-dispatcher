# krci-agent-dispatcher

Dispatcher for KubeRocketCI issue agents. A GitLab project hook sends issue and comment events to `POST /hook`. The dispatcher runs one isolated Agent Substrate actor per issue:

- The trigger label `agent` on an open issue starts the agent.
- A comment by a Developer or higher starts the next turn.
- Closing the issue, or removing the trigger label, stops the agent and deletes its actor.

The state label (`agent::working`, `agent::waiting`, `agent::done`, `agent::error`) and the status note show the agent state.

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
