# The dispatcher repo owns the turn protocol and its harness

The turn protocol (handshake, turn, turn marker, turn history, watchdog, reply) is implemented once, in Go, in this repo. The dispatcher and the harness, the agent loop that runs inside every actor, share one package for it. The dispatcher mounts the harness into every actor as a Substrate image volume, pinned by digest in the dispatcher release, and sets it as the actor command; agent images provide tools and an agent profile file only. Alternatives were conformance fixtures shared across repos (the second implementation stays and is only tested), a shared CLI called from a bash loop (half of the protocol stays in bash), and copying the harness into each agent image (harness versions drift across images; kept as the fallback where image volumes are not available).

## Consequences

- A change to the turn protocol is one release of this repo.
- This repo builds and releases the harness, not the agent images.
- New actors always run the harness of the dispatcher that created them; an actor keeps its harness digest until stop.
- The harness uses the dispatcher's GitLab REST adapter: one GitLab client.
- Depends on Substrate image volumes on the worker pool.
