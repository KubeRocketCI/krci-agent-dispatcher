# The status note record is the truth; state labels are a projection

The agent states working and waiting follow from the Substrate actor state, but stopped and error (with its reason) leave no trace in Substrate: after a stop there is no actor, and an error reason exists only in GitLab. Labels can be changed by any person with label rights, so they cannot hold that truth. The bot user writes a hidden record line in the status note (`state=`, `reason=`), and every state label is computed from the record and the actor state by one decision function, which also repairs labels that people change. Alternatives were labels as the truth (lost on a hand edit) and a dispatcher store (rejected, ADR 0001).

## Consequences

- A person clears an error on purpose by removing `agent::error`; the dispatcher sees the label event and writes a new record.
- A deleted status note loses the record; the label is then the fallback.
- A later store holds the same record per issue, and the same decision function reconciles it.
