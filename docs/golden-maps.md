# Golden maps

`decide` is a lookup table: the facts read from GitLab and Substrate go in, the rule and its plan come out (`Poll`: the action and the GitLab writes). It has no memory, no I/O and no clock. The dispatcher stores no state of its own (ADR 0001); every event and every poll re-reads both sides and decides again.

```
 GitLab: issue, labels, notes, record ─┐                         ┌─ rule
 Substrate: actor parts and state     ─┼──▶ Decide / Poll (LUT) ─┼─ plan: ordered effects
 event: request kind, author role     ─┘                         └─ (writes: record, label, notes)
```

The golden maps are the truth table of that lookup table:

- `internal/services/decide/testdata/decide.golden`: `Decide`, every combination of its inputs.
- `internal/services/decide/testdata/decide-unread.golden`: `Decide` after a failed issue read; the read outcome alone decides.
- `internal/services/decide/testdata/poll.golden`: `Poll`, one poll of the activation loop.

They pin behaviour; they do not judge it. A row is right when a person says so in review.

## Format

- `# <axis>: <values>`: one line per input, with every value.
- `# rule <rule>: <n> cases`: cases per rule. The total is fixed by the axes; a change moves cases between rules.
- Rows: one value set per axis, then the outcome. `*` is any value, `a|b` is either. Every case is in exactly one row.
- `Decide` outcome: `rule plan`. A plan is `effect > effect > …`, `-` for none. An effect is `kind!fail`; a write is `write(writes)!fail`, a loop `loop(since=<note id>[,recovered])`, a recheck `recheck`. `fail` is `log` (go on), `abort` (end the plan), `error` (record substrate error, end), `error-fresh` (the same with status rows from a reload).
- Writes: `record=<state>[/<reason>]`, `label=<state>`, `note=<marker>`, comma-separated; `-` for none.
- `Poll` outcome: `rule action writes marker`.

## Work with the maps

| Task | Do |
|---|---|
| Check | `make test`; `TestDecideGolden`, `TestDecideUnreadGolden` and `TestPollGolden` fail on any difference and print the diff. |
| Accept an intended change | `go test ./internal/services/decide -run Golden -update`, then review the diff and commit the `.golden` files with the change. |
| Review a diff | Read the `# rule` counts first: which rules lost or gained cases, and whether the numbers match the intended scope. Then read the rows. A rule at 0 cases is unreachable. |
| Refactor `decide` | The maps must not change. |
| Add a request kind, agent state or actor state | Add the constant and its enumerator entry (`intake.Kinds`, `models.AgentStates`, `models.ActorStates`). An actor state also needs a value in `actorValues`. Run `-update`; the header shows the new value, the rows show where it lands by default. Decide whether that is right. |
| Add an issue read outcome | Add the `IssueRead` constant and its `issueReads` entry; `unreadAxes` picks it up. |
| Add an input to `Decide` or `Poll` | Add an axis in `golden_test.go` (`decideAxes`, `pollAxes`) with every value class the code distinguishes. |

## Guards

- `TestAgentStatesListsEveryAgentState`, `TestActorStatesListsEveryActorState`, `TestKindsListsEveryKind`: each enumerator lists every constant of its type.
- `TestIssueReadsListsEveryIssueRead`: `IssueReads` lists every issue read outcome.
- `TestEffectKindsListsEveryEffectKind`, `TestFailsListsEveryFailure`: `EffectKinds` and `Fails` list every effect kind and failure handling, each with a name.
- `TestActorAxisCoversEveryActorState`: every actor state is a value of the actor axis.
- `TestRecordRoundTripsEveryAgentState`: every agent state is written and read back by the status note record line.
- `renderGolden` panics above 12 axes or 16 values per axis.

## Limits

- Inputs are value classes, not every possible value: one marker note id, one comment, a turn at each step up to the limit.
- A plan states what the dispatcher does and how it handles a failed effect. Which effect fails, and the retries, are dispatcher behaviour, tested through `Handle` and `Run`.
- Which issue read outcome applies, gone or unavailable, is classified in the dispatcher (`settleUnread`), outside `decide`.
- A failed actor read is the actor value `unknown` in `decide.golden`. `Poll` never sees it: a failed read in the activation loop ends in the turn timeout. Other failed issue reads in the activation loop end there too, outside the maps.
