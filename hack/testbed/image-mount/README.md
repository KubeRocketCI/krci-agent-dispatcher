# Image volume mount check

Checks on the testbed pool that Substrate mounts a digest-pinned OCI image read-only at `/opt/krci-agent` and runs `/opt/krci-agent/krci-agent-harness` as the actor command over the agent image, across suspend, resume and revert. Decides between mounting the harness and copying it into each agent image (ADR 0006).

- `actortemplate.yaml`: the dispatcher actor template plus the `harness` image volume; command `krci-agent-harness testbed-probe`; no egress policy.
- `run.sh`: drives the check, keeps the evidence in `--out`. Exit 0: mount works. Exit 1: a check failed. Exit 2: mount held, the process restarted on a resume.

`testbed-probe` (`internal/harness/testbedprobe`) serves `GET /state` on port 8080 and logs one JSON line per heartbeat. The harness image is `Dockerfile.harness` at the repo root.

## Prerequisites

- Testbed from `try-kuberocketci`: `make substrate`, atespace `krci-agents`, pool `workers`.
- No issue actor running: the pool holds one actor.
- `jq`, `curl`, `kubectl-ate` (`try-kuberocketci/.cache/substrate/<version>/bin`).

## Run

```bash
make build HOST_OS=linux HOST_ARCH=arm64
docker buildx build --platform linux/arm64 -f Dockerfile.harness -t <registry>/krci-agent-harness:dev --push .

hack/testbed/image-mount/run.sh \
  --context kind-krci \
  --kubectl-ate ../try-kuberocketci/.cache/substrate/v1.0.0/bin/kubectl-ate \
  --root-image <krci-agent-go ref>@sha256:<digest> \
  --harness <registry>/krci-agent-harness:dev@sha256:<digest> \
  --out "$TMPDIR/image-mount"
```

Atelet pulls the volume image itself; use a registry the worker reaches without the kind containerd mirror.

## Checks

| Step | State | Pass |
|---|---|---|
| create, resume | `s0` | probe checks pass |
| async suspend, resume from the snapshot on the node | `s1` | checks pass; same `instance`, counter not below `s0` |
| sync suspend (durable snapshot), resume, run 20 s | `s2` | checks pass; same `instance`, counter above `s1` |
| revert, resume | `s3` | checks pass; same `instance`, counter from `s1` up to below `s2`; `file_counter` below `s2` |

An async snapshot turns durable only after atelet's `--snapshot-upload-delay`, 5 minutes by default; the sync suspend in step 3 makes the revert target at once. The dispatcher suspends async and reverts only a crashed actor, which then returns to the last durable snapshot; ADR 0003 and ADR 0007 cover a repeated turn.

Probe checks: the agent image contract of `selftest` (`claude`, `git`, `sh` on `PATH`; uid 1000; `HOME` and `/work` writable; mount path free or holding the harness), `/opt/krci-agent` mounted `ro` in `/proc/self/mountinfo` with the write refused, the binary running from it, the trust bundle present.

`instance` is random per process start; the pid is 1 after every cold boot. A new `instance` at `s1`, `s2` or `s3` is exit 2: the mount held and memory was not restored. `snapshot-s1.json`, `snapshot-durable.json` and `snapshot-s3.json` hold the fidelity of the snapshot each resume restored from; a MEMORY restore can still fall back to ROOTFS when no worker has room.

`kubectl ate proxy` resumes a suspended actor on connect; `run.sh` reads `/state` only while the actor is running.
