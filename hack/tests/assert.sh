#!/usr/bin/env bash
# Minimal bash test harness. Source it, define test_* functions, call run_tests.
_failed=0
_case_failed=0

assert_eq() {
  if [ "$1" != "$2" ]; then echo "    expected: '$1'"; echo "    actual:   '$2'"; _case_failed=1; fi
}
assert_contains() {
  if [[ "$2" != *"$1"* ]]; then echo "    missing: '$1'"; echo "    in: $2"; _case_failed=1; fi
}
assert_not_contains() {
  if [[ "$2" == *"$1"* ]]; then echo "    unexpected: '$1'"; echo "    in: $2"; _case_failed=1; fi
}

run_tests() {
  local t
  for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
    _case_failed=0
    "$t"
    if [ "${_case_failed}" = 0 ]; then echo "ok   $t"; else echo "FAIL $t"; _failed=$((_failed + 1)); fi
  done
  [ "${_failed}" = 0 ] || { echo "${_failed} test(s) failed"; exit 1; }
}
