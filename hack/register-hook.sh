#!/usr/bin/env bash
# Project hook for krci-agent-dispatcher (spec §5.7).
#   register-hook.sh add <project-path> <hook-url>     create or update the hook with that exact URL
#   register-hook.sh remove <project-path> <hook-url>  delete the hook with that exact URL
#   register-hook.sh list <project-path> <hook-url>    show the hook with that exact URL
# Hook settings: issues_events and note_events on, every other event off, enable_ssl_verification on,
# token = the dispatcher hook token. Hooks with another URL (KRCI hook 1) are never changed.
# Env: GITLAB_URL (required), GITLAB_CA_FILE (optional PEM), GITLAB_RESOLVE (optional curl --resolve host:port:addr), GITSERVER_SECRET (default krci/ci-gitlab, key token;
# a Maintainer token: krci-agent cannot manage hooks), HOOK_SECRET (default krci-agents/hook, key token).
# Tokens stay in non-exported variables; curl gets them from a process substitution or stdin, never argv.
set -uo pipefail

GITLAB_URL="${GITLAB_URL:-}"
GITLAB_CA_FILE="${GITLAB_CA_FILE:-}"
GITLAB_RESOLVE="${GITLAB_RESOLVE:-}"
GITSERVER_SECRET="${GITSERVER_SECRET:-krci/ci-gitlab}"
HOOK_SECRET="${HOOK_SECRET:-krci-agents/hook}"

usage() { echo "usage: register-hook.sh add|remove|list <project-path> <hook-url>" >&2; exit 2; }

cmd="${1:-}" project="${2:-}" url="${3:-}"
case "${cmd}" in add|remove|list) ;; *) usage ;; esac
[ -n "${project}" ] && [ -n "${url}" ] || usage
[ -n "${GITLAB_URL}" ] || { echo "FAIL register-hook GITLAB_URL is required" >&2; exit 2; }

secret_token() { kubectl -n "${1%%/*}" get secret "${1#*/}" -o json 2>/dev/null | jq -r '.data.token // empty | @base64d'; }

GS_TOKEN="$(secret_token "${GITSERVER_SECRET}")"
[ -n "${GS_TOKEN}" ] || { echo "FAIL register-hook no token in Secret ${GITSERVER_SECRET}" >&2; exit 1; }

# api <METHOD> <path> [body on stdin]: prints the body, then the HTTP code on the last line.
api() {
  local method="$1" path="$2" opts=()
  shift 2
  [ -z "${GITLAB_CA_FILE}" ] || opts=(--cacert "${GITLAB_CA_FILE}")
  [ -z "${GITLAB_RESOLVE}" ] || opts+=(--resolve "${GITLAB_RESOLVE}")
  curl -sS --noproxy '*' ${opts[@]+"${opts[@]}"} -X "${method}" -H @<(printf 'PRIVATE-TOKEN: %s\n' "${GS_TOKEN}") \
    -H 'Content-Type: application/json' -w '\n%{http_code}' "$@" "${GITLAB_URL%/}/api/v4/${path}"
}

# call <METHOD> <path> [curl args]: api with a 2xx check; prints the body.
call() {
  local out code
  out="$(api "$@")" || { echo "FAIL register-hook $1 $2: curl error" >&2; return 1; }
  code="$(tail -n 1 <<<"${out}")"
  case "${code}" in
    2??) sed '$d' <<<"${out}" ;;
    *) echo "FAIL register-hook $1 $2: HTTP ${code}" >&2; return 1 ;;
  esac
}

enc="$(jq -rn --arg p "${project}" '$p | @uri')"
pid="$(call GET "projects/${enc}" | jq -r '.id // empty')" || exit 1
[ -n "${pid}" ] || { echo "FAIL register-hook project ${project} not found" >&2; exit 1; }
hooks="$(call GET "projects/${pid}/hooks?per_page=100")" || exit 1
hook="$(jq -c --arg u "${url}" 'first(.[] | select(.url == $u)) // empty' <<<"${hooks}")"
hid="$(jq -r '.id // empty' <<<"${hook:-null}")"

case "${cmd}" in
  list)
    if [ -n "${hid}" ]; then
      jq -r '"\(.id) \(.url) issues=\(.issues_events) notes=\(.note_events) ssl=\(.enable_ssl_verification)"' <<<"${hook}"
    else
      echo "INFO register-hook no hook ${url} on ${project}"
    fi
    ;;
  remove)
    if [ -z "${hid}" ]; then echo "INFO register-hook no hook ${url} on ${project}"; exit 0; fi
    call DELETE "projects/${pid}/hooks/${hid}" >/dev/null || exit 1
    echo "PASS register-hook removed hook ${hid} from ${project}"
    ;;
  add)
    HOOK_TOKEN="$(secret_token "${HOOK_SECRET}")"
    [ -n "${HOOK_TOKEN}" ] || { echo "FAIL register-hook no token in Secret ${HOOK_SECRET}" >&2; exit 1; }
    body() {
      jq -n --arg u "${url}" --rawfile t <(printf '%s' "${HOOK_TOKEN}") '{
        url: $u, token: $t, enable_ssl_verification: true,
        issues_events: true, note_events: true,
        push_events: false, merge_requests_events: false, tag_push_events: false, pipeline_events: false,
        job_events: false, wiki_page_events: false, confidential_issues_events: false,
        confidential_note_events: false, deployment_events: false, releases_events: false,
        feature_flag_events: false, emoji_events: false, resource_access_token_events: false}'
    }
    if [ -n "${hid}" ]; then
      body | call PUT "projects/${pid}/hooks/${hid}" --data-binary @- >/dev/null || exit 1
      echo "PASS register-hook updated hook ${hid} on ${project}"
    else
      hid="$(body | call POST "projects/${pid}/hooks" --data-binary @- | jq -r '.id // empty')" || exit 1
      echo "PASS register-hook created hook ${hid} on ${project}"
    fi
    ;;
esac
