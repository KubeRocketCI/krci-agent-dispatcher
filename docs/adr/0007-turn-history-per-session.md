# Turn history is per session

The turn marker carries the session. The dispatcher and the harness read the turn number and the comments a turn covered from the turn markers of the current session, the session of the actor while it exists and the session of the status note otherwise; the harness keeps no turn counter. A session with no turn marker starts turn 1 at once and covers every comment. Alternatives were a turn counter in the actor (it diverges from the turn history after a revert, a lost actor state or a re-enable) and one turn history per issue across sessions (a re-enabled agent would hit the turn limit at once).

## Consequences

- Supersedes the marker format of ADR 0003. The turn marker is still the completion signal.
- The turn limit applies per session.
- The session is the actor's (the `SESSION_ID` of its template) while the actor exists, the status note's otherwise. The `Session` row of the status note is a projection of it, rewritten with every record written while the actor can be read (ADR 0004); a record written after a failed actor read keeps the old rows, and once the actor is gone that row is the session. A status note that records done under an actor of another session is a re-enable whose record writes did not land: the done record counts as none, and the issue goes on from the actor for every request.
- The turn history is the only truth for turn numbers, so an actor can be resumed on any worker and after a revert.
- A reply is identified by session and turn number; the harness posts it once.
