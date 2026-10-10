#!/usr/bin/env bash

# Hermetic proof of the real service world. This script detects both regional
# variants and verifies physical domain isolation; it deliberately performs no
# Agent repair.

set -euo pipefail

runner_dir=$(cd "$(dirname "$0")" && pwd -P)
repository_root=$(cd "$runner_dir/../../.." && pwd -P)
case_root="$repository_root/testdata/memoryd/domainops"
compose_file="$case_root/compose.yaml"
project="mnr7-domain-ops-$$"
first_prefix="incident-a-$$"
second_prefix="incident-b-$$"
runtime_dir=$(mktemp -d /tmp/mnr7-domain-ops.XXXXXX)

fail() {
  printf 'r7 domain ops world: %s\n' "$*" >&2
  return 1
}

cleanup() {
  docker compose -p "$project" -f "$compose_file" down --volumes --remove-orphans \
    >/dev/null 2>&1 || true
  if test -d "$runtime_dir" && test ! -L "$runtime_dir"; then
    case "$runtime_dir" in
      /tmp/mnr7-domain-ops.??????|/private/tmp/mnr7-domain-ops.??????)
        rm -f -- "$runtime_dir"/*
        rmdir "$runtime_dir"
        ;;
      *) printf 'r7 domain ops world: refusing to remove unexpected temporary path\n' >&2 ;;
    esac
  fi
}

require_tools() {
  command -v docker >/dev/null 2>&1 || fail 'docker is required'
  command -v jq >/dev/null 2>&1 || fail 'jq is required'
  docker info >/dev/null 2>&1 || fail 'Docker Engine is unavailable'
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose is required'
}

compose() {
  docker compose -p "$project" -f "$compose_file" "$@"
}

assert_networks() {
  local service=$1 expected=$2 actual
  actual=$(docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' \
    "$(compose ps -q "$service")" | sed "s/^${project}_//" | sed '/^$/d' | sort | \
    tr '\n' ',' | sed 's/,$//')
  test "$actual" = "$expected" || fail "$service networks = $actual, want $expected"
}

trap cleanup EXIT HUP INT TERM
require_tools
compose build --quiet
compose up -d --wait ledger callback-east callback-west payment-east payment-west gateway monitor

compose --profile tools run --rm --no-deps load \
  --gateway-url http://gateway:8080 --monitor-url http://monitor:8080 \
  --prefix "$first_prefix" --count 4 --settle 1s >"$runtime_dir/incident-a.json"

jq -e '
  .sent == 4 and .accepted == 4 and .failed == 0 and
  (.receipts | length) == 4 and all(.receipts[]; .capture_id > 0) and
  .observed.gateway.succeeded == 4 and .observed.gateway.failed == 0 and
  .observed.ledger.charges == 8 and .observed.ledger.active_charges == 8 and
  .observed.ledger.voided_charges == 0 and
  .observed.ledger.unique_businesses == 4 and
  .observed.ledger.duplicate_businesses == 4
' "$runtime_dir/incident-a.json" >/dev/null || fail 'the first hidden service fault was not observed'

assert_networks gateway 'checkout-public,edge-ops,gateway-payment,monitor-gateway'
assert_networks payment-east 'gateway-payment,payment-callback,payment-ops'
assert_networks callback-east 'callback-ledger,payment-callback,platform-ops'
assert_networks ledger 'callback-ledger,data-ops,monitor-data'
assert_networks monitor 'lead-ops,monitor-data,monitor-gateway'

compose --profile tools run --rm --no-deps edge-tool status >"$runtime_dir/edge.json"
compose --profile tools run --rm --no-deps edge-tool \
  read "/history?prefix=$first_prefix" >"$runtime_dir/edge-history.json"
compose --profile tools run --rm --no-deps payment-tool \
  --endpoint http://payment-east:8080 status >"$runtime_dir/payment.json"
compose --profile tools run --rm --no-deps platform-tool \
  --endpoint http://callback-east:8080 status >"$runtime_dir/platform.json"
compose --profile tools run --rm --no-deps data-tool status "$first_prefix" >"$runtime_dir/data.json"
compose --profile tools run --rm --no-deps lead-tool status "$first_prefix" >"$runtime_dir/lead.json"

for report in edge payment platform data lead; do
  jq -e '.role == $role and (.result | type == "object")' \
    --arg role "$report" "$runtime_dir/$report.json" >/dev/null ||
    fail "$report domain could not inspect its own bounded surface"
done

jq -e --slurpfile incident "$runtime_dir/incident-a.json" '
  .role == "edge" and
  (.result.limit | type == "number") and
  .result.limit >= (.result.entries | length) and .result.limit <= 256 and
  (.result.entries | length) == 4 and
  all(.result.entries[];
    .route == "east" and .status == "succeeded" and .capture_id > 0) and
  ([.result.entries[] | {business_id, capture_id}] | sort_by(.business_id)) ==
  ($incident[0].receipts | sort_by(.business_id))
' "$runtime_dir/edge-history.json" >/dev/null ||
  fail 'edge history does not preserve the exact receipts returned to callers'

# Move the hidden fixture to its second episode. These runner-only controls do
# not choose an Agent remediation; they establish the region-reversed world
# that the paid case later asks fresh Runtime processes to diagnose.
compose --profile tools run --rm --no-deps payment-tool \
  --endpoint http://payment-east:8080 action /admin/config \
  '{"timeout_ms":500,"stable_keys":true,"retries":2}' >/dev/null
compose --profile tools run --rm --no-deps platform-tool \
  --endpoint http://callback-east:8080 action /admin/latency \
  '{"latency_ms":5}' >/dev/null
compose --profile tools run --rm --no-deps lead-tool probe >"$runtime_dir/synthetic-probe.json"
jq -e '
  .role == "lead" and
  .result.receipt.business_id == "synthetic-001" and
  .result.receipt.status == "succeeded" and
  .result.receipt.capture_id > 0 and
  .result.observed.charges == 1 and .result.observed.active_charges == 1 and
  .result.observed.voided_charges == 0 and
  .result.ledger.charges == 1 and .result.ledger.active_charges == 1 and
  .result.ledger.voided_charges == 0 and
  .result.ledger.unique_businesses == 1 and
  .result.ledger.duplicate_businesses == 0
' "$runtime_dir/synthetic-probe.json" >/dev/null ||
  fail 'the bounded lead probe did not traverse the real checkout path'
compose --profile tools run --rm --no-deps platform-tool \
  --endpoint http://callback-west:8080 action /admin/latency \
  '{"latency_ms":300}' >/dev/null
compose --profile tools run --rm --no-deps payment-tool \
  --endpoint http://payment-west:8080 action /admin/config \
  '{"timeout_ms":100,"stable_keys":false,"retries":2}' >/dev/null
compose --profile tools run --rm --no-deps edge-tool \
  action /admin/route '{"route":"west"}' >/dev/null
compose --profile tools run --rm --no-deps load \
  --gateway-url http://gateway:8080 --monitor-url http://monitor:8080 \
  --prefix "$second_prefix" --count 4 --settle 1s >"$runtime_dir/incident-b.json"

jq -e '
  .sent == 4 and .accepted == 4 and .failed == 0 and
  (.receipts | length) == 4 and all(.receipts[]; .capture_id > 0) and
  .observed.gateway.route == "west" and
  .observed.ledger.charges == 8 and .observed.ledger.active_charges == 8 and
  .observed.ledger.voided_charges == 0 and
  .observed.ledger.unique_businesses == 4 and
  .observed.ledger.duplicate_businesses == 4
' "$runtime_dir/incident-b.json" >/dev/null ||
  fail 'the region-reversed hidden service fault was not observed'

printf 'r7 domain ops world: PASS (two real regional faults, bounded public probe, receipt history, five isolated domain surfaces)\n'
