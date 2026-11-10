# krci-agent-dispatcher

`github.com/KubeRocketCI/krci-agent-dispatcher`: the dispatcher for KubeRocketCI issue agents. It receives GitLab Issue Hook and Note Hook events on `POST /hook` and drives one Agent Substrate actor per issue: start, turn, stop.

Terms: `GLOSSARY.md`. Decisions: `docs/adr/`. Decision rules: `internal/services/decide/decide.go`. Agent image contract: `docs/agent-image-contract.md`. Use the glossary terms in code names, logs and notes.

## Build & Test

```
make build      # → dist/krci-agent-dispatcher-<arch> (CGO_ENABLED=0)
make test       # go test -race ./... -coverprofile=coverage.out
make lint       # golangci-lint v2 (.golangci.yaml)
make lint-fix
make generate   # oapi-codegen: internal/api/oapi.yaml → internal/api/server_gen.go
make helm-lint
make helm-docs  # deploy-templates/README.md from README.md.gotmpl
make changelog
```

Run one package: `go test -race ./internal/services/decide/... -run TestDecideTable`.

## Architecture

```
cmd/krci-agent-dispatcher/   builds adapters, wires them, starts Run and HTTP
internal/config/             env → Config
internal/api/                oapi.yaml, generated server, hook handler (port: Handle)
internal/models/             IssueKey, Issue, Note, Marker, Record, Writes, ActorView
internal/services/intake/    payload → Request, pure
internal/services/decide/    decision function and activation-loop rules, pure
internal/services/dispatcher/ Handle, Run: issue lanes, execution, activation loop, restart recovery; ports.go
internal/gitlab/             GitLab port adapters: REST, gitlabtest fake, contract suite
internal/substrate/          Substrate port adapters: gRPC, substratetest fake, contract suite
internal/templates/          ActorTemplate + EgressPolicy render, pure
deploy-templates/            Helm chart
hack/register-hook.sh        project hook add|remove|list
```

## Conventions

- Commit titles: `EPMDEDP-<id>: <type>: <description>`, 10–85 characters (`.github/workflows/pr-validation.yml`). Sign off (`git commit -s`).
- Ports are declared by their consumer (`dispatcher/ports.go`). Only `main` builds adapters.
- Each port has a production adapter and an in-memory fake; one contract suite runs against both.
- `decide` and `templates` are pure. A new rule is a new rule constant and a new table-test case.
- Tests go through module interfaces (`Handle`, `Run`, `Decide`, `Poll`, port contracts).
- No secret value in logs, argv, errors or test output.
