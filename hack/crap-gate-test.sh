#!/usr/bin/env bash
# Tests of hack/crap-gate.sh: which files of a fixture module the gate scores with the real go-crap.
#
# Usage: GO_CRAP=<go-crap> GOJQ=<gojq> hack/crap-gate-test.sh
#
# Exit codes: 0 pass, 1 failure.
set -euo pipefail

: "${GO_CRAP:?}" "${GOJQ:?}"

gate=$(cd "$(dirname "$0")" && pwd)/crap-gate.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mod=$work/mod
mkdir -p "$mod"/{plain,sub,tags,p,q/p,uni}

# fixture writes file $1 of the module: optional header lines $3..., then package $2 and one function.
fixture() {
  local file=$1 pkg=$2
  shift 2
  {
    printf '%s\n' "$@"
    printf 'package %s\n\nfunc F() {}\n' "$pkg"
  } >"$mod/$file"
}

printf 'module fixture\n\ngo 1.22\n' >"$mod/go.mod"
fixture plain/plain.go plain
fixture plain/x_gen.go plain
printf 'package plain\n\n//go:build linux\n\nfunc G() {}\n' >"$mod/plain/mention.go"
fixture os_other.go fixture '//go:build !linux && !darwin' ''
fixture sub/after_license.go sub '// Copyright header.' '' '//go:build linux' ''
fixture tags/integration.go tags '//go:build integration' ''
fixture p/t.go p '//go:build linux' ''
fixture q/p/t.go p
fixture uni/é.go uni '//go:build linux' ''
printf 'mode: set\n' >"$mod/coverage.out"

# Expected: plain files, a //go:build line after the package clause, a non-OS tag, a same-named file under another
# directory. Not: CRAP_EXCLUDES, OS tags (also after a comment, also with a non-ASCII name).
want='plain/mention.go
plain/plain.go
q/p/t.go
tags/integration.go'

status=0
(cd "$mod" && env -u CRAP_THRESHOLD -u CRAP_REGRESSION_DELTA LC_ALL=C \
  CRAP_PROFILE=coverage.out CRAP_BASELINE="$work/baseline.json" CRAP_REPORT="$work/report.json" \
  CRAP_EXCLUDES='_gen\.go$' "$gate" baseline >"$work/gate.log" 2>&1) || status=$?
if ((status != 0)); then
  echo "FAIL crap-gate.sh baseline exited $status:" >&2
  cat "$work/gate.log" >&2
  exit 1
fi

got=$("$GOJQ" -r '[.entries[].file] | unique | .[]' "$work/report.json")
if [[ $got != "$want" ]]; then
  printf 'FAIL scored files:\n%s\nwant:\n%s\n' "$got" "$want" >&2
  exit 1
fi
echo "crap-gate tests passed"
