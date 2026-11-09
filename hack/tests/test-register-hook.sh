#!/usr/bin/env bash
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${HERE}/assert.sh"
SCRIPT="${HERE}/../register-hook.sh"
ORIG_PATH="${PATH}"
GS_TOKEN=glpat-gitserver-secret
HOOK_TOKEN=hook-token-secret
URL=https://agent-dispatcher.example/hook

# Stubs log argv to $STUB_LOG. curl also saves each request body to $SD/body.<n> and answers from $SD/hooks.json:
#   GET projects/<enc> → {"id": 12}; GET hooks → hooks.json; POST/PUT hooks → the body; DELETE → 204.
setup_case() {
  SD="$(mktemp -d)"; STUB_LOG="${SD}/log"; : >"${STUB_LOG}"; export SD STUB_LOG GS_TOKEN HOOK_TOKEN ACCEPT_TOKEN
  mkdir -p "${SD}/bin"; echo '[]' >"${SD}/hooks.json"; echo 0 >"${SD}/n"
  cat >"${SD}/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
printf 'kubectl %s\n' "$*" >>"$STUB_LOG"
case "$*" in
  "-n krci get secret ci-gitlab -o json") jq -n --arg t "$GS_TOKEN" '{data: {token: ($t | @base64)}}' ;;
  "-n krci-agents get secret hook -o json") jq -n --arg t "$HOOK_TOKEN" '{data: {token: ($t | @base64)}}' ;;
  *) exit 1 ;;
esac
STUB
  cat >"${SD}/bin/curl" <<'STUB'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$STUB_LOG"
m=GET url= hdr= data=
while [ $# -gt 0 ]; do
  case "$1" in
    -X) m="$2"; shift ;;
    -H) h="${2#@}"; hdr="$hdr$(cat "$h" 2>/dev/null || echo "$2")"; shift ;;
    --data-binary) data="$2"; shift ;;
    http*) url="$1" ;;
  esac
  shift
done
case "$hdr" in *"PRIVATE-TOKEN: ${ACCEPT_TOKEN:-$GS_TOKEN}"*) ;; *) echo '{"message":"401"}'; printf '\n401'; exit 0 ;; esac
n=$(( $(cat "$SD/n") + 1 )); echo "$n" >"$SD/n"
[ "$data" = "@-" ] && cat >"$SD/body.$n"
path="${url#*/api/v4/}"; path="${path%%\?*}"
printf '%s %s\n' "$m" "$path" >>"$SD/requests"
case "$m $path" in
  "GET projects/krci%2Ftest-go-app") printf '{"id": 12}\n200' ;;
  "GET projects/12/hooks") cat "$SD/hooks.json"; printf '\n200' ;;
  "POST projects/12/hooks") cat "$SD/body.$n"; printf '\n201' ;;
  "PUT projects/12/hooks/"*) cat "$SD/body.$n"; printf '\n200' ;;
  "DELETE projects/12/hooks/"*) printf '\n204' ;;
  *) printf '{"message":"404"}\n404' ;;
esac
STUB
  chmod +x "${SD}/bin/"*
}

run_script() {
  OUT="$(PATH="${SD}/bin:${ORIG_PATH}" GITLAB_URL=https://gitlab.example bash "${SCRIPT}" "$@" 2>&1)"
}

last_body() { cat "${SD}/body.$(cat "${SD}/n")"; }

test_add_sets_events_and_ssl() {
  setup_case; run_script add krci/test-go-app "${URL}"; assert_eq 0 $?
  assert_contains 'POST projects/12/hooks' "$(cat "${SD}/requests")"
  local b; b="$(last_body)"
  assert_eq "${URL}" "$(jq -r .url <<<"$b")"
  assert_eq "${HOOK_TOKEN}" "$(jq -r .token <<<"$b")"
  for k in issues_events note_events enable_ssl_verification; do assert_eq true "$(jq -r ".${k}" <<<"$b")"; done
  for k in push_events merge_requests_events tag_push_events pipeline_events job_events wiki_page_events \
    confidential_issues_events confidential_note_events deployment_events releases_events; do
    assert_eq false "$(jq -r ".${k}" <<<"$b")"
  done
  assert_contains 'PASS' "$OUT"
}

test_add_updates_existing() {
  setup_case
  jq -n --arg u "${URL}" '[{id: 1, url: "https://el-gitlab-krci.example"}, {id: 7, url: $u, issues_events: false}]' >"${SD}/hooks.json"
  run_script add krci/test-go-app "${URL}"; assert_eq 0 $?
  assert_contains 'PUT projects/12/hooks/7' "$(cat "${SD}/requests")"
  assert_not_contains 'POST' "$(cat "${SD}/requests")"
  assert_not_contains 'hooks/1' "$(cat "${SD}/requests")"
}

test_remove_matches_exact_url() {
  setup_case
  jq -n --arg u "${URL}" '[{id: 1, url: "https://el-gitlab-krci.example"}, {id: 2, url: ($u + "/extra")}, {id: 7, url: $u}]' >"${SD}/hooks.json"
  run_script remove krci/test-go-app "${URL}"; assert_eq 0 $?
  assert_eq 'DELETE projects/12/hooks/7' "$(grep DELETE "${SD}/requests")"
}

test_remove_none_is_ok() {
  setup_case; run_script remove krci/test-go-app "${URL}"; assert_eq 0 $?
  assert_not_contains 'DELETE' "$(cat "${SD}/requests")"
}

test_list_shows_exact_url_only() {
  setup_case
  jq -n --arg u "${URL}" '[{id: 1, url: "https://el-gitlab-krci.example"}, {id: 7, url: $u, issues_events: true, note_events: true, enable_ssl_verification: true}]' >"${SD}/hooks.json"
  run_script list krci/test-go-app "${URL}"; assert_eq 0 $?
  assert_contains '7' "$OUT"; assert_not_contains 'el-gitlab-krci' "$OUT"
}

test_tokens_not_in_argv() {
  setup_case
  run_script add krci/test-go-app "${URL}"
  run_script list krci/test-go-app "${URL}"
  run_script remove krci/test-go-app "${URL}"
  assert_not_contains "${GS_TOKEN}" "$(cat "${STUB_LOG}")$OUT"
  assert_not_contains "${HOOK_TOKEN}" "$(cat "${STUB_LOG}")$OUT"
}

test_api_error_fails() {
  setup_case; ACCEPT_TOKEN=other-token run_script add krci/test-go-app "${URL}"; assert_eq 1 $?
  assert_contains 'FAIL' "$OUT"
}

test_resolve_option() {
  setup_case; OUT="$(PATH="${SD}/bin:${PATH}" GITLAB_URL=https://gitlab.example GITLAB_RESOLVE=gitlab.example:443:127.0.0.1 \
    bash "${SCRIPT}" list krci/test-go-app "${URL}" 2>&1)"; assert_eq 0 $?
  assert_contains '--resolve gitlab.example:443:127.0.0.1' "$(cat "${STUB_LOG}")"
  setup_case; run_script list krci/test-go-app "${URL}"; assert_not_contains '--resolve' "$(cat "${STUB_LOG}")"
}

test_usage() { setup_case; run_script bogus; assert_eq 2 $?; assert_contains usage "$OUT"; }

test_requires_gitlab_url() {
  setup_case; OUT="$(PATH="${SD}/bin:${ORIG_PATH}" GITLAB_URL= bash "${SCRIPT}" list krci/test-go-app "${URL}" 2>&1)"; assert_eq 2 $?
  assert_contains GITLAB_URL "$OUT"
}

run_tests
