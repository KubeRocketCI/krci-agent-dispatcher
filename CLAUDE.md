# krci-agent-dispatcher

`github.com/KubeRocketCI/krci-agent-dispatcher`: the dispatcher for KubeRocketCI issue agents. It receives GitLab Issue Hook and Note Hook events on `POST /hook` and drives one Agent Substrate actor per issue: start, turn, stop.

Terms: `GLOSSARY.md`. Decisions: `docs/adr/`. Decision rules: `internal/services/decide/decide.go`; their truth table: `docs/golden-maps.md`. Agent image contract: `docs/agent-image-contract.md`. Use the glossary terms in code names, logs and notes.

## Build & Test

```
make build      # → dist/krci-agent-dispatcher-<arch> (CGO_ENABLED=0)
make test       # go test -race ./... -coverprofile=coverage.out, then the CRAP gate (CRAP_CHECK=false skips it)
make crap       # worst CRAP scores from coverage.out
make crap-baseline  # rewrite .crap-baseline.json after an intended change
make lint       # golangci-lint v2 (.golangci.yaml)
make lint-fix
make generate   # oapi-codegen: internal/api/oapi.yaml → internal/api/server_gen.go
make helm-lint
make helm-docs  # deploy-templates/README.md from README.md.gotmpl
make changelog
```

Run one package: `go test -race ./internal/services/decide/... -run TestDecideTable`.

Rewrite the golden maps after an intended change to `Decide` or `Poll`: `go test ./internal/services/decide -run Golden -update`, then review the diff of `internal/services/decide/testdata/*.golden`.

## Architecture

```
cmd/krci-agent-dispatcher/   builds adapters, wires them, starts Run and HTTP
internal/config/             env → Config
internal/api/                oapi.yaml, generated server, hook handler (port: Handle)
internal/models/             IssueKey, Issue, Note, Marker, Record, Writes, ActorView, ActorSummary, ActorSpec, Role, port errors
internal/services/intake/    payload → Request, pure
internal/services/decide/    decision function and activation-loop rules, pure
internal/services/dispatcher/ Handle, Run: issue lanes, execution, activation loop, restart recovery, sweep; ports.go
internal/gitlab/             GitLab port adapters: REST, gitlabtest fake, contract suite
internal/substrate/          Substrate port adapters: gRPC, substratetest fake, contract suite
internal/substrate/templates/ ActorTemplate + EgressPolicy render, pure
deploy-templates/            Helm chart
hack/register-hook.sh        project hook add|remove|list
hack/crap-gate.sh            CRAP gate behind make crap, crap-check, crap-baseline
```

## Conventions

- Commit messages: generate with `/krci-general:commit` (plugin `krci-general` from the `KubeRocketCI/claude-plugins` marketplace). Title `EPMDEDP-<id>: <type>: <description>`, 10–85 characters, type one of `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore` (`.github/workflows/pr-validation.yml`). Sign off (`git commit -s`).
- Branch names start with the ticket key (`EPMDEDP-<id>-<slug>`); `/krci-general:commit` reads the key from the branch.
- One dispatcher per atespace: it owns every actor named `issue-<project-id>-<iid>` in `ATESPACE` (ADR 0005).
- Ports are declared by their consumer (`dispatcher/ports.go`). Only `main` builds adapters.
- Port errors are declared in `models`; adapters wrap them.
- Each port has a production adapter and an in-memory fake; one contract suite runs against both.
- `decide` and `substrate/templates` are pure. A new rule is a new rule constant, a new table-test case and a reviewed golden map diff.
- A new request kind, agent state or actor state goes into its enumerator (`intake.Kinds`, `models.AgentStates`, `models.ActorStates`); tests fail until it is listed and the golden maps classify it.
- Tests go through module interfaces (`Handle`, `Run`, `Decide`, `Poll`, port contracts).
- No secret value in logs, argv, errors or test output.

## Agent skills

- Issue tracker: Jira project EPMDEDP through the mcp-atlassian tools; pull requests on GitHub. `docs/agents/issue-tracker.md`.
- Triage labels: default five roles as Jira labels. `docs/agents/triage-labels.md`.
- Domain docs: single-context, the files named at the top. `docs/agents/domain.md`.
