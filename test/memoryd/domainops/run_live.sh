#!/usr/bin/env bash

# Opt-in, paid, real-provider proof for federated domain operations. The
# runner fixes only the physical world and bounded attention opportunities.
# Diagnosis, Event vocabulary, peer choice, and remediation remain Agent-owned.

set -euo pipefail
umask 077

# Consume the caller's exported credential before even path discovery starts a
# child process. The private copy is a shell variable only; it is streamed once
# over stdin to each container-local FIFO and never enters argv or env.
provider_key=${DEEPSEEK_API_KEY:-}
unset DEEPSEEK_API_KEY
export -n provider_key

runner_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
repository_root=$(cd "$runner_dir/../../.." && pwd -P)
case_root="$repository_root/testdata/memoryd/domainops"
compose_file="$case_root/compose.yaml"
mission_file="$case_root/mission.md"

pi_version=0.83.0
pi_model=${DOMAIN_OPS_PI_MODEL:-deepseek-v4-flash}
pi_thinking=${DOMAIN_OPS_PI_THINKING:-high}
turn_seconds=${DOMAIN_OPS_TURN_SECONDS:-150}
persisted_evidence_max_bytes=$((8 * 1024 * 1024))
persisted_evidence_max_blocks=$((persisted_evidence_max_bytes / 1024))
peer_quiescence_seconds=30
open_attention_turn_limit=16
monitor_probe_limit=128
monitor_probe_charge_limit=4
gateway_history_limit=192
scenario_episode_count=2
baseline_load_count=4
recovery_load_count=6
stability_load_count=6
scenario_customer_receipt_limit=$((scenario_episode_count *
  (baseline_load_count + recovery_load_count + stability_load_count)))
synthetic_charge_limit=$((monitor_probe_limit * monitor_probe_charge_limit))
domain_request_max_kib=32
domain_response_max_kib=128
attention_exhausted_reason='Attention budget exhausted. This tool did not run. Only memoryd_submit may remain.'
current_failed_reason='Current unavailable.'
report_path=${DOMAIN_OPS_REPORT:-$repository_root/.testdata/memoryd-domainops-live/last-report.json}
trace_path=${DOMAIN_OPS_TRACE:-$repository_root/.testdata/memoryd-domainops-live/last.trace}
failure_report_path=${DOMAIN_OPS_FAILURE_REPORT:-$repository_root/.testdata/memoryd-domainops-live/last-failure.json}
failure_trace_path=${DOMAIN_OPS_FAILURE_TRACE:-$repository_root/.testdata/memoryd-domainops-live/last-failure.trace}
roles='lead edge payment platform data'
attention_contract='This is one bounded attention opportunity, not the whole workflow. Inspect only what is useful now, make at most one accepted contribution, and stop; later turns can continue.'
neutral_attention="$attention_contract Continue the work available in this workspace. Use current evidence and your local authority, preserve uncertainty, and stop when no useful bounded action remains."
outcome_attention="$attention_contract A verified real-world outcome is now available. Re-read your bounded View and local evidence. Decide whether anything should change now or survive into a future context; doing nothing is valid."
initial_mission=

runtime_root=
project=
control_network=
agent_image=
agent_image_id=
agent_binary_digests=
agent_containers=
turn_pids=()
run_started_at=
run_finished_at=
failure_stage=runner.pre-authority
authority_started=0
authority_captured=0

fail() {
  printf 'r7 domain ops live: %s\n' "$*" >&2
  return 1
}

validate_integer() {
  local name=$1 value=$2 minimum=$3 maximum=$4
  case "$value" in
    ''|*[!0-9]*) fail "$name must be an integer" ;;
  esac
  test "$value" -ge "$minimum" && test "$value" -le "$maximum" ||
    fail "$name must be between $minimum and $maximum"
}

container_for() {
  printf '%s-agent-%s\n' "$project" "$1"
}

endpoint_for() {
  case "$1" in
    lead) printf 'http://monitor:8080\n' ;;
    edge) printf 'http://gateway:8080\n' ;;
    payment) printf 'http://payment-east:8080\n' ;;
    platform) printf 'http://callback-east:8080\n' ;;
    data) printf 'http://ledger:8080\n' ;;
    *) fail "unknown domain role $1" ;;
  esac
}

compose() {
  docker compose -p "$project" -f "$compose_file" "$@"
}

cleanup() {
  local pid container
  provider_key=
  for pid in "${turn_pids[@]:-}"; do
    test -n "$pid" || continue
    kill -TERM "$pid" >/dev/null 2>&1 || true
    wait "$pid" >/dev/null 2>&1 || true
  done
  for container in $agent_containers; do
    docker rm -f "$container" >/dev/null 2>&1 || true
  done
  if test -n "$control_network"; then
    docker network rm "$control_network" >/dev/null 2>&1 || true
  fi
  if test -n "$project"; then
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  if test -n "$agent_image"; then
    docker image rm "$agent_image" >/dev/null 2>&1 || true
  fi
  if test -n "${DOMAIN_OPS_IMAGE_TAG:-}"; then
    docker image rm "memory-domain-ops-world:$DOMAIN_OPS_IMAGE_TAG" >/dev/null 2>&1 || true
  fi
  if test -n "$runtime_root" && test -d "$runtime_root" && test ! -L "$runtime_root"; then
    case "$runtime_root" in
      /tmp/mnr7-domain-live.??????|/private/tmp/mnr7-domain-live.??????)
        chmod -R u+w "$runtime_root" >/dev/null 2>&1 || true
        rm -rf -- "$runtime_root"
        ;;
      *) printf 'r7 domain ops live: refusing to remove unexpected temporary path\n' >&2 ;;
    esac
  fi
}

on_exit() {
  local original_status=$?
  trap - EXIT HUP INT TERM
  set +e
  if test "$original_status" -ne 0; then
    finalize_failure_evidence "$failure_stage"
  fi
  cleanup
  exit "$original_status"
}

require_prerequisites() {
  test "${LIVE_DOMAIN_OPS:-}" = 1 ||
    fail 'set LIVE_DOMAIN_OPS=1 to authorize paid real-provider turns'
  test -n "$provider_key" || fail 'DEEPSEEK_API_KEY is required'
  validate_integer DOMAIN_OPS_TURN_SECONDS "$turn_seconds" 30 300
  case "$pi_model" in
    ''|*[!a-zA-Z0-9._-]*) fail 'DOMAIN_OPS_PI_MODEL is invalid' ;;
  esac
  case "$pi_thinking" in
    off|minimal|low|medium|high|xhigh|max) ;;
    *) fail 'DOMAIN_OPS_PI_THINKING is invalid' ;;
  esac
  command -v docker >/dev/null 2>&1 || fail 'docker is required'
  command -v jq >/dev/null 2>&1 || fail 'jq is required'
  command -v sqlite3 >/dev/null 2>&1 ||
    fail 'sqlite3 is required for the authority-state oracle'
  docker info >/dev/null 2>&1 || fail 'Docker Engine is unavailable'
  docker compose version >/dev/null 2>&1 || fail 'Docker Compose is required'
  test -f "$compose_file" || fail 'domain-ops compose fixture is missing'
  test -f "$runner_dir/Dockerfile" || fail 'domain-ops Dockerfile is missing'
  test -s "$mission_file" || fail 'domain-ops mission fixture is missing or empty'
  test "$gateway_history_limit" -ge \
    $((monitor_probe_limit + scenario_customer_receipt_limit)) ||
    fail 'gateway history cannot retain the bounded scenario and probe envelope'
  test "$(wc -c <"$mission_file" | tr -d ' ')" -le 1024 ||
    fail 'domain-ops mission exceeds its 1 KiB prompt bound'
  initial_mission=$(<"$mission_file")
  test -n "$initial_mission" || fail 'domain-ops mission is empty after reading'
}

build_and_start_world() {
  export DOMAIN_OPS_IMAGE_TAG="live-$$"
  compose build --quiet
  docker build --quiet --target agent -f "$runner_dir/Dockerfile" \
    -t "$agent_image" "$repository_root" >/dev/null
  agent_image_id=$(docker image inspect --format '{{.Id}}' "$agent_image")
  agent_binary_digests=$(docker run --rm --entrypoint sha256sum "$agent_image" \
    /usr/local/bin/memory /usr/local/bin/domainctl \
    /opt/memory/pi-delegate/delegate.ts /opt/memory/pi-delegate/delegate-runtime.mjs)
  test -n "$agent_image_id" && test -n "$agent_binary_digests" ||
    fail 'candidate Agent image identity is unavailable'
  printf '%s\n' "$agent_binary_digests" >"$runtime_root/candidate-binaries.sha256"
  chmod 0600 "$runtime_root/candidate-binaries.sha256"
  compose up -d --wait ledger callback-east callback-west payment-east payment-west \
    gateway monitor
}

run_load() {
  local prefix=$1 count=$2 output=$3
  compose --profile tools run --rm --no-deps load \
    --gateway-url http://gateway:8080 --monitor-url http://monitor:8080 \
    --prefix "$prefix" --count "$count" --settle 1s >"$output"
}

seed_incident() {
  local episode=$1 prefix=$2 expected_route=$3
  run_load "$prefix" "$baseline_load_count" "$runtime_root/$episode-baseline.json"
  jq -e --arg route "$expected_route" --argjson count "$baseline_load_count" '
    .sent == $count and .accepted == $count and .failed == 0 and
    (.receipts | length) == $count and
    ([.receipts[].business_id] | unique | length) == $count and
    all(.receipts[]; .capture_id > 0) and
    .observed.ledger.charges == ($count * 2) and
    .observed.ledger.active_charges == ($count * 2) and
    .observed.ledger.unique_businesses == $count and
    .observed.ledger.duplicate_businesses == $count and
    .observed.gateway.route == $route
  ' "$runtime_root/$episode-baseline.json" >/dev/null ||
    fail 'the hidden production incident was not established'
}

inject_second_variant() {
  # This runner-only mutation reverses the affected region while preserving the
  # same externally visible fault family. It is never mounted or prompted into
  # an Agent workspace. Resetting the first region here prevents the second
  # episode from inheriting whichever valid remediation episode 1 happened to
  # choose; the oracle still does not prescribe how episode 2 is recovered.
  compose --profile tools run --rm --no-deps platform-tool \
    --endpoint http://callback-east:8080 action /admin/latency \
    '{"latency_ms":5}' >"$runtime_root/episode-2-reset-platform.json"
  compose --profile tools run --rm --no-deps payment-tool \
    --endpoint http://payment-east:8080 action /admin/config \
    '{"timeout_ms":500,"stable_keys":true,"retries":2}' \
    >"$runtime_root/episode-2-reset-payment.json"
  compose --profile tools run --rm --no-deps platform-tool \
    --endpoint http://callback-west:8080 action /admin/latency \
    '{"latency_ms":300}' >"$runtime_root/episode-2-injected-platform.json"
  compose --profile tools run --rm --no-deps payment-tool \
    --endpoint http://payment-west:8080 action /admin/config \
    '{"timeout_ms":100,"stable_keys":false,"retries":2}' \
    >"$runtime_root/episode-2-injected-payment.json"
  compose --profile tools run --rm --no-deps edge-tool \
    action /admin/route '{"route":"west"}' \
    >"$runtime_root/episode-2-injected-edge.json"
}

prepare_workspace() {
  local role=$1 projection_dir="$runtime_root/workspaces/$1"
  test -s "$case_root/domains/$role/AGENTS.md" ||
    fail "$role domain projection is missing or empty"
  mkdir -p "$projection_dir"
  cp "$case_root/domains/$role/AGENTS.md" "$projection_dir/AGENTS.md"
  chmod 0444 "$projection_dir/AGENTS.md"
}

start_agent_container() {
  local role=$1 container endpoint projection
  container=$(container_for "$role")
  endpoint=$(endpoint_for "$role")
  projection="$runtime_root/workspaces/$role/AGENTS.md"
  # The name is deterministic and unique to this run. Register it before
  # creation so even a partially created container is covered by cleanup.
  agent_containers="$agent_containers $container"
  docker run -d --name "$container" --hostname "$role" \
    --network "$control_network" --network-alias "$role" \
    --memory 1g --memory-swap 1g --cpus 1 --pids-limit 256 \
    --security-opt no-new-privileges:true --cap-drop ALL \
    --mount "type=bind,src=$projection,dst=/workspace/AGENTS.md,readonly" \
    --env "DOMAIN_ROLE=$role" --env "DOMAIN_ENDPOINT=$endpoint" \
    "$agent_image" >/dev/null
  # Root creates only the non-secret parent. It is writable just long enough
  # for the unprivileged Runtime to create its owned 0700 state below; setup
  # tightens the parent again before any Agent turn.
  docker exec -u 0 "$container" sh -c \
    'test ! -e /runtime || test -d /runtime; mkdir -p /runtime; chmod 0733 /runtime'
  docker network connect "${project}_${role}-ops" "$container"
  test "$(docker inspect --format '{{.Image}}' "$container")" = "$agent_image_id" ||
    fail "$role does not run the candidate Agent image"
  test "$(docker exec "$container" sha256sum /usr/local/bin/memory \
    /usr/local/bin/domainctl \
    /opt/memory/pi-delegate/delegate.ts \
    /opt/memory/pi-delegate/delegate-runtime.mjs)" = "$agent_binary_digests" ||
    fail "$role does not run the candidate Agent binaries"
}

assert_agent_boundary() {
  local role=$1 container expected actual leaked projection
  container=$(container_for "$role")
  projection="$runtime_root/workspaces/$role/AGENTS.md"
  expected=$(printf '%s\n%s\n' "$control_network" "${project}_${role}-ops" | sort |
    tr '\n' ',' | sed 's/,$//')
  actual=$(docker inspect --format \
    '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' "$container" |
    sed '/^$/d' | sort | tr '\n' ',' | sed 's/,$//')
  test "$actual" = "$expected" ||
    fail "$role Agent networks = $actual, want $expected"
  docker inspect "$container" | jq -e --arg projection "$projection" '
    length == 1 and
    (.[0].Mounts | length) == 1 and
    .[0].Mounts[0].Type == "bind" and
    .[0].Mounts[0].Source == $projection and
    .[0].Mounts[0].Destination == "/workspace/AGENTS.md" and
    .[0].Mounts[0].RW == false and
    .[0].HostConfig.Memory == 1073741824 and
    .[0].HostConfig.MemorySwap == 1073741824 and
    .[0].HostConfig.NanoCpus == 1000000000 and
    .[0].HostConfig.PidsLimit == 256 and
    (.[0].HostConfig.CapDrop | index("ALL")) != null and
    (.[0].HostConfig.SecurityOpt | index("no-new-privileges:true")) != null
  ' >/dev/null || fail "$role Agent received an unexpected mount or resource/security profile"
  docker exec "$container" sh -c '
    probe=/workspace/.projection-replace-probe
    test "$(stat -c %u /workspace)" = "$(id -u)" &&
    test "$(stat -c %a /workspace/AGENTS.md)" = 444 &&
    printf probe >"$probe" &&
    ! mv -f "$probe" /workspace/AGENTS.md 2>/dev/null &&
    ! sh -c "printf changed > /workspace/AGENTS.md" 2>/dev/null &&
    rm -f "$probe" &&
    test "$(stat -c %a /workspace/AGENTS.md)" = 444
  ' || fail "$role Agent can replace its immutable workspace projection"
  leaked=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container" |
    grep -E 'DEEPSEEK|API_KEY' || true)
  test -z "$leaked" || fail "$role Agent container received a provider credential"
}

prepare_agents() {
  local role remote container reported_version state_dir=/workspace/.memory/agency
  docker network create "$control_network" >/dev/null
  for role in $roles; do
    prepare_workspace "$role"
    start_agent_container "$role"
  done
  for role in $roles; do
    assert_agent_boundary "$role"
    container=$(container_for "$role")
    reported_version=$(docker exec "$container" pi --version)
    test "$reported_version" = "$pi_version" ||
      fail "$role Pi version = $reported_version, want $pi_version"
    docker exec -w /workspace "$container" memory agency peer prepare \
      --listen 0.0.0.0:7447 --advertise "$role:7447" --project-root /workspace \
      >"$runtime_root/cards/$role.json"
  done
  for role in $roles; do
    container=$(container_for "$role")
    for remote in $roles; do
      test "$role" = "$remote" && continue
      docker exec -i -w /workspace "$container" memory agency peer enroll \
        --alias "$remote" --project-root /workspace \
        <"$runtime_root/cards/$remote.json" >/dev/null
    done
    docker exec -w /workspace "$container" memory agency setup \
      --runtime pi --project-root /workspace >"$runtime_root/setup-$role.json"
    jq -e '.schema == "memory.setup" and .version == 1 and .status == "ready"' \
      "$runtime_root/setup-$role.json" >/dev/null || fail "$role setup was not ready"
    docker exec "$container" sh -c \
      'umask 077; mkdir -p /runtime/pi-state /workspace/.memory/live && chmod 700 /runtime/pi-state /workspace/.memory/live'
    docker exec -u 0 "$container" chmod 0711 /runtime
    docker exec -d "$container" sh -c \
      "exec memory agency serve --state-dir $state_dir >/workspace/.memory/live/memoryd.log 2>&1"
  done
  authority_started=1
  for role in $roles; do
    container=$(container_for "$role")
    local ready=0 attempt=0
    while test "$attempt" -lt 50; do
      if docker exec "$container" test -S "$state_dir/control.sock"; then
        ready=1
        break
      fi
      sleep 0.1
      attempt=$((attempt + 1))
    done
    test "$ready" = 1 || fail "$role memoryd did not become ready"
  done
}

with_deadline() {
  local seconds=$1 marker=$2 pipeline_pid pipeline_status elapsed
  shift 2
  rm -f -- "$marker"
  # Job control gives this asynchronous function and every local pipeline child
  # (docker exec and jq included) one fresh process group on macOS and Linux.
  # The synchronous owner never returns until that entire group is gone.
  set -m
  "$@" &
  pipeline_pid=$!
  elapsed=0
  while kill -0 "$pipeline_pid" 2>/dev/null; do
    if test "$elapsed" -ge "$seconds"; then
      : >"$marker"
      kill -TERM -- "-$pipeline_pid" >/dev/null 2>&1 || true
      local shutdown_elapsed=0
      while kill -0 -- "-$pipeline_pid" 2>/dev/null &&
          test "$shutdown_elapsed" -lt 5; do
        sleep 1
        shutdown_elapsed=$((shutdown_elapsed + 1))
      done
      kill -KILL -- "-$pipeline_pid" >/dev/null 2>&1 || true
      wait "$pipeline_pid" >/dev/null 2>&1 || true
      set +m
      kill -0 -- "-$pipeline_pid" >/dev/null 2>&1 && return 125
      return 124
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  if wait "$pipeline_pid"; then pipeline_status=0; else pipeline_status=$?; fi
  set +m
  return "$pipeline_status"
}

pi_process() {
  local container=$1 tag=$2 pid_file="/workspace/.memory/live/pi-$2.pid"
  docker exec -w /workspace "$container" env \
    PI_CODING_AGENT_DIR=/runtime/pi-state PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0 \
    sh -c '
      umask 077
      pid_file=$1
      model=$2
      thinking=$3
      # BusyBox setsid forks and returns immediately when invoked as the
      # docker-exec process-group leader. Starting it as a background child of
      # this non-interactive wrapper lets it become the session leader in
      # place, while the wrapper explicitly owns and joins its lifetime.
      setsid pi --mode json --print --no-session --approve --no-prompt-templates --no-themes \
        --extension /opt/memory/pi-delegate/delegate.ts \
        --provider deepseek --model "$model" --thinking "$thinking" \
        --tools bash,delegate,memoryd_current,memoryd_submit \
        @/workspace/.memory/live/turn-prompt.md &
      child=$!
      printf "%s\n" "$child" >"$pid_file"
      if wait "$child"; then status=0; else status=$?; fi
      rm -f "$pid_file"
      exit "$status"
    ' pi-turn-wrapper "$pid_file" "$pi_model" "$pi_thinking"
}

bounded_pi_process() {
  local container=$1 tag=$2
  # Bash defines -f in 1024-byte blocks on both the pinned macOS shell and
  # Linux runner. The limit is inherited by docker, the process writing both
  # redirected persisted evidence streams. Pi's message_update carries a
  # growing full message snapshot, while tool_execution_update is transient
  # progress. Neither participates in an oracle, so retaining them creates
  # quadratic output with no additional evidence. Keep final boundaries and
  # results, and fail if any
  # remaining record is malformed. The pre-filter provider stream is never
  # materialized: the kernel pipe applies backpressure and the turn deadline
  # bounds its lifetime. A separate transient byte meter would add another
  # buffering process without strengthening retained evidence. The 8 MiB file
  # limit below applies only to persisted, filtered evidence and stderr.
  ulimit -f "$persisted_evidence_max_blocks"
  pi_process "$container" "$tag" |
    jq --unbuffered -c 'select(.type != "message_update" and .type != "tool_execution_update")'
}

stop_remote_pi_pipeline() {
  local container=$1 tag=$2 pid_file="/workspace/.memory/live/pi-$2.pid"
  docker exec "$container" sh -c '
    pid_file=$1
    test -f "$pid_file" || exit 0
    IFS= read -r pid <"$pid_file"
    case "$pid" in ""|*[!0-9]*) exit 1 ;; esac
    if kill -0 -$pid 2>/dev/null; then
      kill -TERM -$pid 2>/dev/null || true
      elapsed=0
      while kill -0 -$pid 2>/dev/null && test "$elapsed" -lt 50; do
        sleep 0.1
        elapsed=$((elapsed + 1))
      done
      kill -KILL -$pid 2>/dev/null || true
    fi
    rm -f "$pid_file"
    ! kill -0 -$pid 2>/dev/null
  ' pi-turn-cleanup "$pid_file"
}

write_key_once() {
  local container=$1
  printf '%s' "$provider_key" | docker exec -i "$container" sh -c \
    'trap '\''rm -f /runtime/pi-state/provider-key.pipe /runtime/pi-state/auth.json'\'' EXIT HUP INT TERM; cat > /runtime/pi-state/provider-key.pipe'
}

sanitize_turn() {
  local role=$1 tag=$2 raw=$3 output=$4
  jq -s -e --arg role "$role" --arg turn "$tag" \
    --arg attention_exhausted "$attention_exhausted_reason" \
    --arg current_failed "$current_failed_reason" \
    --arg captured_at "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" '
    def command: (.args.command // "");
    def invocation_pattern($verb):
      (("(^|[|;&\n][[:space:]]*)([^[:space:];|&]*/)?memory" +
        "[[:space:]]+agency[[:space:]]+agent[[:space:]]+" + $verb +
        "([[:space:];|&]|$)"));
    def invocation_count($verb): [command | scan(invocation_pattern($verb))] | length;
    def invokes($verb):
      (invocation_count($verb) > 0);
    def mentions_current:
      (command | test(
        "memory[[:space:]]+agency[[:space:]]+agent[[:space:]]+current([[:space:]]|$)"));
    def invokes_exact_current:
      (command | test(
        "^[[:space:]]*memory[[:space:]]+agency[[:space:]]+agent" +
        "[[:space:]]+current[[:space:]]+--json[[:space:]]*$"));
    def is_submit_start:
      .type == "tool_execution_start" and
      (.toolName == "memoryd_submit" or
       (.toolName == "bash" and invokes("submit")));
    def domain_invocation_pattern($verb):
      (("(^|[|;&\n])[[:space:]]*([^[:space:];|&]*/)?domainctl" +
        "(?:[[:space:]]+--?(?:role|endpoint|timeout)" +
        "(?:=[^[:space:];|&]+|[[:space:]]+[^[:space:];|&]+))*" +
        "[[:space:]]+" + $verb + "([[:space:];|&]|$)"));
    def domain_invocation_count($verb):
      [command | scan(domain_invocation_pattern($verb))] | length;
    def result_texts:
      if (.result | type) == "object" and
          (.result.content? | type) == "array" then
        .result.content[] | select(.type == "text" and (.text | type) == "string") |
          .text
      elif (.result | type) == "string" then .result
      else empty end;
    def result_objects:
      [result_texts | split("\n")[] | fromjson? | select(type == "object")];
    def exact_object($required; $optional):
      . as $object |
      ($object | type) == "object" and
      all($required[]; . as $field | $object | has($field)) and
      all(($object | keys_unsorted[]);
        . as $field | (($required + $optional) | index($field)) != null);
    def bounded_string($maximum):
      type == "string" and utf8bytelength > 0 and utf8bytelength <= $maximum;
    def bounded_payload:
      type == "string" and (contains("\u0000") | not) and
      (tojson | utf8bytelength) <= 4098;
    def valid_view_artifact:
      exact_object(["digest", "handle"]; []) and
      (.digest | type == "string" and
        test("^sha256:[0-9a-f]{64}$") and
        . != "sha256:0000000000000000000000000000000000000000000000000000000000000000") and
      (.handle | bounded_string(192));
    def valid_view_semantic:
      exact_object(["kind", "payload"]; []) and
      (.kind | bounded_string(96)) and (.payload | bounded_payload);
    def valid_view_current:
      exact_object(["facts", "semantic"]; []) and
      (.semantic | valid_view_semantic) and
      (.facts | exact_object(["handle", "reply_observation_pending", "reply_required", "reply_to"];
        ["artifacts", "reply_target"])) and
      (.facts.handle | bounded_string(192)) and
      (.facts.reply_to | bounded_string(192)) and
      (.facts.reply_required | type == "boolean") and
      (.facts.reply_observation_pending | type == "boolean") and
      (.facts.reply_required == (.facts | has("reply_target"))) and
      (if .facts | has("reply_target") then
        (.facts.reply_target | bounded_string(192)) else true end) and
      ((.facts.artifacts // []) | type == "array" and length <= 8 and
        all(.[]; valid_view_artifact));
    def valid_view_related:
      exact_object(["facts", "semantic"]; []) and
      (.semantic | valid_view_semantic) and
      (.facts | exact_object(["event", "relation"]; ["artifacts", "outcome"])) and
      (.facts.event | bounded_string(192)) and
      (if .facts.relation == "correlation" then
        (.facts | has("outcome") | not)
       elif .facts.relation == "terminal_reply" then
        (.facts.outcome == "completed" or .facts.outcome == "declined" or
          .facts.outcome == "unresolved")
       else false end) and
      ((.facts.artifacts // []) | type == "array" and length <= 8 and
        all(.[]; valid_view_artifact));
    def valid_view_reference:
      exact_object(["facts"]; []) and
      (.facts | exact_object(["head", "key", "state"];
        ["artifact"])) and
      (.facts.head | bounded_string(192)) and (.facts.key | bounded_string(160)) and
      (.facts.state == "active" or .facts.state == "retracted") and
      (if .facts.state == "active" then
        (.facts | has("artifact")) and (.facts.artifact | valid_view_artifact)
       else (.facts | has("artifact") | not) end);
    def valid_view_intent:
      exact_object(["artifacts", "consequence", "subject"];
        ["reference", "successors"]) and
      all([.artifacts, .consequence, .subject][]; bounded_string(192)) and
      (if has("reference") then (.reference | bounded_string(192)) else true end) and
      (if has("successors") then (.successors | bounded_string(192)) else true end);
    def valid_agent_view:
      exact_object(["allowed_intents", "outstanding", "schema", "version", "view"];
        ["current", "provenance_handles", "references", "related", "targets"]) and
      .schema == "memory.agent.view" and .version == 8 and
      (.view | bounded_string(192)) and
      (.outstanding |
        exact_object(["open_total", "related_projected", "related_total", "truncated"];
          []) and
        all([.open_total, .related_projected, .related_total][];
          type == "number" and floor == . and . >= 0) and
        .open_total <= 64 and .related_total <= 128 and
        .related_projected <= 1 and .related_projected <= .related_total and
        (.truncated | type == "boolean") and
        .truncated == (.related_projected < .related_total)) and
      (if has("current") then
        (.current | valid_view_current) and .outstanding.open_total > 0
       else true end) and
      ((.related // []) | type == "array" and length <= 1 and
        all(.[]; valid_view_related)) and
      .outstanding.related_projected == ((.related // []) | length) and
      ((.references // []) | type == "array" and length <= 8 and
        all(.[]; valid_view_reference)) and
      ((.targets // []) | type == "array" and length <= 64 and
        all(.[]; bounded_string(192))) and
      ((.provenance_handles // []) | type == "array" and length <= 128 and
        all(.[]; bounded_string(192))) and
      (.allowed_intents | type == "array" and length <= 8 and
        all(.[]; valid_view_intent));
    def view_summary:
      . as $view |
      {has_current:($view | has("current")),
       open_total:$view.outstanding.open_total,
       related_total:$view.outstanding.related_total,
       related_projected:$view.outstanding.related_projected,
       truncated:$view.outstanding.truncated} |
      if .has_current then
        . + {reply_required:$view.current.facts.reply_required,
             reply_observation_pending:$view.current.facts.reply_observation_pending}
      else . end;
    def host_attention_disposition:
      .isError == true and
      ((.result | type) == "object") and
      ((.result | keys | sort) == ["content", "details"]) and
      .result.details == {} and
      ((.result.content | type) == "array") and
      (.result.content | length) == 1 and
      ((.result.content[0] | type) == "object") and
      ((.result.content[0] | keys | sort) == ["text", "type"]) and
      .result.content[0] == {type:"text", text:$attention_exhausted};
    def valid_receipt:
      .schema == "memory.agent.receipt" and .version == 1 and
      (.replayed | type == "boolean") and
      ((.outcome == "accepted" and
        (keys | sort) == ["outcome", "replayed", "schema", "version"]) or
       (.outcome == "rejected" and
        (keys | sort) == ["diagnostic", "outcome", "replayed", "schema", "version"] and
        (.diagnostic | type == "string" and utf8bytelength > 0 and
          utf8bytelength <= 512)));
    def valid_control_error:
      (keys | sort) == ["code", "message", "operation_id", "replayed", "retryable",
        "schema_version", "status"] and
      .schema_version == 1 and .status == "error" and .operation_id == null and
      .replayed == false and
      (.message | type == "string" and utf8bytelength > 0 and utf8bytelength <= 512) and
      (.code as $code | [
        "invalid_argument", "content_required", "content_too_large", "artifact_invalid",
        "artifact_too_large", "authentication_failed", "context_required", "context_stale",
        "asset_revision_mismatch", "action_not_allowed", "operation_mismatch",
        "operation_pending", "memoryd_unavailable", "internal"
      ] | index($code) != null) and
      (.retryable == (.code == "operation_pending" or .code == "memoryd_unavailable"));
    def valid_domain_result:
      (keys | sort) == ["result", "role"] and
      (.role | type == "string" and utf8bytelength > 0 and utf8bytelength <= 64) and
      .role == $role;
    def belongs($ids):
      (.toolCallId // null) as $id |
      $id != null and (($ids | index($id)) != null);
    def exact_empty_object:
      type == "object" and (keys | length) == 0;
    def shell_current_protocol($calls):
      reduce .[] as $record (
        {seen:[], open:[], starts:[], ends:[], violations:[]};
        if $record.type == "tool_execution_start" and
            $record.toolName == "bash" and ($record | invokes_exact_current) then
          ($record.toolCallId // null) as $id |
          if (($id | type) != "string" or ($id | length) == 0) then
            .violations += ["invalid_start_id"]
          elif (.seen | index($id)) != null then
            .violations += ["duplicate_start"]
          else
            .seen += [$id] | .open += [$id] | .starts += [$record]
          end
        elif $record.type == "tool_execution_end" and
            $record.toolName == "bash" and ($record | belongs($calls)) then
          ($record.toolCallId // null) as $id |
          if (.open | index($id)) == null then
            .violations += [if (.seen | index($id)) == null then
              "orphan_or_early_end" else "duplicate_end" end]
          else
            .open = [.open[] | select(. != $id)] | .ends += [$record]
          end
        else . end
      ) |
      .valid = ((.violations | length) == 0 and (.open | length) == 0) |
      .unfinished = (.open | length);
    def submit_protocol($calls):
      reduce .[] as $record (
        {seen:[], open:{}, starts:[], ends:[], violations:[]};
        if ($record | is_submit_start) then
          ($record.toolCallId // null) as $id |
          if (($id | type) != "string" or ($id | length) == 0) then
            .violations += ["invalid_start_id"]
          elif (.seen | index($id)) != null then
            .violations += ["duplicate_start"]
          else
            .seen += [$id] | .open[$id] = $record.toolName |
            .starts += [$record]
          end
        elif $record.type == "tool_execution_end" and
            ($record.toolName == "memoryd_submit" or ($record | belongs($calls))) then
          ($record.toolCallId // null) as $id |
          if (($id | type) != "string" or ($id | length) == 0) then
            .violations += ["invalid_end_id"]
          elif (.open | has($id) | not) then
            .violations += [if (.seen | index($id)) == null then
              "orphan_or_early_end" else "duplicate_end" end]
          elif .open[$id] != $record.toolName then
            .open |= del(.[$id]) | .violations += ["mismatched_surface"]
          else
            .open |= del(.[$id]) | .ends += [$record]
          end
        else . end
      ) |
      .valid = ((.violations | length) == 0 and (.open | length) == 0) |
      .unfinished = (.open | length);
    def native_current_protocol:
      reduce .[] as $record (
        {seen:[], open:[], starts:[], ends:[], violations:[]};
        if $record.type == "tool_execution_start" and
            $record.toolName == "memoryd_current" then
          ($record.toolCallId // null) as $id |
          if (($id | type) != "string" or ($id | length) == 0) then
            .violations += ["invalid_start_id"]
          elif ($record.args | exact_empty_object | not) then
            .violations += ["invalid_start_args"]
          elif (.seen | index($id)) != null then
            .violations += ["duplicate_start"]
          else
            .seen += [$id] | .open += [$id] | .starts += [$record]
          end
        elif $record.type == "tool_execution_end" and
            $record.toolName == "memoryd_current" then
          ($record.toolCallId // null) as $id |
          if (($id | type) != "string" or ($id | length) == 0) then
            .violations += ["invalid_end_id"]
          elif (.open | index($id)) == null then
            .violations += [if (.seen | index($id)) == null then
              "orphan_or_early_end" else "duplicate_end" end]
          else
            .open = [.open[] | select(. != $id)] | .ends += [$record]
          end
        else . end
      ) |
      .valid = ((.violations | length) == 0 and (.open | length) == 0) |
      .unfinished = (.open | length);
    def valid_delegate_result:
      (.result.details? // null) as $details |
      host_attention_disposition or
      (($details | type) == "object" and
       $details.schema == "memory.pi.delegate" and $details.version == 1 and
       (($details.status == "completed" and .isError == false) or
        (($details.status as $status |
          ["slot_used", "task_invalid", "model_unavailable", "auth_unavailable", "failed"] |
          index($status)) != null and .isError == true)));
    def valid_native_current_result:
      . as $end |
      host_attention_disposition or
      (($end.result | exact_object(["content", "details"]; [])) and
       ($end.result.details | exact_object(["schema", "status", "version"]; [])) and
       $end.result.details.schema == "memory.pi.current" and
       $end.result.details.version == 1 and
       ($end.result.content | type == "array" and length == 1) and
       ($end.result.content[0] | exact_object(["text", "type"]; [])) and
       $end.result.content[0].type == "text" and
       ($end.result.content[0].text | type == "string") and
       ($end.result.content[0].text | contains("\n") | not) and
       (if $end.result.details.status == "projected" then $end.isError == false
        elif $end.result.details.status == "failed" then
          $end.isError == true and $end.result.content[0].text == $current_failed
        else false end));
    . as $stream |
    ([$stream[] | select(.type == "message_end" and .message.role == "assistant")]) as
      $assistant_ends |
    ([$stream[] | select(.type == "tool_execution_start" and .toolName == "bash" and
      mentions_current) | .toolCallId] | unique) as $observed_current_calls |
    ([$stream[] | select(.type == "tool_execution_start" and .toolName == "bash" and
      invokes_exact_current) | .toolCallId] | unique) as $current_calls |
    ($stream | shell_current_protocol($current_calls)) as $shell_current_protocol |
    ($shell_current_protocol.starts) as $shell_current_starts |
    ($shell_current_protocol.ends) as $current_ends |
    ($stream | native_current_protocol) as $native_current_protocol |
    ($native_current_protocol.starts) as $native_current_starts |
    ([$native_current_starts[].toolCallId]) as $native_current_calls |
    ($native_current_protocol.ends) as $native_current_ends |
    ([$current_ends[] | select(.isError == false) |
      ([result_objects[] | select(.schema? == "memory.agent.view")]) as $views |
      select(($views | length) == 1 and ($views[0] | valid_agent_view)) |
      $views[0]]) as $shell_agent_views |
    ([$current_ends[] | . as $end | select(.isError == false) |
      ([result_objects[] | select(.schema? == "memory.agent.view")]) as $views |
      select(($views | length) == 1 and ($views[0] | valid_agent_view)) |
      $end.toolCallId]) as $shell_agent_view_calls |
    ([$native_current_ends[] | select(
      .isError == false and .result.details.status == "projected") |
      ([result_objects[] | select(.schema? == "memory.agent.view")]) as $views |
      select(($views | length) == 1 and ($views[0] | valid_agent_view)) |
      $views[0]]) as $native_agent_views |
    ([$native_current_ends[] | . as $end | select(
      .isError == false and .result.details.status == "projected") |
      ([result_objects[] | select(.schema? == "memory.agent.view")]) as $views |
      select(($views | length) == 1 and ($views[0] | valid_agent_view)) |
      $end.toolCallId]) as $native_agent_view_calls |
    (if ($native_current_starts | length) > 0 then
       $native_agent_views
     else
       $shell_agent_views
     end) as $agent_views |
    (if ($native_current_starts | length) > 0 then
       $native_agent_view_calls
     else
       $shell_agent_view_calls
     end) as $trusted_view_calls |
    ([range(0; $stream | length) as $index |
      $stream[$index] |
      select(.type == "tool_execution_end" and belongs($trusted_view_calls)) |
      {index:$index, tool_call_id:.toolCallId}]) as $trusted_view_ends |
    ([range(0; $stream | length) as $index |
      $stream[$index] |
      select(.type == "tool_execution_start" and
        (.toolName == "memoryd_current" or
         (.toolName == "bash" and mentions_current))) |
      {index:$index, tool_call_id:.toolCallId}]) as $current_attempts |
    ([$stream[] | select(is_submit_start) | .toolCallId] | unique) as $submit_calls |
    ($stream | submit_protocol($submit_calls)) as $submit_protocol |
    ($submit_protocol.starts) as $submit_starts |
    ($submit_protocol.ends) as $submit_ends |
    (reduce range(0; $stream | length) as $index ({};
      ($stream[$index]) as $record |
      if ($record | is_submit_start) then
        .[$record.toolCallId] = $index
      else . end
    )) as $submit_start_indexes |
    ([$submit_ends[] |
      ([result_objects[] | select(valid_receipt and .outcome == "accepted")]) as
        $accepted_receipt_objects |
      ([result_objects[] | select(valid_receipt and .outcome == "rejected")]) as
        $rejected_receipt_objects |
      ($accepted_receipt_objects | length) as $accepted_receipt_count |
      ($rejected_receipt_objects | length) as $rejected_receipt_count |
      ($accepted_receipt_count > 1 or
        ($accepted_receipt_count > 0 and $rejected_receipt_count > 0)) as
        $receipt_conflict |
      ($accepted_receipt_count == 1 and $rejected_receipt_count == 0) as $accepted |
      ($accepted_receipt_count == 0 and $rejected_receipt_count > 0) as $rejected |
      ([result_objects[] | select(valid_control_error) | .code] | unique) as $denial_codes |
      (if $receipt_conflict or $accepted or $rejected then false else
        ($denial_codes | length) > 0 end) as $denied |
      (if $receipt_conflict or $accepted or $rejected or $denied then false else
        .isError == true end) as $failed |
      {tool_call_id:.toolCallId,
        accepted_receipt_objects:$accepted_receipt_count,
        accepted:(if $accepted then 1 else 0 end),
        rejected:(if $rejected then 1 else 0 end),
        denials:(if $denied then 1 else 0 end),
        denial_code:(if $denied and ($denial_codes | length) == 1 then
          $denial_codes[0] else "" end),
        invocation_failures:(if $failed then 1 else 0 end),
        closed:(($receipt_conflict | not) and
          ($accepted or $rejected or $failed or
           ($denied and ($denial_codes | length) == 1)))}
    ]) as $submit_outcomes |
    ([$stream[] | select(.type == "tool_execution_start" and
      .toolName == "delegate") | .toolCallId] | unique) as $delegate_attempts |
    ([$stream[] | select(.type == "tool_execution_end" and
      .toolName == "delegate" and belongs($delegate_attempts))]) as $delegate_ends |
    ([$stream[] | select(.type == "tool_execution_start" and .toolName == "bash") |
      {
        tool_call_id:.toolCallId,
        read:(domain_invocation_count("status") + domain_invocation_count("read")),
        probe:domain_invocation_count("probe"),
        mutation:domain_invocation_count("action")
      } | .total = (.read + .probe + .mutation) | select(.total > 0)
    ]) as $domain_starts |
    ([$domain_starts[].tool_call_id] | unique) as $domain_calls |
    ([$stream[] | select(.type == "tool_execution_end" and .toolName == "bash" and
      belongs($domain_calls))]) as $domain_ends |
    def domain_outcome($start):
      if $start.total != 1 then "batched_unattributed"
      else first($domain_ends[] | select(.toolCallId == $start.tool_call_id)) as $end |
        if $end.isError == true then "tool_error"
        elif $end.isError == false and
          ($end | any(result_objects[]; valid_domain_result)) then "success"
        else "invalid_result" end
      end;
    def domain_summary($name):
      {attempts:([$domain_starts[] | .[$name]] | add // 0),
       successes:([$domain_starts[] as $start |
         select($start[$name] > 0 and domain_outcome($start) == "success") |
         $start[$name]] | add // 0),
       tool_errors:([$domain_starts[] as $start |
         select($start[$name] > 0 and domain_outcome($start) == "tool_error") |
         $start[$name]] | add // 0),
       invalid_results:([$domain_starts[] as $start |
         select($start[$name] > 0 and domain_outcome($start) == "invalid_result") |
         $start[$name]] | add // 0),
       batched_unattributed:([$domain_starts[] as $start |
         select($start[$name] > 0 and domain_outcome($start) == "batched_unattributed") |
         $start[$name]] | add // 0)};
    (reduce $submit_outcomes[] as $outcome (
      {accepted_seen:false, denials:0};
      .denials += (if .accepted_seen then $outcome.denials else 0 end) |
      .accepted_seen = (.accepted_seen or $outcome.accepted == 1)
    )) as $post_accept |
    {
      role: $role,
      turn: $turn,
      captured_at: $captured_at,
      hook_cues: ([$stream[] | select(
        (.type == "message_start" or .type == "message_end") and
        .message.role == "custom" and .message.customType == "memoryd")] | length),
      bash_calls: ([$stream[] | select(
        .type == "tool_execution_start" and .toolName == "bash")] | length),
      delegate_calls: ([$delegate_ends[] |
        select(.result.details.status == "completed")] | length),
      current_reads: ($agent_views | length),
      submit_attempts: ($submit_outcomes | length),
      intent_submits: ([$submit_outcomes[] | .accepted + .rejected] | add // 0),
      accepted_receipts: ([$submit_outcomes[].accepted] | add // 0),
      rejected_receipts: ([$submit_outcomes[].rejected] | add // 0),
      submit_denials: ([$submit_outcomes[].denials] | add // 0),
      submit_invocation_failures: ([$submit_outcomes[].invocation_failures] | add // 0),
      submit_control_denials: ([$submit_outcomes[] |
        select(.denials == 1) | .denial_code] | group_by(.) |
        map({code:.[0],count:length}) | sort_by(.code)),
      domain_operations:{
        read:domain_summary("read"),
        probe:domain_summary("probe"),
        mutation:domain_summary("mutation")
      },
      post_accept_denials: $post_accept.denials,
      private_binding_probes: ([$stream[] | select(.type == "tool_execution_start" and
        .toolName == "bash" and
        (command | test("DEEPSEEK|API_KEY|printenv|auth\\.json|provider-key")))] | length),
      agent_end: any($stream[]; .type == "agent_end")
    }
    | if ($agent_views | length) > 0 then
        . + {view:($agent_views[0] | view_summary)}
      else . end
    | select(
        .hook_cues >= 1 and
        ($assistant_ends | length) >= 1 and
        (($assistant_ends[-1].message.stopReason // "") != "error") and
        (($assistant_ends[-1].message.stopReason // "") != "aborted") and
        .private_binding_probes == 0 and
        .agent_end == true and
        (.submit_control_denials | length) <= 14 and
        ([.submit_control_denials[].count] | add // 0) == .submit_denials and
        ([.domain_operations[] | .[]] |
          all(. >= 0 and . <= 256)) and
        all(.domain_operations[];
          (.successes + .tool_errors + .invalid_results + .batched_unattributed) == .attempts) and
        ([.hook_cues, .bash_calls, .delegate_calls, .current_reads, .submit_attempts, .intent_submits,
          .accepted_receipts, .rejected_receipts, .submit_denials, .submit_invocation_failures,
          .post_accept_denials, .private_binding_probes] | all(. >= 0 and . <= 256)) and
        .delegate_calls <= 1 and
        (if ($native_current_starts | length) > 0 then true else
          $observed_current_calls == $current_calls and
          $shell_current_protocol.valid and
          ($shell_current_starts | length) == ($current_calls | length) and
          ($current_ends | length) == ($current_calls | length) and
          ([$current_ends[].toolCallId] | unique | sort) == ($current_calls | sort) and
          all($current_ends[]; .isError == true or .isError == false) and
          ($shell_agent_views | length) ==
            ([ $current_ends[] | select(.isError == false) ] | length)
        end) and
        ($agent_views | unique | length) <= 1 and
        $native_current_protocol.valid and
        ($native_current_ends | length) == ($native_current_calls | length) and
        ([$native_current_ends[].toolCallId] | unique | sort) ==
          ($native_current_calls | sort) and
        all($native_current_ends[]; valid_native_current_result) and
        ($native_agent_views | length) == ([$native_current_ends[] | select(
          .isError == false and .result.details.status == "projected")] | length) and
        ($delegate_ends | length) == ($delegate_attempts | length) and
        ([$delegate_ends[].toolCallId] | unique | sort) == ($delegate_attempts | sort) and
        all($delegate_ends[]; valid_delegate_result) and
        ($domain_starts | length) == ($domain_calls | length) and
        ($domain_ends | length) == ($domain_calls | length) and
        ([$domain_ends[].toolCallId] | unique | sort) == ($domain_calls | sort) and
        $submit_protocol.valid and
        ($submit_starts | length) == ($submit_calls | length) and
        ($submit_ends | length) == ($submit_calls | length) and
        ([$submit_ends[].toolCallId] | unique | sort) == ($submit_calls | sort) and
        all($submit_outcomes[];
          .closed and
          .accepted_receipt_objects <= 1 and
          (.accepted + .rejected + .denials + .invocation_failures) == 1) and
        .accepted_receipts <= 1 and
        all($submit_outcomes[];
          .accepted == 0 or
          (($submit_start_indexes[.tool_call_id] // -1) as $submit_index |
           ([$current_attempts[] | select(.index < $submit_index)]) as
             $prior_current_attempts |
           ($prior_current_attempts | length) > 0 and
           (($prior_current_attempts[-1].tool_call_id) as $last_current_call |
            any($trusted_view_ends[];
              .tool_call_id == $last_current_call and .index < $submit_index)))) and
        .post_accept_denials <= .submit_denials and
        (.post_accept_denials == 0 or .accepted_receipts == 1) and
        (.accepted_receipts + .rejected_receipts) == .intent_submits and
        (.intent_submits + .submit_denials + .submit_invocation_failures) == .submit_attempts)
  ' "$raw" >"$output" || return 1
  jq -s -e '
    all(.[] | select(.type == "tool_execution_start" and .toolName == "bash");
      ((.args.command // "") | contains("memory agency hook attach") | not))
  ' "$raw" >/dev/null
}

snapshot_accepted_events() {
  local container=$1 destination=$2 database=/workspace/.memory/agency/agency.db
  docker exec "$container" sqlite3 -readonly -batch -json -cmd '.timeout 5000' \
    "$database" '
      SELECT events.event_id AS id, events.event_digest AS digest
      FROM operations JOIN events ON events.event_id = operations.event_id
      WHERE operations.outcome = '\''accepted'\''
      ORDER BY events.origin_sequence;' >"$destination" || return 1
  test -s "$destination" || printf '[]\n' >"$destination"
  jq -e '
    type == "array" and length <= 256 and
    all(.[]; (keys | sort) == ["digest","id"] and
      (.id | type == "string") and (.digest | type == "string"))
  ' "$destination" >/dev/null
}

bind_turn_events() {
  local before=$1 after=$2 summary=$3 temporary
  temporary=$summary.events
  jq -e -n --slurpfile before "$before" --slurpfile after "$after" '
    select(all($before[0][];
      . as $prior | any($after[0][];
        .id == $prior.id and .digest == $prior.digest))) |
    ($before[0] | map(.id)) as $known |
    [$after[0][] as $event |
      select(($known | index($event.id)) == null) | $event]
  ' >"$temporary.refs" || return 1
  jq -e --slurpfile accepted "$temporary.refs" '
    ($accepted[0]) as $events |
    select(($events | length) <= 1) |
    . + {accepted_events:$events}
  ' "$summary" >"$temporary" || return 1
  test -s "$temporary" || return 1
  mv "$temporary" "$summary"
  rm -f "$temporary.refs"
}

claim_turn_window() {
  local role=$1 lock="$runtime_root/turn-locks/$1"
  mkdir "$lock" || fail "$role already has an active Runtime turn"
}

release_turn_window() {
  local role=$1 lock="$runtime_root/turn-locks/$1"
  rmdir "$lock" || fail "$role Runtime turn lock did not close cleanly"
}

summarize_partial_turn() {
  local raw=$1
  jq -s -c --arg attention_exhausted "$attention_exhausted_reason" '
    def command: (.args.command // "");
    def invocation_pattern($verb):
      (("(^|[|;&\n][[:space:]]*)([^[:space:];|&]*/)?memory" +
        "[[:space:]]+agency[[:space:]]+agent[[:space:]]+" + $verb +
        "([[:space:];|&]|$)"));
    def invocation_count($verb): [command | scan(invocation_pattern($verb))] | length;
    def mentions_current:
      (command | test(
        "memory[[:space:]]+agency[[:space:]]+agent[[:space:]]+current([[:space:]]|$)"));
    def invokes_exact_current:
      (command | test(
        "^[[:space:]]*memory[[:space:]]+agency[[:space:]]+agent" +
        "[[:space:]]+current[[:space:]]+--json[[:space:]]*$"));
    def domain_invocation_pattern($verb):
      (("(^|[|;&\n])[[:space:]]*([^[:space:];|&]*/)?domainctl" +
        "(?:[[:space:]]+--?(?:role|endpoint|timeout)" +
        "(?:=[^[:space:];|&]+|[[:space:]]+[^[:space:];|&]+))*" +
        "[[:space:]]+" + $verb + "([[:space:];|&]|$)"));
    def domain_invocation_count($verb):
      [command | scan(domain_invocation_pattern($verb))] | length;
    def is_submit_start:
      .type == "tool_execution_start" and
      (.toolName == "memoryd_submit" or
       (.toolName == "bash" and invocation_count("submit") > 0));
    def submit_count:
      if .toolName == "memoryd_submit" then 1 else invocation_count("submit") end;
    def result_strings:
      if (.result | type) == "object" and
          (.result.content? | type) == "array" then
        .result.content[] | select(.type == "text" and (.text | type) == "string") |
          .text
      elif (.result | type) == "string" then .result
      else empty end;
    def result_objects:
      [result_strings | split("\n")[] | fromjson? | select(type == "object")];
    def host_attention_disposition:
      .isError == true and
      ((.result | type) == "object") and
      ((.result | keys | sort) == ["content", "details"]) and
      .result.details == {} and
      ((.result.content | type) == "array") and
      (.result.content | length) == 1 and
      ((.result.content[0] | type) == "object") and
      ((.result.content[0] | keys | sort) == ["text", "type"]) and
      .result.content[0] == {type:"text", text:$attention_exhausted};
    def delegate_result_class:
      (.result.details? // null) as $details |
      if host_attention_disposition then "host_attention_disposition"
      elif (($details | type) == "object" and
        $details.schema == "memory.pi.delegate" and $details.version == 1 and
        $details.status == "completed" and .isError == false) then "completed"
      elif (($details | type) == "object" and
        $details.schema == "memory.pi.delegate" and $details.version == 1 and
        (($details.status as $status |
          ["slot_used", "task_invalid", "model_unavailable", "auth_unavailable", "failed"] |
          index($status)) != null and .isError == true)) then "delegate_error"
      else "invalid" end;
    def native_current_result_class:
      (.result.details? // null) as $details |
      if host_attention_disposition then "host_attention_disposition"
      elif (($details | type) == "object" and
        $details.schema == "memory.pi.current" and $details.version == 1 and
        $details.status == "projected" and .isError == false) then "projected"
      elif (($details | type) == "object" and
        $details.schema == "memory.pi.current" and $details.version == 1 and
        $details.status == "failed" and .isError == true) then "current_error"
      else "invalid" end;
    def exact_empty_object:
      type == "object" and (keys | length) == 0;
    def native_current_protocol:
      reduce .[] as $record (
        {seen:[], open:[], starts:[], ends:[], violations:[]};
        if $record.type == "tool_execution_start" and
            $record.toolName == "memoryd_current" then
          ($record.toolCallId // null) as $id |
          if (($id | type) != "string" or ($id | length) == 0) then
            .violations += ["invalid_start_id"]
          elif ($record.args | exact_empty_object | not) then
            .violations += ["invalid_start_args"]
          elif (.seen | index($id)) != null then
            .violations += ["duplicate_start"]
          else
            .seen += [$id] | .open += [$id] | .starts += [$record]
          end
        elif $record.type == "tool_execution_end" and
            $record.toolName == "memoryd_current" then
          ($record.toolCallId // null) as $id |
          if (($id | type) != "string" or ($id | length) == 0) then
            .violations += ["invalid_end_id"]
          elif (.open | index($id)) == null then
            .violations += [if (.seen | index($id)) == null then
              "orphan_or_early_end" else "duplicate_end" end]
          else
            .open = [.open[] | select(. != $id)] | .ends += [$record]
          end
        else . end
      ) |
      .valid = ((.violations | length) == 0 and (.open | length) == 0) |
      .unfinished = (.open | length);
    def valid_control_error:
      (keys | sort) == ["code", "message", "operation_id", "replayed", "retryable",
        "schema_version", "status"] and
      .schema_version == 1 and .status == "error" and .operation_id == null and
      .replayed == false and
      (.message | type == "string" and utf8bytelength > 0 and utf8bytelength <= 512) and
      (.code as $code | [
        "invalid_argument", "content_required", "content_too_large", "artifact_invalid",
        "artifact_too_large", "authentication_failed", "context_required", "context_stale",
        "asset_revision_mismatch", "action_not_allowed", "operation_mismatch",
        "operation_pending", "memoryd_unavailable", "internal"
      ] | index($code) != null) and
      (.retryable == (.code == "operation_pending" or .code == "memoryd_unavailable"));
    . as $stream |
    ([$stream[] | select(is_submit_start)]) as $submit_starts |
    ([$submit_starts[].toolCallId] | unique) as $submit_calls |
    ([$stream[] | select(.type == "tool_execution_end" and
      (.toolCallId as $id | $submit_calls | index($id) != null))]) as $submit_ends |
    ([$stream[] | select(.type == "tool_execution_start" and .toolName == "bash" and
      mentions_current)]) as $observed_current_starts |
    ([$observed_current_starts[] | select(invokes_exact_current)]) as $exact_current_starts |
    ([$exact_current_starts[].toolCallId] | unique) as $exact_current_calls |
    ([$stream[] | select(.type == "tool_execution_end" and
      (.toolCallId as $id | $exact_current_calls | index($id) != null))]) as $exact_current_ends |
    ([$stream[] | select(.type == "tool_execution_start" and
      .toolName == "memoryd_current")]) as $native_current_starts |
    ([$stream[] | select(.type == "tool_execution_end" and
      .toolName == "memoryd_current")]) as $native_current_raw_ends |
    ($stream | native_current_protocol) as $native_current_protocol |
    ($native_current_protocol.ends) as $native_current_ends |
    ([$exact_current_ends[] | select(.isError == false) | result_objects[] |
      select(.schema? == "memory.agent.view")] +
     [$native_current_ends[] | select(native_current_result_class == "projected") |
      result_objects[] | select(.schema? == "memory.agent.view")]) as $current_view_objects |
    {
      stream_records:length,
      record_types:(reduce .[] as $record ({};
        ($record.type // "unknown") as $type | .[$type] = ((.[$type] // 0) + 1))),
      message_starts:([.[] | select(.type == "message_start")] | length),
      message_boundaries:([.[] | select(
        .type == "message_start" or .type == "message_end") |
        {type,role:(.message.role // "missing"),
          custom_type:(.message.customType // "")}]),
      assistant_stop_reasons:([.[] | select(
        .type == "message_end" and .message.role == "assistant") |
        (.message.stopReason // "missing")]),
      tool_starts:([.[] | select(.type == "tool_execution_start")] | length),
      tool_ends:([.[] | select(.type == "tool_execution_end")] | length),
      tool_errors:([.[] | select(.type == "tool_execution_end" and .isError == true)] | length),
      domain_calls:([.[] | select(.type == "tool_execution_start" and
        .toolName == "bash" and (command | contains("domainctl")))] | length),
      domain_invocations:{
        read:([.[] | select(.type == "tool_execution_start" and .toolName == "bash") |
          domain_invocation_count("status") + domain_invocation_count("read")] | add // 0),
        probe:([.[] | select(.type == "tool_execution_start" and .toolName == "bash") |
          domain_invocation_count("probe")] | add // 0),
        mutation:([.[] | select(.type == "tool_execution_start" and .toolName == "bash") |
          domain_invocation_count("action")] | add // 0)
      },
      delegate_attempts:([.[] | select(.type == "tool_execution_start" and
        .toolName == "delegate")] | length),
      delegate_results:([.[] | select(.type == "tool_execution_end" and
        .toolName == "delegate") |
        {class:delegate_result_class,is_error:(.isError == true)}]),
      delegate_effects:([.[] | select(.type == "tool_execution_end" and
        .toolName == "delegate" and .isError == false and
        .result.details.schema == "memory.pi.delegate" and
        .result.details.version == 1 and .result.details.status == "completed")] | length),
      current_attempts:(($observed_current_starts | length) +
        ($native_current_starts | length)),
      current_boundary:{
        observed_starts:($observed_current_starts | length),
        exact_starts:($exact_current_starts | length),
        exact_ends:($exact_current_ends | length),
        exact_errors:([$exact_current_ends[] | select(.isError == true)] | length),
        native_starts:($native_current_starts | length),
        native_ends:($native_current_raw_ends | length),
        native_protocol_valid:$native_current_protocol.valid,
        native_unfinished:$native_current_protocol.unfinished,
        native_violations:($native_current_protocol.violations | group_by(.) |
          map({class:.[0],count:length})),
        native_results:([$native_current_raw_ends[] |
          {class:native_current_result_class,is_error:(.isError == true)}]),
        mixed_surfaces:(($observed_current_starts | length) > 0 and
          ($native_current_starts | length) > 0),
        untrusted_shell_explorations:(if ($native_current_starts | length) > 0 then
          ($observed_current_starts | length) else 0 end),
        view_objects:($current_view_objects | length),
        v7_view_objects:([$current_view_objects[] | select(
          .schema == "memory.agent.view" and .version == 8)] | length),
        unique_views:($current_view_objects | unique | length),
        one_invocation_each:all($observed_current_starts[];
          invocation_count("current") == 1)
      },
      submit_command_occurrences:([$submit_starts[] | submit_count] | add // 0),
      submit_ends:($submit_ends | length),
      submit_command_cardinality:(reduce $submit_starts[] as $start ({};
        ($start | submit_count | tostring) as $count |
        .[$count] = ((.[$count] // 0) + 1))),
      submit_control_error_codes:(reduce ([$submit_ends[] | result_objects[] |
        select(valid_control_error) | .code][]) as $code ({};
        .[$code] = ((.[$code] // 0) + 1))),
      submit_unclassified:([$submit_ends[] | select(
        (any(result_strings;
          contains("\"schema\":\"memory.agent.receipt\"") or
          contains("\"schema_version\":1") and contains("\"status\":\"error\"")) | not) and
        .isError != true)] | length),
      accepted_receipts:([$submit_ends[] | select(any(result_strings;
          contains("\"schema\":\"memory.agent.receipt\"") and
          contains("\"outcome\":\"accepted\"")))] | length),
      rejected_receipts:([$submit_ends[] | select(any(result_strings;
          contains("\"schema\":\"memory.agent.receipt\"") and
          contains("\"outcome\":\"rejected\"")))] | length),
      submit_denials:([$submit_ends[] | select(
        (any(result_strings;
          contains("\"schema\":\"memory.agent.receipt\"") and
          (contains("\"outcome\":\"accepted\"") or
            contains("\"outcome\":\"rejected\""))) | not) and
        any(result_objects[]; valid_control_error))] | length),
      submit_invocation_failures:([$submit_ends[] | select(
        (any(result_strings;
          contains("\"schema\":\"memory.agent.receipt\"") or
          contains("\"schema_version\":1") and contains("\"status\":\"error\"")) | not) and
        .isError == true)] | length),
      hook_cues:([.[] | select(
        (.type == "message_start" or .type == "message_end") and
        .message.role == "custom" and .message.customType == "memoryd")] | length),
      forbidden_hook_attach:([.[] | select(.type == "tool_execution_start" and
        .toolName == "bash" and
        (command | contains("memory agency hook attach")))] | length),
      forbidden_secret_probe:([.[] | select(.type == "tool_execution_start" and
        .toolName == "bash" and
        (command | test("DEEPSEEK|API_KEY|printenv|auth\\.json|provider-key")))] | length),
      agent_end:any(.[]; .type == "agent_end")
    }
  ' "$raw" 2>/dev/null || printf '{"stream_records":0,"parseable":false}'
}

summarize_provider_stderr() {
  local errors=$1
  jq -n -c --rawfile value "$errors" '
    def matches($pattern): ($value | test($pattern; "i"));
    {
      bytes:($value | utf8bytelength),
      auth:matches("auth|api[ -]?key|unauthori[sz]ed|http[^0-9]*401"),
      rate_limited:matches("rate.?limit|http[^0-9]*429"),
      balance:matches("insufficient|balance|payment|required|http[^0-9]*402"),
      invalid_request:matches("bad request|invalid request|http[^0-9]*400"),
      unavailable:matches("overload|unavailable|http[^0-9]*50[0234]"),
      network:matches("timed out|timeout|connection|dns|socket|tls")
    }
  '
}

run_turn() {
  local role=$1 prompt=$2 tag=$3 container raw errors sanitized marker writer status before after
  local raw_bytes error_bytes
  claim_turn_window "$role"
  container=$(container_for "$role")
  raw="$runtime_root/raw/$tag.jsonl"
  errors="$runtime_root/raw/$tag.err"
  sanitized="$runtime_root/sanitized/$tag.json"
  marker="$runtime_root/raw/$tag.timeout"
  before="$runtime_root/raw/$tag.events-before.json"
  after="$runtime_root/raw/$tag.events-after.json"
  snapshot_accepted_events "$container" "$before" ||
    fail "$role turn $tag could not freeze its pre-turn accepted Event boundary"
  printf '%s\n' "$prompt" | docker exec -i "$container" sh -c \
    'umask 077; cat > /workspace/.memory/live/turn-prompt.md'
  docker exec "$container" sh -c \
    'rm -f /runtime/pi-state/provider-key.pipe /runtime/pi-state/auth.json && mkfifo /runtime/pi-state/provider-key.pipe && chmod 600 /runtime/pi-state/provider-key.pipe && jq -cn --arg command "!cat /runtime/pi-state/provider-key.pipe" '\''{deepseek:{type:"api_key",key:$command}}'\'' > /runtime/pi-state/auth.json && chmod 600 /runtime/pi-state/auth.json'
  write_key_once "$container" &
  writer=$!
  if with_deadline "$turn_seconds" "$marker" bounded_pi_process "$container" "$tag" \
      >"$raw" 2>"$errors"; then
    status=0
  else
    status=$?
  fi
  if ! stop_remote_pi_pipeline "$container" "$tag"; then
    rm -f -- "$raw" "$errors" "$marker"
    fail "$role turn $tag did not terminate its complete Pi process group"
  fi
  if kill -0 "$writer" 2>/dev/null; then
    kill -TERM "$writer" >/dev/null 2>&1 || true
  fi
  if wait "$writer"; then :; else
    rm -f -- "$raw" "$errors" "$marker"
    fail "$role turn $tag did not consume its one-shot credential"
  fi
  docker exec "$container" sh -c \
    'test ! -e /runtime/pi-state/provider-key.pipe && test ! -e /runtime/pi-state/auth.json' || {
    rm -f -- "$raw" "$errors" "$marker"
    fail "$role turn $tag left its credential pipe behind"
  }
  docker exec "$container" rm -f /workspace/.memory/live/turn-prompt.md
  raw_bytes=$(wc -c <"$raw" | tr -d '[:space:]')
  error_bytes=$(wc -c <"$errors" | tr -d '[:space:]')
  if test "$raw_bytes" -ge "$persisted_evidence_max_bytes" ||
      test "$error_bytes" -ge "$persisted_evidence_max_bytes"; then
    rm -f -- "$raw" "$errors" "$marker"
    fail "$role turn $tag exceeded the ${persisted_evidence_max_bytes}-byte persisted-evidence bound; retained output was deleted"
  fi
  if test "$status" -ne 0; then
    local reason='provider turn failed' partial
    test "$status" -ne 124 || reason="provider turn exceeded ${turn_seconds}s"
    if grep -Eqi 'auth|api[ -]?key|unauthori[sz]ed|http 401' "$errors"; then
      reason='DeepSeek rejected the one-shot credential'
    elif grep -Eqi 'rate.?limit|http 429' "$errors"; then
      reason='DeepSeek rate-limited the live case'
    fi
    partial=$(summarize_partial_turn "$raw")
    rm -f -- "$raw" "$errors" "$marker"
    fail "$role turn $tag: $reason; partial=$partial; raw provider output was deleted"
  fi
  jq -e . "$raw" >/dev/null 2>&1 || {
    rm -f -- "$raw" "$errors" "$marker"
    fail "$role turn $tag did not emit a canonical JSON stream"
  }
  sanitize_turn "$role" "$tag" "$raw" "$sanitized" || {
    local partial provider_error
    partial=$(summarize_partial_turn "$raw")
    provider_error=$(summarize_provider_stderr "$errors")
    rm -f -- "$raw" "$errors" "$marker"
    fail "$role turn $tag violated the Hook/submit/terminal boundary; partial=$partial; provider_error=$provider_error"
  }
  snapshot_accepted_events "$container" "$after" || {
    rm -f -- "$raw" "$errors" "$marker" "$before" "$after"
    fail "$role turn $tag could not freeze its post-turn accepted Event boundary"
  }
  bind_turn_events "$before" "$after" "$sanitized" || {
    rm -f -- "$raw" "$errors" "$marker" "$before" "$after" "$sanitized"
    fail "$role turn $tag accepted Event turn boundary is invalid"
  }
  rm -f -- "$raw" "$errors" "$marker" "$before" "$after"
  release_turn_window "$role"
}

run_agents() {
  local episode=$1 initial_attention
  initial_attention=$(printf '%s\n\n%s' "$initial_mission" "$attention_contract")
  run_turn lead "$initial_attention" "$episode-initial-lead"
  wait_for_peer_delivery_quiescence "$episode-initial-lead"
}

run_post_outcome_attention() {
  local episode=$1
  run_turn lead "$outcome_attention" "$episode-post-outcome-lead"
  wait_for_peer_delivery_quiescence "$episode-post-outcome-lead"
}

pause_agent_containers() {
  local container paused=
  for container in $agent_containers; do
    if ! docker pause "$container" >/dev/null; then
      for container in $paused; do
        docker unpause "$container" >/dev/null 2>&1 || true
      done
      return 1
    fi
    paused="$paused $container"
  done
}

unpause_agent_containers() {
  local container failed=0
  for container in $agent_containers; do
    if ! docker unpause "$container" >/dev/null; then failed=1; fi
  done
  test "$failed" = 0
}

capture_authority_snapshot() {
  local destination=$1 role container failed=0
  rm -rf -- "$destination"
  mkdir -p "$destination"
  pause_agent_containers || return 1
  for role in $roles; do
    container=$(container_for "$role")
    mkdir -p "$destination/$role"
    if ! docker cp "$container:/workspace/.memory/agency/." \
        "$destination/$role" >/dev/null; then
      failed=1
      break
    fi
  done
  unpause_agent_containers || failed=1
  test "$failed" = 0
}

snapshot_peer_delivery_occupancy() {
  local attempt=$1 snapshot role database values pending staged total=0
  snapshot="$runtime_root/quiescence/snapshot-$attempt"
  : >"$runtime_root/quiescence/counts.jsonl"
  capture_authority_snapshot "$snapshot" || return 1

  for role in $roles; do
    database="$snapshot/$role/agency.db"
    values=$(sqlite3 -readonly -batch -cmd '.timeout 5000' \
      -cmd 'PRAGMA query_only=ON;' "$database" \
      'SELECT (SELECT COUNT(*) FROM peer_outbox WHERE state = '\''pending'\''),
              (SELECT COUNT(*) FROM peer_inbox WHERE state = '\''staged'\'');') || {
      rm -rf -- "$snapshot"
      return 1
    }
    IFS='|' read -r pending staged <<EOF
$values
EOF
    case "$pending" in ''|*[!0-9]*) rm -rf -- "$snapshot"; return 1 ;; esac
    case "$staged" in ''|*[!0-9]*) rm -rf -- "$snapshot"; return 1 ;; esac
    total=$((total + pending + staged))
    jq -cn --arg role "$role" --argjson pending "$pending" --argjson staged "$staged" \
      '{role:$role,pending_outbox:$pending,staged_inbox:$staged}' \
      >>"$runtime_root/quiescence/counts.jsonl"
  done
  rm -rf -- "$snapshot"
  printf '%s\n' "$total"
}

read_open_attention_counts() {
  local database=$1
  sqlite3 -readonly -batch -cmd '.timeout 5000' \
    -cmd 'PRAGMA query_only=ON;' "$database" '
      SELECT
        (SELECT COUNT(*) FROM handlings
          WHERE state = '\''open'\'' AND claim_attachment_id IS NULL),
        (SELECT COUNT(*) FROM handlings
          WHERE state = '\''open'\'' AND claim_attachment_id IS NOT NULL);'
}

snapshot_open_attention() {
  local episode=$1 wave=$2 snapshot role database values unclaimed occupied
  local nodes="$runtime_root/open-attention/$episode-wave-$wave-nodes.jsonl"
  local output="$runtime_root/open-attention/$episode-wave-$wave.json"
  snapshot="$runtime_root/open-attention/snapshot-$episode-$wave"
  : >"$nodes"
  capture_authority_snapshot "$snapshot" || return 1

  for role in $roles; do
    database="$snapshot/$role/agency.db"
    values=$(read_open_attention_counts "$database") || {
      rm -rf -- "$snapshot"
      return 1
    }
    IFS='|' read -r unclaimed occupied <<EOF
$values
EOF
    case "$unclaimed" in ''|*[!0-9]*) rm -rf -- "$snapshot"; return 1 ;; esac
    case "$occupied" in ''|*[!0-9]*) rm -rf -- "$snapshot"; return 1 ;; esac
    jq -cn --arg role "$role" --argjson unclaimed "$unclaimed" \
      --argjson occupied "$occupied" \
      '{role:$role,open_unclaimed:$unclaimed,occupied_claims:$occupied}' >>"$nodes"
  done
  rm -rf -- "$snapshot"
  jq -s '.' "$nodes" >"$output"
  printf '%s\n' "$output"
}

run_open_attention_wave() {
  local episode=$1 wave=$2 snapshot=$3 role pid failed=0
  local wave_pids=()
  while IFS= read -r role; do
    run_turn "$role" "$neutral_attention" \
      "$episode-open-attention-$wave-$role" &
    pid=$!
    wave_pids+=("$pid")
    turn_pids+=("$pid")
  done < <(jq -r '.[] | select(.open_unclaimed > 0) | .role' "$snapshot")
  for pid in "${wave_pids[@]}"; do
    if ! wait "$pid"; then failed=1; fi
  done
  if test "$failed" != 0; then
    fail "$episode open-attention wave $wave did not finish cleanly"
    return 1
  fi
  turn_pids=()
  wait_for_peer_delivery_quiescence "$episode-open-attention-$wave"
}

validate_episode_goal() {
  local episode=$1 source=$2
  jq -er --arg episode "$episode" \
    --argjson canary_count_limit "$monitor_probe_charge_limit" '
    def exact_keys($value; $names):
      ($value | type) == "object" and (($value | keys | sort) == ($names | sort));
    def bounded_integer($maximum):
      type == "number" and floor == . and . >= 0 and . <= $maximum;
    def valid_ledger($value; $maximum):
      exact_keys($value; ["charges","active_charges","voided_charges",
        "unique_businesses","duplicate_businesses"]) and
      ([$value.charges,$value.active_charges,$value.voided_charges,
        $value.unique_businesses,$value.duplicate_businesses] |
        all(.[]; bounded_integer($maximum))) and
      $value.active_charges + $value.voided_charges == $value.charges and
      $value.unique_businesses <= $value.charges and
      $value.duplicate_businesses <= $value.unique_businesses;
    def historical_ok($value):
      $value == {charges:8,active_charges:4,voided_charges:4,
        unique_businesses:4,duplicate_businesses:0};
    def clean_canary_ledger($value):
      $value == {charges:1,active_charges:1,voided_charges:0,
        unique_businesses:1,duplicate_businesses:0};
    def valid_canary($value):
      exact_keys($value; ["receipt_status","capture_id_present","observed","settled"]) and
      ($value.receipt_status == "succeeded" or $value.receipt_status == "failed") and
      ($value.capture_id_present | type) == "boolean" and
      (($value.receipt_status == "succeeded") == $value.capture_id_present) and
      valid_ledger($value.observed; $canary_count_limit) and
      valid_ledger($value.settled; $canary_count_limit);
    select(exact_keys(.; ["schema","version","episode","satisfied","observed","canary"]) and
      .schema == "memory.r7.domain-ops.goal" and .version == 2 and
      .episode == $episode and (.satisfied | type) == "boolean" and
      valid_ledger(.observed; 1000000)) |
    (historical_ok(.observed)) as $historical |
    select((if $historical then valid_canary(.canary) else .canary == null end)) |
    ($historical and .canary.receipt_status == "succeeded" and
      .canary.capture_id_present and clean_canary_ledger(.canary.observed) and
      clean_canary_ledger(.canary.settled)) as $derived |
    select(.satisfied == $derived) |
    if .satisfied then "true" else "false" end
  ' "$source"
}

observe_episode_goal() {
  local episode=$1 wave=$2 destination=$3 incident_prefix=$4
  local directory raw_history history raw_canary canary staged historical
  directory=$(dirname "$destination")
  raw_history=$(mktemp "$directory/.$episode-goal-$wave-history-raw.XXXXXX") || return 1
  history=$(mktemp "$directory/.$episode-goal-$wave-history.XXXXXX") || {
    rm -f -- "$raw_history"
    return 1
  }
  raw_canary=$(mktemp "$directory/.$episode-goal-$wave-canary-raw.XXXXXX") || {
    rm -f -- "$raw_history" "$history"
    return 1
  }
  canary=$(mktemp "$directory/.$episode-goal-$wave-canary.XXXXXX") || {
    rm -f -- "$raw_history" "$history" "$raw_canary"
    return 1
  }
  staged=$(mktemp "$directory/.$episode-goal-$wave.XXXXXX") || {
    rm -f -- "$raw_history" "$history" "$raw_canary" "$canary"
    return 1
  }

  if ! compose --profile tools run --rm --no-deps data-tool \
      status "$incident_prefix" >"$raw_history" ||
      ! jq -e '
        def exact_keys($value; $names):
          ($value | type) == "object" and (($value | keys | sort) == ($names | sort));
        def bounded_integer:
          type == "number" and floor == . and . >= 0 and . <= 1000000;
        select(exact_keys(.; ["role","result"]) and .role == "data" and
          exact_keys(.result; ["charges","active_charges","voided_charges",
            "unique_businesses","duplicate_businesses"]) and
          ([.result.charges,.result.active_charges,.result.voided_charges,
            .result.unique_businesses,.result.duplicate_businesses] |
            all(.[]; bounded_integer)) and
          .result.active_charges + .result.voided_charges == .result.charges and
          .result.unique_businesses <= .result.charges and
          .result.duplicate_businesses <= .result.unique_businesses) |
        {observed:.result,historical_satisfied:(.result ==
          {charges:8,active_charges:4,voided_charges:4,
           unique_businesses:4,duplicate_businesses:0})}
      ' "$raw_history" >"$history"; then
    rm -f -- "$raw_history" "$history" "$raw_canary" "$canary" "$staged"
    return 1
  fi
  historical=$(jq -r '.historical_satisfied' "$history") || {
    rm -f -- "$raw_history" "$history" "$raw_canary" "$canary" "$staged"
    return 1
  }
  case "$historical" in true|false) ;; *)
    rm -f -- "$raw_history" "$history" "$raw_canary" "$canary" "$staged"
    return 1
  esac

  if test "$historical" = true; then
    if ! compose --profile tools run --rm --no-deps lead-tool probe >"$raw_canary" ||
        ! jq -e --argjson canary_count_limit "$monitor_probe_charge_limit" '
          def exact_keys($value; $names):
            ($value | type) == "object" and (($value | keys | sort) == ($names | sort));
          def bounded_integer($maximum):
            type == "number" and floor == . and . >= 0 and . <= $maximum;
          def valid_ledger($value):
            exact_keys($value; ["charges","active_charges","voided_charges",
              "unique_businesses","duplicate_businesses"]) and
            ([$value.charges,$value.active_charges,$value.voided_charges,
              $value.unique_businesses,$value.duplicate_businesses] |
              all(.[]; bounded_integer($canary_count_limit))) and
            $value.active_charges + $value.voided_charges == $value.charges and
            $value.unique_businesses <= $value.charges and
            $value.duplicate_businesses <= $value.unique_businesses;
          select(exact_keys(.; ["role","result"]) and .role == "lead" and
            exact_keys(.result; ["receipt","observed","ledger"]) and
            exact_keys(.result.receipt;
              ["request_id","business_id","capture_id","route","status"]) and
            (.result.receipt.request_id | bounded_integer(1000000)) and
            .result.receipt.request_id > 0 and
            (.result.receipt.business_id | type) == "string" and
            (.result.receipt.business_id | length) >= 1 and
            (.result.receipt.business_id | length) <= 128 and
            (.result.receipt.capture_id | bounded_integer(1000000)) and
            (.result.receipt.route == "east" or .result.receipt.route == "west") and
            (.result.receipt.status == "succeeded" or .result.receipt.status == "failed") and
            ((.result.receipt.status == "succeeded" and .result.receipt.capture_id > 0) or
             (.result.receipt.status == "failed" and .result.receipt.capture_id == 0)) and
            valid_ledger(.result.observed) and valid_ledger(.result.ledger)) |
          {receipt_status:.result.receipt.status,
           capture_id_present:(.result.receipt.capture_id > 0),
           observed:.result.observed,settled:.result.ledger}
        ' "$raw_canary" >"$canary"; then
      rm -f -- "$raw_history" "$history" "$raw_canary" "$canary" "$staged"
      return 1
    fi
  else
    printf 'null\n' >"$canary"
  fi

  if ! jq -n --arg episode "$episode" --slurpfile history "$history" \
      --slurpfile canary "$canary" '
        $history[0] as $history |
        $canary[0] as $canary |
        ($history.historical_satisfied and $canary != null and
          $canary.receipt_status == "succeeded" and $canary.capture_id_present and
          $canary.observed == {charges:1,active_charges:1,voided_charges:0,
            unique_businesses:1,duplicate_businesses:0} and
          $canary.settled == {charges:1,active_charges:1,voided_charges:0,
            unique_businesses:1,duplicate_businesses:0}) as $satisfied |
        {schema:"memory.r7.domain-ops.goal",version:2,episode:$episode,
         satisfied:$satisfied,observed:$history.observed,canary:$canary}
      ' >"$staged" ||
      ! validate_episode_goal "$episode" "$staged" >/dev/null; then
    rm -f -- "$raw_history" "$history" "$raw_canary" "$canary" "$staged"
    return 1
  fi
  rm -f -- "$raw_history" "$history" "$raw_canary" "$canary"
  mv "$staged" "$destination"
}

write_attention_boundary() {
  local destination=$1 episode=$2 status=$3 used=$4 waves=$5 snapshot=$6 goal=$7
  local goal_json=null directory temporary
  if test -n "$goal" && test -s "$goal"; then
    goal_json=$(cat "$goal") || return 1
  fi
  directory=$(dirname "$destination")
  mkdir -p "$directory" || return 1
  temporary=$(mktemp "$directory/.attention-boundary.XXXXXX") || return 1
  if ! jq -n --arg episode "$episode" --arg status "$status" \
    --argjson limit "$open_attention_turn_limit" --argjson used "$used" \
    --argjson waves "$(jq -s '.' "$waves")" --argjson final "$(cat "$snapshot")" \
    --argjson goal "$goal_json" '
      {episode:$episode,status:$status,turn_limit:$limit,turns_used:$used,
       waves:$waves,goal:$goal,final_nodes:$final}
    ' >"$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  chmod 0600 "$temporary" || { rm -f -- "$temporary"; return 1; }
  mv "$temporary" "$destination"
}

drive_attention_until_outcome() {
  local episode=$1 goal_probe=$2
  shift 2
  local wave=1 used=0 snapshot goal satisfied unclaimed occupied targets
  local directory="$runtime_root/open-attention"
  local waves="$directory/$episode-waves.jsonl"
  local settlement="$directory/$episode-settlement.json"
  mkdir -p "$directory"
  : >"$waves"
  while :; do
    snapshot=$(snapshot_open_attention "$episode" "$wave") || {
      fail "$episode could not inspect protocol-neutral open attention"
      return 1
    }
    occupied=$(jq '[.[].occupied_claims] | add' "$snapshot")
    if test "$occupied" -ne 0; then
      write_attention_boundary "$directory/$episode-claim-occupied.json" "$episode" \
        claim_occupied "$used" "$waves" "$snapshot" ""
      failure_stage="scenario.$episode.attention-claim-occupied"
      fail "$episode open-attention snapshot found $occupied occupied claims"
      return 1
    fi
    goal="$directory/$episode-goal-$wave.json"
    if ! "$goal_probe" "$episode" "$wave" "$goal" "$@"; then
      failure_stage="scenario.$episode.goal-probe-invalid"
      fail "$episode bounded goal observation did not return closed evidence"
      return 1
    fi
    satisfied=$(validate_episode_goal "$episode" "$goal") || {
      failure_stage="scenario.$episode.goal-probe-invalid"
      fail "$episode bounded goal observation is invalid"
      return 1
    }
    case "$satisfied" in true|false) ;; *)
      failure_stage="scenario.$episode.goal-probe-invalid"
      fail "$episode bounded goal observation omitted a closed result"
      return 1
    esac
    if test "$satisfied" = true; then
      write_attention_boundary "$settlement" "$episode" outcome_observed "$used" \
        "$waves" "$snapshot" "$goal"
      return 0
    fi
    unclaimed=$(jq '[.[].open_unclaimed] | add' "$snapshot")
    if test "$unclaimed" -eq 0; then
      write_attention_boundary "$directory/$episode-quiescent-without-outcome.json" \
        "$episode" quiescent_without_outcome "$used" "$waves" "$snapshot" "$goal"
      failure_stage="scenario.$episode.attention-quiescent-without-outcome"
      fail "$episode has no eligible attention but its external goal is unsatisfied"
      return 1
    fi
    targets=$(jq '[.[] | select(.open_unclaimed > 0)] | length' "$snapshot")
    if test $((used + targets)) -gt "$open_attention_turn_limit"; then
      write_attention_boundary \
        "$directory/$episode-budget-exhausted-before-outcome.json" "$episode" \
        budget_exhausted_before_outcome "$used" "$waves" "$snapshot" "$goal"
      failure_stage="scenario.$episode.attention-budget-exhausted-before-outcome"
      fail "$episode exhausted its ${open_attention_turn_limit}-turn attention budget before its external goal"
      return 1
    fi
    jq -cn --argjson wave "$wave" --argjson nodes "$(cat "$snapshot")" \
      '{wave:$wave,nodes:$nodes}' >>"$waves"
    run_open_attention_wave "$episode" "$wave" "$snapshot" || return 1
    used=$((used + targets))
    wave=$((wave + 1))
  done
}

wait_for_peer_delivery_quiescence() {
  local phase=$1
  local started=$SECONDS deadline=$((SECONDS + peer_quiescence_seconds))
  local attempt=0 occupancy
  mkdir -p "$runtime_root/quiescence"
  while test "$SECONDS" -le "$deadline"; do
    attempt=$((attempt + 1))
    occupancy=$(snapshot_peer_delivery_occupancy "$attempt") ||
      fail 'could not inspect protocol-neutral peer delivery occupancy'
    if test "$occupancy" = 0; then
      jq -s --arg phase "$phase" --argjson attempts "$attempt" \
        --argjson elapsed "$((SECONDS - started))" '
        {
          phase:$phase,
          status:"quiescent",
          attempts:$attempts,
          elapsed_seconds:$elapsed,
          pending_delivery_records:([.[] | .pending_outbox + .staged_inbox] | add),
          nodes:.
        }
      ' "$runtime_root/quiescence/counts.jsonl" \
        >>"$runtime_root/peer-quiescence.jsonl"
      return 0
    fi
    test "$SECONDS" -lt "$deadline" || break
    sleep 0.25
  done
  fail "peer delivery did not quiesce after $phase within ${peer_quiescence_seconds}s; pending=$occupancy"
}

assert_receipts() {
  local report=$1 charges=$2 expected_per_business=$3 expected_voids=$4 label=$5
  jq -e --slurpfile report "$report" --argjson expected "$expected_per_business" \
    --argjson voids "$expected_voids" '
      . as $charges |
      .role == "data" and
      (.result | length) == (($report[0].receipts | length) * $expected) and
      all($report[0].receipts[];
        . as $receipt |
        ([$charges.result[] | select(.business_id == $receipt.business_id)] | length) == $expected and
        ([$charges.result[] | select(
          .business_id == $receipt.business_id and .sequence == $receipt.capture_id and
          .state == "active")] | length) == 1 and
        ([$charges.result[] | select(
          .business_id == $receipt.business_id and .state == "voided")] | length) == $voids)
    ' "$charges" >/dev/null || fail "$label receipt integrity oracle failed"
}

revalidate_episode_state() {
  local episode=$1 incident_prefix=$2 evaluation_prefix=$3 stability_prefix=$4
  local suffix=post-attention

  assert_synthetic_probes "$episode"
  compose --profile tools run --rm --no-deps data-tool status "$incident_prefix" \
    >"$runtime_root/$episode-incident-after-$suffix.json"
  compose --profile tools run --rm --no-deps data-tool \
    read "/charges?prefix=$incident_prefix" \
    >"$runtime_root/$episode-incident-charges-$suffix.json"
  compose --profile tools run --rm --no-deps data-tool \
    read "/charges?prefix=$evaluation_prefix" \
    >"$runtime_root/$episode-recovery-charges-$suffix.json"
  compose --profile tools run --rm --no-deps data-tool \
    read "/charges?prefix=$stability_prefix" \
    >"$runtime_root/$episode-stability-charges-$suffix.json"

  jq -e '
    .role == "data" and
    .result.charges == 8 and .result.active_charges == 4 and
    .result.voided_charges == 4 and .result.unique_businesses == 4 and
    .result.duplicate_businesses == 0
  ' "$runtime_root/$episode-incident-after-$suffix.json" >/dev/null ||
    fail "$episode changed after its external outcome was accepted"
  assert_receipts "$runtime_root/$episode-baseline.json" \
    "$runtime_root/$episode-incident-charges-$suffix.json" 2 1 \
    "$episode post-attention historical"
  assert_receipts "$runtime_root/$episode-recovery.json" \
    "$runtime_root/$episode-recovery-charges-$suffix.json" 1 0 \
    "$episode post-attention recovery"
  assert_receipts "$runtime_root/$episode-stability.json" \
    "$runtime_root/$episode-stability-charges-$suffix.json" 1 0 \
    "$episode post-attention stability"

  mv "$runtime_root/$episode-incident-after-$suffix.json" \
    "$runtime_root/$episode-incident-after.json"
  mv "$runtime_root/$episode-incident-charges-$suffix.json" \
    "$runtime_root/$episode-incident-charges.json"
  mv "$runtime_root/$episode-recovery-charges-$suffix.json" \
    "$runtime_root/$episode-recovery-charges.json"
  mv "$runtime_root/$episode-stability-charges-$suffix.json" \
    "$runtime_root/$episode-stability-charges.json"
}

assert_fresh_batch() {
  local report=$1 count=$2 label=$3 observed
  if jq -e --argjson count "$count" '
    .sent == $count and .accepted == $count and .failed == 0 and
    (.receipts | length) == $count and all(.receipts[]; .capture_id > 0) and
    .observed.ledger.charges == $count and
    .observed.ledger.active_charges == $count and
    .observed.ledger.voided_charges == 0 and
    .observed.ledger.unique_businesses == $count and
    .observed.ledger.duplicate_businesses == 0
  ' "$report" >/dev/null; then
    return 0
  fi
  observed=$(jq -c '{sent,accepted,failed,receipt_count:(.receipts | length),observed}' \
    "$report")
  fail "$label fresh-traffic oracle failed; observed=$observed"
}

assert_synthetic_probes() {
  local episode=$1 history="$runtime_root/$1-synthetic-history.json"
  local charges="$runtime_root/$1-synthetic-charges.json"
  local audit="$runtime_root/$1-synthetic-probes.json"

  compose --profile tools run --rm --no-deps edge-tool \
    read '/history?prefix=synthetic-' >"$history"
  compose --profile tools run --rm --no-deps data-tool \
    read '/charges?prefix=synthetic-' >"$charges"
  jq -e --slurpfile charges "$charges" \
    --argjson history_limit "$gateway_history_limit" \
    --argjson probe_limit "$monitor_probe_limit" \
    --argjson per_probe_charge_limit "$monitor_probe_charge_limit" \
    --argjson charge_limit "$synthetic_charge_limit" '
    .role == "edge" and .result.limit == $history_limit and
    (.result.entries | type == "array" and length <= $probe_limit) and
    ($charges | length == 1 and $charges[0].role == "data" and
      ($charges[0].result | type == "array" and length <= $charge_limit)) and
    (.result.entries as $receipts | $charges[0].result as $records |
      ([ $receipts[].business_id ] | unique | length) == ($receipts | length) and
      all($receipts[];
        (.business_id | startswith("synthetic-")) and .request_id > 0 and
        (.route == "east" or .route == "west") and
        ((.status == "succeeded" and .capture_id > 0) or
         (.status == "failed" and .capture_id == 0))) and
      all($records[];
        . as $record |
        ($record.business_id | startswith("synthetic-")) and
        any($receipts[]; .business_id == $record.business_id)) and
      all($receipts[];
        . as $receipt |
        [ $records[] | select(.business_id == $receipt.business_id) ] as $related |
        ($related | length) <= $per_probe_charge_limit and
        if $receipt.status == "succeeded" then
          ([ $related[] | select(.state == "active") ] | length) == 1 and
          any($related[]; .state == "active" and .sequence == $receipt.capture_id) and
          all($related[];
            (.sequence == $receipt.capture_id and .state == "active") or
            (.sequence != $receipt.capture_id and .state == "voided"))
        else
          all($related[]; .state == "voided")
        end))
  ' "$history" >/dev/null ||
    fail "$episode synthetic checkout side effects are not reconciled"

  jq -n --argjson history "$(cat "$history")" \
    --argjson charges "$(cat "$charges")" '
      $history.result.entries as $receipts |
      $charges.result as $records |
      {
        observed:($receipts | length),
        succeeded:([$receipts[] | select(.status == "succeeded")] | length),
        failed:([$receipts[] | select(.status == "failed")] | length),
        ledger:{
          charges:($records | length),
          active_charges:([$records[] | select(.state == "active")] | length),
          voided_charges:([$records[] | select(.state == "voided")] | length),
          unique_businesses:([$records[].business_id] | unique | length),
          duplicate_businesses:([$records | group_by(.business_id)[] |
            select([.[] | select(.state == "active")] | length > 1)] | length)
        }
      }
    ' >"$audit"
}

assert_recovery() {
  local episode=$1 incident_prefix=$2 evaluation_prefix=$3 stability_prefix=$4
  failure_stage="scenario.$episode.synthetic-probes"
  assert_synthetic_probes "$episode"
  failure_stage="scenario.$episode.recovery-load"
  run_load "$evaluation_prefix" "$recovery_load_count" "$runtime_root/$episode-recovery.json"
  failure_stage="scenario.$episode.stability-load"
  run_load "$stability_prefix" "$stability_load_count" "$runtime_root/$episode-stability.json"
  failure_stage="scenario.$episode.world-observation"
  compose --profile tools run --rm --no-deps data-tool status "$incident_prefix" \
    >"$runtime_root/$episode-incident-after.json"
  compose --profile tools run --rm --no-deps data-tool \
    read "/charges?prefix=$incident_prefix" >"$runtime_root/$episode-incident-charges.json"
  compose --profile tools run --rm --no-deps data-tool \
    read "/charges?prefix=$evaluation_prefix" >"$runtime_root/$episode-recovery-charges.json"
  compose --profile tools run --rm --no-deps data-tool \
    read "/charges?prefix=$stability_prefix" >"$runtime_root/$episode-stability-charges.json"

  failure_stage="scenario.$episode.recovery-fresh"
  assert_fresh_batch "$runtime_root/$episode-recovery.json" \
    "$recovery_load_count" "$episode recovery"
  failure_stage="scenario.$episode.stability-fresh"
  assert_fresh_batch "$runtime_root/$episode-stability.json" \
    "$stability_load_count" "$episode stability"
  failure_stage="scenario.$episode.historical-reconciliation"
  if ! jq -e --argjson count "$baseline_load_count" '
    .role == "data" and
    .result.charges == ($count * 2) and
    .result.active_charges == $count and
    .result.voided_charges == $count and
    .result.unique_businesses == $count and
    .result.duplicate_businesses == 0
  ' "$runtime_root/$episode-incident-after.json" >/dev/null; then
    local observed
    observed=$(jq -c '.result // {}' "$runtime_root/$episode-incident-after.json")
    fail "$episode independent existing-data reconciliation oracle failed; observed=$observed"
  fi
  failure_stage="scenario.$episode.historical-receipts"
  assert_receipts "$runtime_root/$episode-baseline.json" \
    "$runtime_root/$episode-incident-charges.json" 2 1 "$episode historical"
  failure_stage="scenario.$episode.recovery-receipts"
  assert_receipts "$runtime_root/$episode-recovery.json" \
    "$runtime_root/$episode-recovery-charges.json" 1 0 "$episode recovery"
  failure_stage="scenario.$episode.stability-receipts"
  assert_receipts "$runtime_root/$episode-stability.json" \
    "$runtime_root/$episode-stability-charges.json" 1 0 "$episode stability"
}

capture_consolidation_start() {
  local staging="$runtime_root/evolution-consolidation-state"
  local role container database sequence failed=0
  rm -rf -- "$staging" "$runtime_root/evolution-consolidation-start"
  mkdir -p "$staging" "$runtime_root/evolution-consolidation-start"

  pause_agent_containers || fail 'could not pause nodes before result consolidation'
  for role in $roles; do
    container=$(container_for "$role")
    mkdir -p "$staging/$role"
    if ! docker cp "$container:/workspace/.memory/agency/." \
        "$staging/$role" >/dev/null; then
      failed=1
      break
    fi
  done
  unpause_agent_containers || failed=1
  test "$failed" = 0 || fail 'could not capture the pre-consolidation authority boundary'

  for role in $roles; do
    database="$staging/$role/agency.db"
    sequence=$(sqlite3 -readonly -batch -cmd '.timeout 5000' \
      -cmd 'PRAGMA query_only=ON;' "$database" \
      'SELECT COALESCE(MAX(origin_sequence), 0) FROM events;')
    case "$sequence" in ''|*[!0-9]*) fail "$role consolidation sequence is invalid" ;; esac
    jq -n --arg role "$role" --argjson sequence "$sequence" \
      '{role:$role,max_origin_sequence:$sequence}' \
      >"$runtime_root/evolution-consolidation-start/$role.json"
  done
  # Keep the stopped databases as independent evidence for the trace oracle.
  # Report-owned sequence numbers are not authority for this boundary.
  chmod -R a-w "$staging"
}

capture_evolution_boundary() {
  local staging="$runtime_root/evolution-boundary-state"
  local role container database start_sequence sequence heads total=0 failed=0
  rm -rf -- "$staging" "$runtime_root/evolution-boundary"
  mkdir -p "$staging" "$runtime_root/evolution-boundary"

  pause_agent_containers || fail 'could not pause nodes at the episode boundary'
  for role in $roles; do
    container=$(container_for "$role")
    mkdir -p "$staging/$role"
    if ! docker cp "$container:/workspace/.memory/agency/." \
        "$staging/$role" >/dev/null; then
      failed=1
      break
    fi
  done
  unpause_agent_containers || failed=1
  test "$failed" = 0 || fail 'could not capture the episode authority boundary'

  for role in $roles; do
    database="$staging/$role/agency.db"
    start_sequence=$(jq -er '.max_origin_sequence' \
      "$runtime_root/evolution-consolidation-start/$role.json")
    sequence=$(sqlite3 -readonly -batch -cmd '.timeout 5000' \
      -cmd 'PRAGMA query_only=ON;' "$database" \
      'SELECT COALESCE(MAX(origin_sequence), 0) FROM events;')
    case "$sequence" in ''|*[!0-9]*) fail "$role episode boundary sequence is invalid" ;; esac
    test "$sequence" -ge "$start_sequence" ||
      fail "$role authority sequence regressed across result consolidation"
    heads=$(sqlite3 -readonly -batch -json -cmd '.timeout 5000' \
      -cmd 'PRAGMA query_only=ON;' "$database" "
      SELECT r.head_event_id AS event_id,
             e.event_digest AS event_digest
      FROM active_references AS r
      JOIN events AS e ON e.event_id = r.head_event_id
      WHERE r.state = 'active'
        AND e.origin_sequence > $start_sequence
      ORDER BY r.head_event_id;")
    test -n "$heads" || heads='[]'
    jq -e 'type == "array" and all(.[];
      (.event_id | type == "string" and length > 0) and
      (.event_digest | type == "string" and length > 0))' <<<"$heads" >/dev/null ||
      fail "$role episode boundary Reference snapshot is invalid"
    total=$((total + $(jq 'length' <<<"$heads")))
    jq -n --arg role "$role" --argjson start "$start_sequence" \
      --argjson sequence "$sequence" --argjson heads "$heads" \
      '{role:$role,consolidation_after_sequence:$start,
        max_origin_sequence:$sequence,active_heads:$heads}' \
      >"$runtime_root/evolution-boundary/$role.json"
  done
  rm -rf -- "$runtime_root/runtime-restart-state"
  mv "$staging" "$runtime_root/runtime-restart-state"
  # Freeze the authority evidence before any restart path reads it. Runtime
  # restoration uses a disposable copy and cannot mutate this boundary.
  chmod -R a-w "$runtime_root/runtime-restart-state"
  jq -s '{nodes:.,active_head_count:([.[].active_heads | length] | add)}' \
    "$runtime_root/evolution-boundary"/*.json >"$runtime_root/evolution-boundary.json"
}

restart_agent_runtimes() {
  local role container snapshot restore
  local restore_root="$runtime_root/runtime-restore-state"
  local state_dir=/workspace/.memory/agency
  rm -rf -- "$restore_root"
  mkdir -p "$restore_root"
  for role in $roles; do
    container=$(container_for "$role")
    docker stop --time 5 "$container" >/dev/null
    docker rm "$container" >/dev/null
  done
  agent_containers=

  for role in $roles; do
    snapshot="$runtime_root/runtime-restart-state/$role"
    test -s "$snapshot/agency.db" || fail "$role restart snapshot lacks authority"
    restore="$restore_root/$role"
    mkdir -p "$restore"
    cp -R "$snapshot/." "$restore"
    chmod -R u+w "$restore"
    rm -f -- "$restore/control.sock"
    start_agent_container "$role"
    container=$(container_for "$role")
    docker exec "$container" sh -c \
      'umask 077; mkdir -p /workspace/.memory/agency'
    tar -C "$restore" -cf - . | docker exec -i "$container" sh -c \
      'umask 077; tar -C /workspace/.memory/agency -xf -'
    assert_agent_boundary "$role"
    docker exec -w /workspace "$container" memory agency setup \
      --runtime pi --project-root /workspace >"$runtime_root/restart-setup-$role.json"
    jq -e '.schema == "memory.setup" and .version == 1 and .status == "ready"' \
      "$runtime_root/restart-setup-$role.json" >/dev/null ||
      fail "$role fresh Runtime setup was not ready"
    docker exec "$container" sh -c \
      'umask 077; mkdir -p /runtime/pi-state /workspace/.memory/live && chmod 700 /runtime/pi-state /workspace/.memory/live'
    docker exec -u 0 "$container" chmod 0711 /runtime
    docker exec -d "$container" sh -c \
      "exec memory agency serve --state-dir $state_dir >/workspace/.memory/live/memoryd.log 2>&1"
  done

  for role in $roles; do
    container=$(container_for "$role")
    local ready=0 attempt=0
    while test "$attempt" -lt 50; do
      if docker exec "$container" test -S "$state_dir/control.sock"; then
        ready=1
        break
      fi
      sleep 0.1
      attempt=$((attempt + 1))
    done
    test "$ready" = 1 || fail "$role fresh Runtime memoryd did not become ready"
  done
  rm -rf -- "$restore_root"
}

assert_evolution() {
  local role database boundary sequence events summary total=0
  test "$authority_captured" = 1 || fail 'evolution oracle requires captured authority state'
  : >"$runtime_root/evolution-effects.jsonl"
  for role in $roles; do
    database="$runtime_root/authority/$role/agency.db"
    boundary="$runtime_root/evolution-boundary/$role.json"
    sequence=$(jq -er '.max_origin_sequence' "$boundary")
    events=$(sqlite3 -readonly -batch -json -cmd '.timeout 5000' \
      -cmd 'PRAGMA query_only=ON;' "$database" "
      SELECT origin_sequence,
             CAST(canonical_json AS TEXT) AS canonical_json
      FROM events
      WHERE origin_sequence > $sequence
      ORDER BY origin_sequence;")
    test -n "$events" || events='[]'
    summary=$(jq -n --arg role "$role" --slurpfile boundary "$boundary" \
      --argjson events "$events" '
      def exact($left; $right):
        $left != null and $left.id == $right.event_id and
        $left.digest == $right.event_digest;
      ($events | map(.canonical_json | fromjson)) as $accepted |
      ($boundary[0].active_heads) as $heads |
      [ $accepted[] as $event |
        $heads[] as $head |
        select(
          any($event.evidence.causation[]?; exact(.; $head)) or
          exact($event.machine.expected_reference.head?; $head)
        ) |
        {event_id:$event.machine.event_id,
         reference_event_id:$head.event_id,reference_digest:$head.event_digest}
      ] | unique_by(.event_id + "\u0000" + .reference_event_id) as $matches |
      {role:$role,boundary_sequence:$boundary[0].max_origin_sequence,
       active_head_count:($heads|length),accepted_reference_uses:($matches|length),
       matches:$matches}')
    jq -e '.accepted_reference_uses >= 0 and (.matches | type == "array")' \
      <<<"$summary" >/dev/null || fail "$role evolution evidence is invalid"
    total=$((total + $(jq '.accepted_reference_uses' <<<"$summary")))
    printf '%s\n' "$summary" >>"$runtime_root/evolution-effects.jsonl"
  done
  printf '%s\n' "$total" >"$runtime_root/evolution-effects.total"
}

stop_and_capture_authority() {
  local role container staging="$runtime_root/authority-capture"
  test "$authority_captured" = 0 || return 0
  rm -rf -- "$staging"
  mkdir -p "$staging"
  for role in $roles; do
    container=$(container_for "$role")
    docker stop --time 5 "$container" >/dev/null
    mkdir -p "$staging/$role"
    # Copy the complete stopped state directory so a committed WAL remains
    # part of the read-only oracle rather than being mistaken for lost state.
    docker cp "$container:/workspace/.memory/agency/." \
      "$staging/$role" >/dev/null
    test -s "$staging/$role/agency.db" || return 1
  done
  rm -rf -- "$runtime_root/authority"
  mv "$staging" "$runtime_root/authority"
  authority_captured=1
}

assert_peer_effect() {
  local role database count total=0
  test "$authority_captured" = 1 || fail 'peer oracle requires captured authority state'
  : >"$runtime_root/peer-effects.jsonl"
  for role in $roles; do
    database="$runtime_root/authority/$role/agency.db"
    count=$(sqlite3 -readonly -batch -cmd '.timeout 5000' -cmd 'PRAGMA query_only=ON;' "$database" \
      'SELECT COUNT(*) FROM peer_inbox WHERE state = '\''settled'\'' AND local_event_id IS NOT NULL;')
    case "$count" in ''|*[!0-9]*) fail "$role peer-effect oracle returned invalid data" ;; esac
    total=$((total + count))
    jq -cn --arg role "$role" --argjson accepted "$count" \
      '{role:$role,accepted_peer_effects:$accepted}' >>"$runtime_root/peer-effects.jsonl"
  done
  test "$total" -ge 1 ||
    fail 'no authenticated cross-peer Event became an accepted local effect'
  printf '%s\n' "$total" >"$runtime_root/peer-effects.total"
}

episode_report_json() {
  local episode=$1
  jq -n \
    --arg id "$episode" \
    --argjson baseline "$(cat "$runtime_root/$episode-baseline.json")" \
    --argjson synthetic_probes "$(cat "$runtime_root/$episode-synthetic-probes.json")" \
    --argjson recovery "$(cat "$runtime_root/$episode-recovery.json")" \
    --argjson stability "$(cat "$runtime_root/$episode-stability.json")" \
    --argjson incident_after "$(cat "$runtime_root/$episode-incident-after.json")" \
    --argjson incident_charges "$(cat "$runtime_root/$episode-incident-charges.json")" \
    --argjson recovery_charges "$(cat "$runtime_root/$episode-recovery-charges.json")" \
    --argjson stability_charges "$(cat "$runtime_root/$episode-stability-charges.json")" '
      {id:$id,baseline:$baseline,synthetic_probes:$synthetic_probes,
       recovery:$recovery,stability:$stability,
       incident_after:$incident_after,incident_charges:$incident_charges,
       recovery_charges:$recovery_charges,stability_charges:$stability_charges}
    '
}

collect_failure_world() {
  local destination=$1 episode source staging
  staging=$destination.jsonl
  : >"$staging"
  for episode in episode-1 episode-2; do
    source="$runtime_root/$episode-incident-after.json"
    test -s "$source" || continue
    jq -ce --arg episode "$episode" '
      select(.role == "data" and (.result | type) == "object") |
      .result as $value |
      [$value.charges,$value.active_charges,$value.voided_charges,
       $value.unique_businesses,$value.duplicate_businesses] as $counts |
      select(all($counts[]; type == "number" and floor == . and . >= 0 and . <= 1000000)) |
      select($value.active_charges + $value.voided_charges == $value.charges) |
      select($value.unique_businesses <= $value.charges) |
      select($value.duplicate_businesses <= $value.unique_businesses) |
      {episode:$episode,charges:$value.charges,active_charges:$value.active_charges,
       voided_charges:$value.voided_charges,unique_businesses:$value.unique_businesses,
       duplicate_businesses:$value.duplicate_businesses}
    ' "$source" >>"$staging" || return 1
  done
  jq -s 'select(length <= 2)' "$staging" >"$destination" || return 1
  test -s "$destination"
}

write_report() {
  local temporary total episodes evolution_total evolution_demonstrated
  temporary="$runtime_root/report.json"
  total=$(cat "$runtime_root/peer-effects.total")
  evolution_total=$(cat "$runtime_root/evolution-effects.total")
  evolution_demonstrated=false
  if test "$(jq -r '.active_head_count' "$runtime_root/evolution-boundary.json")" -gt 0 &&
      test "$evolution_total" -gt 0; then
    evolution_demonstrated=true
  fi
  episodes=$(jq -n --argjson first "$(episode_report_json episode-1)" \
    --argjson second "$(episode_report_json episode-2)" '[$first,$second]')
  run_finished_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  jq -n \
    --arg schema 'memory.r7.domain-ops.live-report' \
    --arg model "$pi_model" \
    --arg thinking "$pi_thinking" \
    --arg run_id "$project" \
    --arg started_at "$run_started_at" \
    --arg finished_at "$run_finished_at" \
    --arg candidate_digest "$agent_image_id" \
    --argjson episodes "$episodes" \
    --argjson turns "$(jq -s 'sort_by(.turn)' "$runtime_root/sanitized"/*.json)" \
    --argjson delivery_quiescence "$(jq -s '.' "$runtime_root/peer-quiescence.jsonl")" \
    --argjson open_attention "$(jq -s '.' "$runtime_root/open-attention"/*-settlement.json)" \
    --argjson peer_effects "$(jq -s '.' "$runtime_root/peer-effects.jsonl")" \
    --argjson evolution_boundary "$(cat "$runtime_root/evolution-boundary.json")" \
    --argjson evolution_effects "$(jq -s '.' "$runtime_root/evolution-effects.jsonl")" \
    --argjson evolution_total "$evolution_total" \
    --argjson evolution_demonstrated "$evolution_demonstrated" \
    --argjson peer_effect_total "$total" '
      {
        schema:$schema,
        version:7,
        status:"passed",
        model:$model,
        thinking:$thinking,
        run:{id:$run_id,started_at:$started_at,finished_at:$finished_at,
          candidate_digest:$candidate_digest},
        isolation:{passed:true,fresh_runtime_between_episodes:true},
        world:{episodes:$episodes},
        protocol:{accepted_peer_effects:$peer_effect_total,by_receiver:$peer_effects,
          delivery_quiescence:$delivery_quiescence,
          attention_envelopes:$open_attention,
          evolution:{boundary:$evolution_boundary,effects:$evolution_effects,
            accepted_reference_uses:$evolution_total,
            demonstrated:$evolution_demonstrated}},
        turns:$turns,
        raw_provider_streams_retained:false
      }
  ' >"$temporary"
  chmod 0600 "$temporary"
}

write_trace() {
  (
    cd "$repository_root"
    go run ./test/memoryd/domainops/trace \
      --report "$runtime_root/report.json" \
      --authority "$runtime_root/authority" \
      --consolidation-authority "$runtime_root/evolution-consolidation-state" \
      --boundary-authority "$runtime_root/runtime-restart-state" \
      --scenario-root "$repository_root" \
      --candidate-binaries "$runtime_root/candidate-binaries.sha256" \
      --output "$runtime_root/report.trace"
  )
}

publish_evidence_file() {
  local source=$1 target=$2 directory temporary
  directory=$(dirname "$target")
  mkdir -p "$directory"
  temporary=$(mktemp "$directory/.r7-domain-evidence.XXXXXX")
  cp "$source" "$temporary"
  chmod 0600 "$temporary"
  mv "$temporary" "$target"
}

publish_evidence() {
  # A failed run never touches the last PASS pair. On success, trace is
  # published first so a fresh passed report can never point at an older trace.
  publish_evidence_file "$runtime_root/report.trace" "$trace_path"
  publish_evidence_file "$runtime_root/report.json" "$report_path"
}

finalize_failure_evidence() {
  local code=$1 observed_at completed_turns='[]' attention_envelope='null' candidate
  local attention_files=()
  test "$authority_started" = 1 || return 0
  test -n "$runtime_root" && test -d "$runtime_root" || return 0
  test -s "$runtime_root/candidate-binaries.sha256" || return 0
  stop_and_capture_authority || return 0

  observed_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  if compgen -G "$runtime_root/sanitized/*.json" >/dev/null; then
    completed_turns=$(jq -s 'sort_by(.turn)' "$runtime_root/sanitized"/*.json) || return 0
  fi
  for candidate in "$runtime_root"/open-attention/*-budget-exhausted-before-outcome.json \
      "$runtime_root"/open-attention/*-quiescent-without-outcome.json \
      "$runtime_root"/open-attention/*-claim-occupied.json; do
    test -f "$candidate" || continue
    attention_files+=("$candidate")
  done
  if test "${#attention_files[@]}" -gt 0; then
    test "${#attention_files[@]}" -eq 1 || return 0
    attention_envelope=$(cat "${attention_files[0]}") || return 0
  fi
  collect_failure_world "$runtime_root/failure-world.json" || return 0
  jq -n \
    --arg schema 'memory.r7.domain-ops.failure-report' \
    --arg model "$pi_model" \
    --arg thinking "$pi_thinking" \
    --arg run_id "$project" \
    --arg started_at "$run_started_at" \
    --arg finished_at "$observed_at" \
    --arg candidate_digest "$agent_image_id" \
    --arg code "$code" \
    --arg observed_at "$observed_at" \
    --argjson attention_envelope "$attention_envelope" \
    --argjson world "$(cat "$runtime_root/failure-world.json")" \
    --argjson turns "$completed_turns" '
      {
        schema:$schema,
        version:7,
        status:"failed",
        model:$model,
        thinking:$thinking,
        run:{id:$run_id,started_at:$started_at,finished_at:$finished_at,
          candidate_digest:$candidate_digest},
        failure:{code:$code,observed_at:$observed_at},
        world:$world,
        attention_envelope:$attention_envelope,
        turns:$turns,
        raw_provider_streams_retained:false
      }
    ' >"$runtime_root/failure-report.json" || return 0
  chmod 0600 "$runtime_root/failure-report.json" || return 0
  (
    cd "$repository_root" || exit 1
    go run ./test/memoryd/domainops/trace \
      --failure-report "$runtime_root/failure-report.json" \
      --authority "$runtime_root/authority" \
      --scenario-root "$repository_root" \
      --candidate-binaries "$runtime_root/candidate-binaries.sha256" \
      --output "$runtime_root/failure-report.trace"
  ) || return 0
  # Trace first: a published failure JSON can never point at a stale trace.
  publish_evidence_file "$runtime_root/failure-report.trace" "$failure_trace_path" || return 0
  publish_evidence_file "$runtime_root/failure-report.json" "$failure_report_path" || return 0
  printf 'failure report: %s\nfailure observer trace: %s\n' \
    "$failure_report_path" "$failure_trace_path" >&2
}

main() {
  require_prerequisites
  rm -f -- "$failure_report_path" "$failure_trace_path"
  runtime_root=$(mktemp -d /tmp/mnr7-domain-live.XXXXXX)
  trap on_exit EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  runtime_root=$(cd "$runtime_root" && pwd -P)
  chmod 0700 "$runtime_root"
  project="mnr7-domain-live-$$"
  control_network="$project-memory-control"
  agent_image="memory-domain-ops-agent:live-$$"
  run_started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  mkdir -p "$runtime_root/cards" "$runtime_root/workspaces" "$runtime_root/raw" \
    "$runtime_root/sanitized" "$runtime_root/authority" "$runtime_root/turn-locks"

  local first_incident_prefix="incident-a-$$"
  local first_evaluation_prefix="evaluation-a-$$"
  local first_stability_prefix="stability-a-$$"
  local second_incident_prefix="incident-b-$$"
  local second_evaluation_prefix="evaluation-b-$$"
  local second_stability_prefix="stability-b-$$"
  failure_stage=runner.world-start
  build_and_start_world
  failure_stage=scenario.episode-1-incident-seed
  seed_incident episode-1 "$first_incident_prefix" east
  failure_stage=runner.authority-start
  prepare_agents
  failure_stage=scenario.episode-1-agent-turns
  run_agents episode-1
  failure_stage=scenario.episode-1-open-attention
  drive_attention_until_outcome episode-1 observe_episode_goal \
    "$first_incident_prefix"
  failure_stage=scenario.episode-1-recovery
  assert_recovery episode-1 "$first_incident_prefix" "$first_evaluation_prefix" \
    "$first_stability_prefix"
  failure_stage=scenario.episode-1-consolidation-start
  capture_consolidation_start
  failure_stage=scenario.episode-1-post-outcome-attention
  run_post_outcome_attention episode-1
  failure_stage=scenario.evolution-boundary
  capture_evolution_boundary
  failure_stage=scenario.episode-1-post-outcome-revalidation
  revalidate_episode_state episode-1 "$first_incident_prefix" \
    "$first_evaluation_prefix" "$first_stability_prefix"
  failure_stage=runner.runtime-restart
  restart_agent_runtimes
  failure_stage=scenario.episode-2-injection
  inject_second_variant
  seed_incident episode-2 "$second_incident_prefix" west
  failure_stage=scenario.episode-2-agent-turns
  run_agents episode-2
  failure_stage=scenario.episode-2-open-attention
  drive_attention_until_outcome episode-2 observe_episode_goal \
    "$second_incident_prefix"
  failure_stage=scenario.episode-2-recovery
  assert_recovery episode-2 "$second_incident_prefix" "$second_evaluation_prefix" \
    "$second_stability_prefix"
  failure_stage=runner.authority-capture
  stop_and_capture_authority
  failure_stage=r7.peer-effect
  assert_peer_effect
  failure_stage=scenario.evolution
  assert_evolution
  failure_stage=runner.pass-report
  write_report
  failure_stage=runner.pass-trace
  write_trace
  failure_stage=runner.pass-publish
  publish_evidence
  failure_stage=runner.complete

  printf 'r7 domain ops live: PASS (two real incidents, fresh Pi turns, retained authority, external recovery and evolution oracles)\n'
  printf 'sanitized report: %s\n' "$report_path"
  printf 'observer trace: %s\n' "$trace_path"
}

if test "${BASH_SOURCE[0]}" = "$0"; then
  main "$@"
fi
