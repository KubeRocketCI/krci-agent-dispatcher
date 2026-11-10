# Events are best effort; missed events are not recovered

The dispatcher acts only on GitLab events as they arrive. It keeps no event store and does not scan GitLab for missed work. GitLab does not redeliver a failed event, so an event lost while the dispatcher is down or failing is lost: the person must comment again. We chose this to keep the dispatcher stateless and small; the only recovery is the restart recovery of actors left RUNNING by a dispatcher restart, which lists actors in Substrate instead of scanning GitLab issues.

## Considered Options

- Periodic reconcile: scan issues with the trigger label and compare with actors. Rejected: a second source of truth and a scan of every project.
- Event store or queue: persist each event before the reply to GitLab. Rejected: needs storage and its own failure handling.

## Consequences

- A comment during dispatcher downtime gets no turn and no note.
- Adding a reconcile or a store later needs no change to the event handling, because each action already re-reads the real state before it acts.
