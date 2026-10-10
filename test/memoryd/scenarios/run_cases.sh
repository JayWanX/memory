#!/usr/bin/env bash

set -euo pipefail

RUNNER_DIR=$(cd "$(dirname "$0")" && pwd -P)
# shellcheck source=lib.sh
source "$RUNNER_DIR/lib.sh"

requested=${1:-}
cases_root="$R7_REPOSITORY_ROOT/testdata/memoryd/cases"

r7_require_tools
r7_build_image

cleanup() {
  r7_end_case
  if test "$R7_KEEP" != 1; then
    docker image rm "$R7_IMAGE" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

ran=0
for case_dir in "$cases_root"/*; do
  test -d "$case_dir" || continue
  case_name=$(basename "$case_dir")
  if test -n "$requested" && test "$case_name" != "$requested"; then
    continue
  fi
  test -x "$case_dir/oracle.sh" || r7_fail "$case_name has no executable oracle.sh"
  r7_begin_case "$case_dir"
  # The oracle owns semantics; the runner owns only isolated infrastructure.
  # shellcheck disable=SC1090
  source "$case_dir/oracle.sh"
  r7_run_case "$case_dir"
  r7_end_case
  ran=$((ran + 1))
  printf 'r7 case passed: %s\n' "$case_name"
done

test "$ran" -gt 0 || r7_fail "no matching R7 case"
printf 'r7 oracle: one candidate image and binary pair ran %d case(s): %s\n' \
  "$ran" "$R7_IMAGE_ID"
