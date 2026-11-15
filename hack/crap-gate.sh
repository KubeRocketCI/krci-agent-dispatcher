#!/usr/bin/env bash
# CRAP gate over go-crap reports.
#
# Usage: hack/crap-gate.sh report|check|baseline
#
# Environment (required ones are set by the Makefile; the rest have defaults and can be
# overridden, e.g. make crap-check CRAP_THRESHOLD=20):
#   GO_CRAP, GOJQ            tool binaries (required)
#   CRAP_PROFILE             coverage profile written by go test [coverage.out]
#   CRAP_BASELINE            committed baseline [.crap-baseline.json]
#   CRAP_REPORT              JSON report of the last check [crap.json]
#   CRAP_THRESHOLD           score at or above which a new function fails [30]
#   CRAP_REGRESSION_DELTA    score increase above which an existing function fails [1.0]
#   CRAP_EXCLUDES            space-separated regexes of files to skip [_gen\.go$]
# A "$" in a value passed as a make argument is written "$$".
#
# A file whose //go:build line names an OS (GOOS value or unix) is always skipped.
#
# The coverage profile must be newer than every .go file.
#
# Exit codes: 0 pass, 1 usage or tool error, 2 gate failure.
set -euo pipefail

: "${GO_CRAP:?}" "${GOJQ:?}"
: "${CRAP_PROFILE:=coverage.out}"
: "${CRAP_BASELINE:=.crap-baseline.json}"
: "${CRAP_REPORT:=crap.json}"
: "${CRAP_THRESHOLD:=30}"
: "${CRAP_REGRESSION_DELTA:=1.0}"
: "${CRAP_EXCLUDES=_gen\.go\$}"

# Functions are matched to the baseline by file and name; absent from the baseline means new.
# $t is a jq variable.
# shellcheck disable=SC2016
readonly NEW_FUNCTIONS='
  .entries[]
  | select((.baseline_crap // 0) == 0 and .crap >= $t)
  | "  \(.file):\(.line) \(.function): \(.crap * 100 | round / 100) (CC \(.cyclomatic), coverage \(.coverage | round)%)"'

# Baseline keeps file, function and scores rounded to 0.1, sorted; effective_crap is the compared value.
readonly BASELINE_FORMAT='
  def r: if . == null then null else . * 10 | round / 10 end;
  {entries: ([.entries[]
    | {file, function, cyclomatic,
       coverage: (.coverage | r), crap: (.crap | r), effective_crap: (.effective_crap | r)}]
    | sort_by(.file, .function))}'

scan() {
  local -a args excludes
  args=(scan ./... --coverage-profile "$CRAP_PROFILE" --threshold "$CRAP_THRESHOLD" --no-progress)
  read -r -a excludes <<<"$CRAP_EXCLUDES"
  local re
  # The +-expansion keeps an empty array valid under set -u in bash 3.2 (macOS /bin/bash).
  for re in ${excludes[@]+"${excludes[@]}"}; do
    args+=(--exclude "$re")
  done
  local f
  while IFS= read -r -d '' f; do
    if [[ $f != *_test.go ]] && names_os "$f"; then
      # go-crap matches the regex against the absolute path.
      args+=(--exclude "^$(quote_regex "$PWD/${f#./}")\$")
    fi
  done < <(go_files)
  "$GO_CRAP" "${args[@]}" "$@"
}

# go_files prints the .go files under . outside bin/ and dist/, NUL-terminated.
go_files() {
  find . -name '*.go' -not -path './bin/*' -not -path './dist/*' -print0
}

# GOOS values and unix: the build tags that name an OS.
readonly OS_TAGS=' aix android darwin dragonfly freebsd hurd illumos ios js linux netbsd openbsd plan9 solaris wasip1 windows zos unix '

# names_os reports whether the //go:build line of file $1, before its package clause, names an OS tag.
names_os() {
  local line word
  while IFS= read -r line; do
    case $line in
    '//go:build '* | $'//go:build\t'*)
      for word in ${line//[^A-Za-z0-9_.]/ }; do
        [[ $OS_TAGS == *" $word "* ]] && return 0
      done
      return 1
      ;;
    package*) return 1 ;;
    esac
  done <"$1"
  return 1
}

# quote_regex prints $1 with each RE2 metacharacter escaped.
quote_regex() {
  local s=$1 out= c i
  for ((i = 0; i < ${#s}; i++)); do
    c=${s:i:1}
    case $c in
    [\\.+*?\(\)\|\[\]\{\}^\$]) c="\\$c" ;;
    esac
    out+=$c
  done
  printf '%s' "$out"
}

report() {
  scan --top 20
}

check() {
  if [[ ! -f $CRAP_BASELINE ]]; then
    echo "CRAP gate failed: $CRAP_BASELINE not found: run make crap-baseline" >&2
    return 2
  fi

  local failed=0 new
  rm -f "$CRAP_REPORT"
  scan --baseline "$CRAP_BASELINE" --fail-regression \
    --fail-regression-threshold "$CRAP_REGRESSION_DELTA" -f json -o "$CRAP_REPORT" || failed=1
  if [[ ! -s $CRAP_REPORT ]]; then
    echo "go-crap produced no report" >&2
    return 1
  fi

  new=$("$GOJQ" -r --argjson t "$CRAP_THRESHOLD" "$NEW_FUNCTIONS" "$CRAP_REPORT")
  if [[ -n $new ]]; then
    printf 'CRAP new functions at or above %s:\n%s\n' "$CRAP_THRESHOLD" "$new" >&2
    failed=1
  fi

  if ((failed)); then
    echo "CRAP gate failed, details in $CRAP_REPORT: add tests or simplify the listed functions; make crap-baseline accepts an intended change" >&2
    return 2
  fi
  echo "CRAP gate passed"
}

baseline() {
  local tmp
  scan -f json -o "$CRAP_REPORT"
  tmp=$(mktemp "$CRAP_BASELINE.XXXXXX")
  if ! "$GOJQ" "$BASELINE_FORMAT" "$CRAP_REPORT" >"$tmp"; then
    rm -f "$tmp"
    echo "cannot format $CRAP_REPORT; $CRAP_BASELINE not changed" >&2
    return 1
  fi
  mv "$tmp" "$CRAP_BASELINE"
  echo "CRAP baseline written to $CRAP_BASELINE"
}

# require_profile fails when the coverage profile is missing or older than a .go file.
require_profile() {
  local f
  if [[ ! -f $CRAP_PROFILE ]]; then
    echo "$CRAP_PROFILE not found: run make test first" >&2
    return 1
  fi
  while IFS= read -r -d '' f; do
    if [[ $f -nt $CRAP_PROFILE ]]; then
      echo "$CRAP_PROFILE is older than $f: run make test first" >&2
      return 1
    fi
  done < <(go_files)
}

main() {
  case "${1:-}" in
  report | check | baseline)
    require_profile
    "$1"
    ;;
  *)
    echo "usage: $0 report|check|baseline" >&2
    return 1
    ;;
  esac
}

main "$@"
