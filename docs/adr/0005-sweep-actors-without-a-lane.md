# A periodic sweep over Substrate actors settles actors without an issue lane

ADR 0001 makes restart recovery the only recovery. An activation that ends early can leave its actor running with no issue lane: a suspend that fails, an actor still transitional at the turn timeout, a kept actor of a deleted issue. No event comes for such an actor, so it holds a worker until the next dispatcher restart. We chose a sweep: every `SWEEP_INTERVAL` the dispatcher lists the actors in Substrate and sends a recover request for each actor that is running or transitional, has no issue lane, and was seen in that state by the previous sweep too. The recover request is the one restart recovery sends; the decision function settles it.

## Considered Options

- Requeue with backoff in the issue lane: retry each failed step. Rejected: it covers only the failures the code knows about, and a requeue is lost at a dispatcher restart.
- Per-issue level-triggered reconcile, reading GitLab and Substrate in every pass. Rejected for now: it needs GitLab reads for every actor and a desired state that start and clear-error requests do not leave behind.
- A periodic scan of GitLab issues. Rejected by ADR 0001.

## Consequences

- The sweep lists actors, not issues: no GitLab call for an actor that is suspended, crashed, or has an issue lane.
- An actor left running is settled within two sweep intervals instead of at the next dispatcher restart; the two sweeps keep it from racing an issue lane that is about to start.
- Lost events stay lost (ADR 0001): the sweep settles actors; it does not find comments made while the dispatcher was down.
- Sweep requests share the recovery concurrency limit with restart recovery.
- An actor that stays running or transitional with no issue lane after its recover request is requested again at every later sweep, each with a warning.
- The dispatcher owns every actor named `issue-<project-id>-<iid>` in its atespace: one dispatcher per atespace. Actor names carry no Git provider or deployment, and Substrate actors have no labels to tell owners apart; a second dispatcher in the same atespace would recover, suspend or delete the other's actors. `ATESPACE` is required; the chart sets it to the release namespace.
