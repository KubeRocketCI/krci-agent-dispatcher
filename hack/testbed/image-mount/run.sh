#!/usr/bin/env bash
# Image volume mount check on the testbed pool: start, run from the mount, suspend/resume, revert.
# Exit 0: mount works. Exit 1: a check failed. Exit 2: mount held, the process restarted on a resume.
# Evidence: OUT_DIR (run.log, s0..s3.json, snapshot-{s1,durable,s3}.json, lifecycle.log).
set -euo pipefail

usage() {
  cat >&2 <<USAGE
usage: $0 --context <kube-context> --root-image <ref@sha256:...> --harness <ref@sha256:...>
          [--atespace krci-agents] [--pool workers] [--kubectl-ate <path>] [--out <dir>] [--wait <seconds>] [--keep]
USAGE
  exit 64
}

HERE=$(cd "$(dirname "$0")" && pwd)
ATESPACE=krci-agents
POOL=workers
ACTOR=mount-check-1
TEMPLATE=mount-check
KATE=${KUBECTL_ATE:-kubectl-ate}
OUT_DIR=
KEEP=false
CTX= ROOT_IMAGE= HARNESS=
LOCAL_PORT=18080
WAIT_SECONDS=120
SETTLED_SECONDS=10
RUN_SECONDS=20
DURABLE_SECONDS=15
RESTARTED=

while [[ $# -gt 0 ]]; do
  case $1 in
    --keep) KEEP=true; shift; continue ;;
    --context | --root-image | --harness | --atespace | --pool | --kubectl-ate | --out | --wait)
      [[ $# -ge 2 ]] || usage ;;
    *) usage ;;
  esac
  case $1 in
    --context) CTX=$2 ;;
    --root-image) ROOT_IMAGE=$2 ;;
    --harness) HARNESS=$2 ;;
    --atespace) ATESPACE=$2 ;;
    --pool) POOL=$2 ;;
    --kubectl-ate) KATE=$2 ;;
    --out) OUT_DIR=$2 ;;
    --wait) WAIT_SECONDS=$2 ;;
  esac
  shift 2
done
[[ -n $CTX && -n $ROOT_IMAGE && -n $HARNESS ]] || usage
for ref in "$ROOT_IMAGE" "$HARNESS"; do
  [[ $ref == *@sha256:* ]] || { echo "not pinned by digest: $ref" >&2; exit 64; }
done
for bin in jq curl "$KATE"; do
  command -v "$bin" >/dev/null || { echo "missing on PATH: $bin" >&2; exit 69; }
done
OUT_DIR=${OUT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/image-mount.XXXXXX")}
mkdir -p "$OUT_DIR"

# The shared ate-client identity is the testbed default; its warning is dropped.
# stdout stays on fd 3; pipefail keeps the exit code of kubectl-ate.
ate() { { "$KATE" --context "$CTX" "$@" 2>&1 1>&3 | sed '/no --token-file given/d' >&2; } 3>&1; }
log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$OUT_DIR/run.log"; }
fail() { log "FAIL: $*"; exit 1; }

# Prints the bare Actor; empty on a transient API error.
actor_json() { ate get actor "$ACTOR" -a "$ATESPACE" -o json 2>>"$OUT_DIR/api-errors.log" | jq '.actors[0] // .' || true; }

# wait_state fails fast on CRASHED and on a settled state other than want: nothing will move the actor on its own.
wait_state() {
  local want=$1 json state= settled_since= deadline=$((SECONDS + WAIT_SECONDS))
  while (( SECONDS < deadline )); do
    json=$(actor_json)
    state=$(jq -r '.status.state // ""' <<<"$json" 2>/dev/null || true)
    [[ $state == "$want" ]] && return 0
    case $state in
      ACTOR_STATE_CRASHED)
        echo "$json" >"$OUT_DIR/crashed.json"
        fail "actor crashed: $(jq -r '.status.crash.message' <<<"$json")" ;;
      ACTOR_STATE_RUNNING | ACTOR_STATE_SUSPENDED)
        settled_since=${settled_since:-$SECONDS}
        (( SECONDS - settled_since < SETTLED_SECONDS )) || fail "actor settled in $state, want $want" ;;
      *) settled_since= ;;
    esac
    sleep 2
  done
  fail "actor not $want after ${WAIT_SECONDS}s (last: ${state:-unknown})"
}

# The newest snapshot generation: what the next resume restores from.
latest_snapshot() { actor_json | jq -c '.status.snapshots // [] | max_by(.generation) // {}' 2>/dev/null || true; }

# wait_durable allows only the lag of the status after a sync suspend, which returns once the snapshot is COMPLETED.
wait_durable() {
  local snap= deadline=$((SECONDS + DURABLE_SECONDS))
  while (( SECONDS < deadline )); do
    snap=$(latest_snapshot)
    if [[ $(jq -r '.durableSnapshot.state // ""' <<<"$snap" 2>/dev/null) == SNAPSHOT_STORAGE_STATE_COMPLETED ]]; then
      return 0
    fi
    sleep 2
  done
  fail "snapshot not durable ${DURABLE_SECONDS}s after a sync suspend: $snap"
}

# record_snapshot saves the snapshot a resume restores from; its fidelity caps what the resume keeps.
record_snapshot() {
  local label=$1 snap
  snap=$(latest_snapshot)
  echo "$snap" >"$OUT_DIR/snapshot-$label.json"
  log "$label restores $(jq -c '{generation, fidelity, durable: .durableSnapshot.state}' <<<"$snap")"
}

# Only call while RUNNING: the proxy resumes a suspended actor on connect.
read_state() {
  local label=$1 pid ok=false deadline=$((SECONDS + 60))
  "$KATE" --context "$CTX" proxy actor "$ACTOR" -a "$ATESPACE" "$LOCAL_PORT:8080" >"$OUT_DIR/proxy-$label.log" 2>&1 &
  pid=$!
  while (( SECONDS < deadline )); do
    if curl -fsS "http://127.0.0.1:$LOCAL_PORT/state" -o "$OUT_DIR/$label.json" 2>/dev/null; then
      ok=true
      break
    fi
    sleep 1
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  $ok || fail "no /state from $ACTOR for $label"
  log "$label $(jq -c '{instance, pid, counter, file_counter, executable, ok, failed: [.checks[] | select(.ok | not) | .name]}' "$OUT_DIR/$label.json")"
}

field() { jq -r ".$2" "$OUT_DIR/$1.json"; }

expect_ok() {
  [[ $(field "$1" ok) == true ]] || fail "$1 checks: $(jq -c '[.checks[] | select(.ok | not)]' "$OUT_DIR/$1.json")"
}

# expect_same_process records a restart; the mount checks still decide pass or fail.
expect_same_process() {
  local label=$1 ref=$2
  if [[ $(field "$label" instance) != $(field "$ref" instance) ]]; then
    RESTARTED+="$label "
    log "$label: process restarted (instance $(field "$ref" instance) -> $(field "$label" instance))"
    return 1
  fi
}

cleanup() {
  ate logs actor "$ACTOR" -a "$ATESPACE" --source lifecycle >"$OUT_DIR/lifecycle.log" 2>&1 || true
  $KEEP && return
  ate delete actor "$ACTOR" -a "$ATESPACE" >/dev/null 2>&1 || true
  ate delete actor-template "$TEMPLATE" -a "$ATESPACE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "out: $OUT_DIR"
log "root image: $ROOT_IMAGE"
log "harness: $HARNESS"

sed -e "s|\${ATESPACE}|$ATESPACE|" -e "s|\${POOL}|$POOL|" \
    -e "s|\${ROOT_IMAGE}|$ROOT_IMAGE|" -e "s|\${HARNESS}|$HARNESS|" \
    "$HERE/actortemplate.yaml" >"$OUT_DIR/actortemplate.yaml"

log "1. create template and actor"
ate create actor-template -f "$OUT_DIR/actortemplate.yaml" >/dev/null
ate create actor "$ACTOR" -a "$ATESPACE" --template "$TEMPLATE" >/dev/null
# A new actor is SUSPENDED with no snapshot; the first resume cold-boots it.
ate resume actor "$ACTOR" -a "$ATESPACE" >/dev/null
wait_state ACTOR_STATE_RUNNING
read_state s0
expect_ok s0

log "2. suspend (async), resume from the snapshot on the node"
ate suspend actor "$ACTOR" -a "$ATESPACE" >/dev/null
wait_state ACTOR_STATE_SUSPENDED
record_snapshot s1
ate resume actor "$ACTOR" -a "$ATESPACE" >/dev/null
wait_state ACTOR_STATE_RUNNING
read_state s1
expect_ok s1
if expect_same_process s1 s0; then
  (( $(field s1 counter) >= $(field s0 counter) )) || fail "counter went back across resume"
fi

# An async snapshot turns durable only after atelet's --snapshot-upload-delay (5 min default); sync makes the revert target now.
log "3. suspend (sync), resume, run ${RUN_SECONDS}s"
ate suspend actor "$ACTOR" -a "$ATESPACE" --mode sync >/dev/null
wait_state ACTOR_STATE_SUSPENDED
wait_durable
record_snapshot durable
ate resume actor "$ACTOR" -a "$ATESPACE" >/dev/null
wait_state ACTOR_STATE_RUNNING
sleep "$RUN_SECONDS"
read_state s2
expect_ok s2
if expect_same_process s2 s1; then
  (( $(field s2 counter) > $(field s1 counter) )) || fail "counter did not advance"
fi

log "4. revert to the durable snapshot, resume"
ate revert actor "$ACTOR" -a "$ATESPACE" >/dev/null
wait_state ACTOR_STATE_SUSPENDED
record_snapshot s3
ate resume actor "$ACTOR" -a "$ATESPACE" >/dev/null
wait_state ACTOR_STATE_RUNNING
read_state s3
expect_ok s3
# The durable snapshot was taken after s1 and before s2: s3 continues from there.
if expect_same_process s3 s1; then
  (( $(field s3 counter) >= $(field s1 counter) && $(field s3 counter) < $(field s2 counter) )) \
    || fail "memory not rewound to the durable snapshot: s1=$(field s1 counter) s3=$(field s3 counter) s2=$(field s2 counter)"
fi
(( $(field s3 file_counter) < $(field s2 file_counter) )) \
  || fail "rootfs not rewound: file_counter s3=$(field s3 file_counter) s2=$(field s2 file_counter)"

if [[ -n $RESTARTED ]]; then
  log "MOUNT HELD, PROCESS RESTARTED at: $RESTARTED(see snapshot-*.json fidelity)"
  exit 2
fi
log "PASS: mount works"
