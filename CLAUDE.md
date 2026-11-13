# krci-agent-dispatcher

`github.com/KubeRocketCI/krci-agent-dispatcher`: the dispatcher for KubeRocketCI issue agents. It receives GitLab Issue Hook and Note Hook events on `POST /hook` and drives one Agent Substrate actor per issue: start, turn, stop.

Terms: `GLOSSARY.md`. Decisions: `docs/adr/`. Decision rules: `internal/services/decide/decide.go`; their truth table: `docs/golden-maps.md`. Dispatcher, harness and agent images: `docs/agent-harness.md`. Use the glossary terms in code names, logs and notes.

## Build & Test

```
make build      # → dist/krci-agent-dispatcher-<arch>, dist/krci-agent-harness-<arch> (linux, input of Dockerfile.harness); CGO_ENABLED=0
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
cmd/krci-agent-harness/      harness command: selftest, run
internal/config/             env → Config
internal/api/                oapi.yaml, generated server, hook handler (port: Handle)
internal/models/             IssueKey, Issue, Note, Record, Writes, ActorView, ActorSummary, ActorSpec, Role, port errors
internal/turn/               turn protocol: Marker, History, Turn; pure
internal/services/intake/    payload → Request, pure
internal/services/decide/    decision function and activation-loop rules: facts in, rule and plan out; pure
internal/services/selftest/  agent image contract checks; ports.go
internal/services/harness/   harness: handshake (New), Step, Run; ports.go
internal/services/dispatcher/ Handle, Run: issue lanes, observation (observe), plan runner (perform), activation loop, restart recovery, sweep; ports.go
internal/gitlab/             GitLab port adapters: REST, gitlabtest fake, contract suite
internal/substrate/          Substrate port adapters: gRPC, substratetest fake, contract suite
internal/substrate/templates/ ActorTemplate + EgressPolicy render, pure
internal/system/             System port adapters: OS, Clock, systemtest fake (Clock too), contract suite
internal/state/              StateStore port adapters: file, statetest fake, contract suite
deploy-templates/            Helm chart
Dockerfile.harness           harness image: FROM scratch, binary at /krci-agent-harness
hack/register-hook.sh        project hook add|remove|list
hack/crap-gate.sh            CRAP gate behind make crap, crap-check, crap-baseline
```

## Conventions

- Commit messages: generate with `/krci-general:commit` (plugin `krci-general` from the `KubeRocketCI/claude-plugins` marketplace). Title `EPMDEDP-<id>: <type>: <description>`, 10–85 characters, type one of `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore` (`.github/workflows/pr-validation.yml`). Sign off (`git commit -s`).
- Branch names start with the ticket key (`EPMDEDP-<id>-<slug>`); `/krci-general:commit` reads the key from the branch.
- Jira keys appear only in commit titles, branch names, PR bodies, this file and `docs/agents/issue-tracker.md`. Never in code, comments, tests or other docs.
- One dispatcher per atespace: it owns every actor named `issue-<project-id>-<iid>` in `ATESPACE` (ADR 0005).
- Ports are declared by their consumer (`dispatcher/ports.go`). Only `main` builds adapters.
- Port errors are declared in `models`; adapters wrap them.
- Each port has a production adapter and an in-memory fake; one contract suite runs against both.
- `decide` and `substrate/templates` are pure. A new rule is a new rule constant, a new table-test case and a reviewed golden map diff.
- A rule's plan holds every effect it causes, in order, with its failure handling. The dispatcher performs plans and adds only the failure handling named by `decide.Fail`, the cancelled-ctx rule and retries; behaviour changes go into `decide`.
- A new request kind, agent state or actor state goes into its enumerator (`intake.Kinds`, `models.AgentStates`, `models.ActorStates`); tests fail until it is listed and the golden maps classify it.
- Tests go through module interfaces (`Handle`, `Run`, `Decide`, `Poll`, port contracts).
- Expected values in tests come from the spec, not from production constants or lists.
- A command's exit codes are tested on the process: the test binary re-runs itself as the command (`cmd/krci-agent-harness/process_test.go`).
- No secret value in logs, argv, errors or test output.

## Agent skills

- Issue tracker: Jira project EPMDEDP through the mcp-atlassian tools; pull requests on GitHub. `docs/agents/issue-tracker.md`.
- Triage labels: default five roles as Jira labels. `docs/agents/triage-labels.md`.
- Domain docs: single-context, the files named at the top. `docs/agents/domain.md`.
