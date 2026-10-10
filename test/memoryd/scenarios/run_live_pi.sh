#!/usr/bin/env bash

# Opt-in, real-provider smoke for the Pi attachment boundary. The model's prose
# is never an oracle: Pi's JSON event stream and a fresh public View prove the
# fixed cue, Current, accepted Receipt, and committed Handling effect.

set -euo pipefail

R7_LIVE_RUNNER_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
R7_LIVE_REPOSITORY_ROOT=$(cd "$R7_LIVE_RUNNER_DIR/../../.." && pwd -P)
R7_LIVE_PI_VERSION=0.83.0
R7_LIVE_PI_PACKAGE="@earendil-works/pi-coding-agent@$R7_LIVE_PI_VERSION"
# DeepSeek retired the legacy deepseek-chat alias. Pin the current low-cost,
# tool-capable model so this paid smoke cannot silently select another model.
R7_LIVE_PI_MODEL=deepseek-v4-flash
R7_LIVE_TIMEOUT_SECONDS=${R7_LIVE_TIMEOUT_SECONDS:-120}
R7_LIVE_ROOT=
R7_LIVE_DAEMON_PID=
R7_LIVE_KEY_FIFO=
R7_LIVE_KEY_WRITER_PID=

r7_live_fail() {
  printf 'r7 live pi: %s\n' "$*" >&2
  return 1
}

r7_live_boundary_envelope() {
  local boundary
  boundary=$(dd if=/dev/urandom bs=32 count=1 2>/dev/null | base64 | tr '+/' '-_' | tr -d '=\n')
  test "${#boundary}" = 43 || r7_live_fail 'Host boundary entropy is unavailable'
  printf '{"boundary":"%s","schema":"memory.hook.boundary","version":1}' "$boundary"
}

r7_live_cleanup() {
  local attempt=0
  if test -n "$R7_LIVE_KEY_WRITER_PID" && kill -0 "$R7_LIVE_KEY_WRITER_PID" 2>/dev/null; then
    kill -TERM "$R7_LIVE_KEY_WRITER_PID" 2>/dev/null || true
    wait "$R7_LIVE_KEY_WRITER_PID" 2>/dev/null || true
  fi
  if test -n "$R7_LIVE_DAEMON_PID" && kill -0 "$R7_LIVE_DAEMON_PID" 2>/dev/null; then
    kill -TERM "$R7_LIVE_DAEMON_PID" 2>/dev/null || true
    while test "$attempt" -lt 50; do
      kill -0 "$R7_LIVE_DAEMON_PID" 2>/dev/null || break
      sleep 0.1
      attempt=$((attempt + 1))
    done
    if kill -0 "$R7_LIVE_DAEMON_PID" 2>/dev/null; then
      kill -KILL "$R7_LIVE_DAEMON_PID" 2>/dev/null || true
    fi
    wait "$R7_LIVE_DAEMON_PID" 2>/dev/null || true
  fi
  if test -n "$R7_LIVE_ROOT" && test -d "$R7_LIVE_ROOT" && test ! -L "$R7_LIVE_ROOT"; then
    case "$R7_LIVE_ROOT" in
      /tmp/mnr7-live.??????|/private/tmp/mnr7-live.??????)
        chmod -R u+w "$R7_LIVE_ROOT" 2>/dev/null || true
        rm -rf -- "$R7_LIVE_ROOT"
        ;;
      *) printf 'r7 live pi: refusing to remove unexpected temporary path\n' >&2 ;;
    esac
  fi
}

r7_live_require_prerequisites() {
  local node_major node_minor
  test "${LIVE_PI:-}" = 1 || r7_live_fail 'set LIVE_PI=1 to authorize a paid live request'
  test -n "${DEEPSEEK_API_KEY:-}" || r7_live_fail 'DEEPSEEK_API_KEY is required'
  case "$R7_LIVE_TIMEOUT_SECONDS" in
    ''|*[!0-9]*) r7_live_fail 'R7_LIVE_TIMEOUT_SECONDS must be an integer' ;;
  esac
  test "$R7_LIVE_TIMEOUT_SECONDS" -ge 10 && test "$R7_LIVE_TIMEOUT_SECONDS" -le 300 ||
    r7_live_fail 'R7_LIVE_TIMEOUT_SECONDS must be between 10 and 300'
  command -v go >/dev/null 2>&1 || r7_live_fail 'go is required'
  command -v jq >/dev/null 2>&1 || r7_live_fail 'jq is required'
  command -v node >/dev/null 2>&1 || r7_live_fail 'Node.js is required'
  command -v npm >/dev/null 2>&1 || r7_live_fail 'npm is required'
  node_major=$(node -p 'Number(process.versions.node.split(".")[0])')
  node_minor=$(node -p 'Number(process.versions.node.split(".")[1])')
  if test "$node_major" -lt 22 || { test "$node_major" -eq 22 && test "$node_minor" -lt 19; }; then
    r7_live_fail 'Node.js >= 22.19 is required by the pinned Pi runtime'
  fi
}

r7_live_tail_safe_log() {
  local path=$1
  # Build/setup/install logs run with the provider credential removed. Pi's
  # own output is deliberately never passed to this helper.
  tail -n 80 "$path" >&2 || true
}

r7_live_build_binaries() {
  local go_version build_log=$R7_LIVE_ROOT/build.log
  go_version=$(awk '$1 == "go" { print $2; exit }' "$R7_LIVE_REPOSITORY_ROOT/go.mod")
  test -n "$go_version" || r7_live_fail 'repository Go version is unavailable'
  if ! env -u DEEPSEEK_API_KEY GOTOOLCHAIN="go$go_version" GOFLAGS=-mod=readonly \
    go -C "$R7_LIVE_REPOSITORY_ROOT" build -o "$R7_LIVE_ROOT/bin/memory" \
      . >"$build_log" 2>&1; then
    r7_live_tail_safe_log "$build_log"
    r7_live_fail 'memory build failed'
  fi
}

r7_live_install_pi() {
  local install_log=$R7_LIVE_ROOT/pi-install.log pi_version
  if ! env -u DEEPSEEK_API_KEY npm install --ignore-scripts --no-audit --no-fund \
    --prefix "$R7_LIVE_ROOT/pi" "$R7_LIVE_PI_PACKAGE" >"$install_log" 2>&1; then
    r7_live_tail_safe_log "$install_log"
    r7_live_fail "installing pinned Pi runtime $R7_LIVE_PI_PACKAGE failed"
  fi
  R7_LIVE_PI_BIN=$R7_LIVE_ROOT/pi/node_modules/.bin/pi
  test -x "$R7_LIVE_PI_BIN" || r7_live_fail 'the pinned Pi package did not provide its pi executable'
  pi_version=$(env -u DEEPSEEK_API_KEY PI_CODING_AGENT_DIR="$R7_LIVE_ROOT/pi-state" \
    "$R7_LIVE_PI_BIN" --version 2>/dev/null) || r7_live_fail 'the Pi executable is unusable'
  test "$pi_version" = "$R7_LIVE_PI_VERSION" ||
    r7_live_fail "the Pi executable reported unexpected version $pi_version"
}

r7_live_prepare_pi_auth() {
  local auth=$R7_LIVE_ROOT/pi-state/auth.json
  R7_LIVE_KEY_FIFO=$R7_LIVE_ROOT/deepseek-key.pipe
  mkfifo "$R7_LIVE_KEY_FIFO" || r7_live_fail 'could not create the one-shot credential pipe'
  chmod 0600 "$R7_LIVE_KEY_FIFO"
  if ! jq -cn --arg command "!cat $R7_LIVE_KEY_FIFO" \
    '{deepseek:{type:"api_key",key:$command}}' >"$auth"; then
    r7_live_fail 'could not configure one-shot Pi authentication'
  fi
  chmod 0600 "$auth"
}

r7_live_start_key_writer() {
  (
    # printf is a shell builtin: the credential never appears in an argv or a
    # regular file. Pi consumes it once through auth.json command resolution.
    printf '%s' "$DEEPSEEK_API_KEY" >"$R7_LIVE_KEY_FIFO"
    unset DEEPSEEK_API_KEY
    rm -f -- "$R7_LIVE_KEY_FIFO"
  ) &
  R7_LIVE_KEY_WRITER_PID=$!
}

r7_live_finish_key_writer() {
  local status
  if kill -0 "$R7_LIVE_KEY_WRITER_PID" 2>/dev/null; then
    kill -TERM "$R7_LIVE_KEY_WRITER_PID" 2>/dev/null || true
    wait "$R7_LIVE_KEY_WRITER_PID" 2>/dev/null || true
    R7_LIVE_KEY_WRITER_PID=
    return 1
  fi
  if wait "$R7_LIVE_KEY_WRITER_PID"; then
    status=0
  else
    status=$?
  fi
  R7_LIVE_KEY_WRITER_PID=
  test "$status" -eq 0 && test ! -e "$R7_LIVE_KEY_FIFO"
}

r7_live_start_workspace() {
  local card=$R7_LIVE_ROOT/node-card.json setup=$R7_LIVE_ROOT/setup.json
  mkdir -p "$R7_LIVE_ROOT/workspace"
  R7_LIVE_WORKSPACE=$(cd "$R7_LIVE_ROOT/workspace" && pwd -P)
  R7_LIVE_STATE=$R7_LIVE_WORKSPACE/.memory/agency

  if ! env -u DEEPSEEK_API_KEY "$R7_LIVE_ROOT/bin/memory" agency peer prepare \
    --listen 127.0.0.1:17447 --advertise 127.0.0.1:17447 \
    --project-root "$R7_LIVE_WORKSPACE" >"$card" 2>"$R7_LIVE_ROOT/prepare.err"; then
    r7_live_tail_safe_log "$R7_LIVE_ROOT/prepare.err"
    r7_live_fail 'workspace provisioning failed'
  fi

  env -u DEEPSEEK_API_KEY "$R7_LIVE_ROOT/bin/memory" agency serve --state-dir "$R7_LIVE_STATE" \
    >"$R7_LIVE_ROOT/daemon.out" 2>"$R7_LIVE_ROOT/daemon.err" &
  R7_LIVE_DAEMON_PID=$!

  if ! env -u DEEPSEEK_API_KEY PATH="$R7_LIVE_ROOT/bin:$PATH" \
    "$R7_LIVE_ROOT/bin/memory" agency setup --runtime pi \
      --project-root "$R7_LIVE_WORKSPACE" >"$setup" 2>"$R7_LIVE_ROOT/setup.err"; then
    r7_live_tail_safe_log "$R7_LIVE_ROOT/setup.err"
    r7_live_fail 'Pi projection setup failed'
  fi
  jq -e '.schema == "memory.setup" and .version == 1 and .status == "ready"' \
    "$setup" >/dev/null || r7_live_fail 'setup did not return the exact ready receipt'
  test -f "$R7_LIVE_WORKSPACE/.pi/extensions/memoryd.ts" ||
    r7_live_fail 'the project-local Pi hook was not installed'
  test -f "$R7_LIVE_WORKSPACE/.pi/extensions/memoryd-current.ts" ||
    r7_live_fail 'the project-local Pi Current tool was not installed'
  test -f "$R7_LIVE_WORKSPACE/.pi/skills/memoryd/SKILL.md" ||
    r7_live_fail 'the project-local memoryd guide was not installed'
}

r7_live_with_deadline() {
  local seconds=$1 marker=$R7_LIVE_ROOT/pi.timeout pid watchdog status elapsed
  shift
  "$@" &
  pid=$!
  (
    elapsed=0
    while test "$elapsed" -lt "$seconds"; do
      sleep 1
      kill -0 "$pid" 2>/dev/null || exit 0
      elapsed=$((elapsed + 1))
    done
    : >"$marker"
    kill -TERM "$pid" 2>/dev/null || exit 0
    elapsed=0
    while test "$elapsed" -lt 5; do
      sleep 1
      kill -0 "$pid" 2>/dev/null || exit 0
      elapsed=$((elapsed + 1))
    done
    kill -KILL "$pid" 2>/dev/null || true
  ) &
  watchdog=$!
  if wait "$pid"; then
    status=0
  else
    status=$?
  fi
  kill "$watchdog" 2>/dev/null || true
  wait "$watchdog" 2>/dev/null || true
  test ! -f "$marker" || return 124
  return "$status"
}

r7_live_pi_process() {
  local prompt=$1
  cd "$R7_LIVE_WORKSPACE" || return 1
  exec env -u DEEPSEEK_API_KEY \
    PATH="$R7_LIVE_ROOT/bin:$PATH" \
    PI_CODING_AGENT_DIR="$R7_LIVE_ROOT/pi-state" \
    PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0 \
    "$R7_LIVE_PI_BIN" --mode json --print --no-session --approve --no-context-files \
      --no-prompt-templates --no-themes --provider deepseek --model "$R7_LIVE_PI_MODEL" \
      --thinking off --tools read,memoryd_current,memoryd_submit "$prompt"
}

r7_live_run_pi() {
  local events=$R7_LIVE_ROOT/pi-events.jsonl errors=$R7_LIVE_ROOT/pi.err status
  local prompt
  # This gate is a bounded live protocol smoke, not a scripted tool transcript.
  # The Hook points Pi at the installed guide; Pi must discover the exact Intent
  # shape there and may correct a rejected attempt within the fixed turn bound.
  # Natural multi-Agent scenario evidence is evaluated separately.
  prompt='Use the installed memoryd protocol to persist one local responsibility. Its kind is `live.pi.probe` and its payload is `persist one Pi-originated responsibility`. Keep the responsibility local to this Agent, read the Receipt, and stop. Do not claim that the responsibility is completed.'

  r7_live_start_key_writer
  if r7_live_with_deadline "$R7_LIVE_TIMEOUT_SECONDS" r7_live_pi_process "$prompt" \
      >"$events" 2>"$errors"; then
    status=0
  else
    status=$?
  fi
  if ! r7_live_finish_key_writer; then
    r7_live_fail 'Pi did not consume and remove the one-shot provider credential pipe'
  fi
  if test "$status" -eq 124; then
    r7_live_fail "Pi exceeded the ${R7_LIVE_TIMEOUT_SECONDS}s live bound"
  fi
  if test "$status" -ne 0; then
    if grep -Eqi 'auth|api[ -]?key|unauthori[sz]ed|http 401' "$errors"; then
      r7_live_fail 'DeepSeek rejected the configured credential'
    fi
    if grep -Eqi 'rate.?limit|http 429' "$errors"; then
      r7_live_fail 'DeepSeek rate-limited the live smoke'
    fi
    r7_live_fail "Pi/DeepSeek exited with status $status; raw provider output was withheld"
  fi
  jq -e . "$events" >/dev/null 2>&1 || r7_live_fail 'Pi did not emit a canonical JSON event stream'
}

r7_live_assert_pi_trace() {
  local events=$R7_LIVE_ROOT/pi-events.jsonl cue
  cue='memoryd state is available; read .pi/skills/memoryd/SKILL.md and use its exact Pi tools and artifact commands.'

  jq -s -e --arg cue "$cue" '
    any(.[];
      (.type == "message_start" or .type == "message_end") and
      .message.role == "custom" and .message.customType == "memoryd" and
      .message.content == $cue)
  ' "$events" >/dev/null || r7_live_fail 'Pi did not expose the exact installed memoryd cue'

  jq -s -e '
    ([.[] | select(.type == "tool_execution_start" and .toolName == "read" and
      (.args | type == "object") and (.args.path | type == "string") and
      (.args.path == ".pi/skills/memoryd/SKILL.md" or
       (.args.path | endswith("/.pi/skills/memoryd/SKILL.md"))))] | length) as $reads |
    $reads >= 1
  ' "$events" >/dev/null || r7_live_fail 'Pi did not read the installed bounded memoryd guide'
  jq -s -e '
    ([.[] | select(.type == "tool_execution_start" and
      .toolName == "memoryd_current" and .args == {})] | length) as $currents |
    $currents >= 1
  ' "$events" >/dev/null || r7_live_fail 'Pi did not obtain a native View'
  jq -s -e '
    [.[] | select(.type == "tool_execution_start" and
      .toolName == "memoryd_submit")] as $submits |
    ($submits | length) >= 1 and all($submits[];
      (.args | type == "object" and (keys | sort) == ["intent"]) and
      (.args.intent | type == "object"))
  ' "$events" >/dev/null || r7_live_fail 'Pi did not submit a native Intent'
  jq -s -e '
    def parsed_receipt:
      (.result.content // null) as $content |
      if ($content | type) == "array" and ($content | length) == 1 and
        $content[0].type == "text" and ($content[0].text | type) == "string"
      then (try ($content[0].text | fromjson) catch null)
      else null end;
    [to_entries[] |
      select(.value.type == "tool_execution_end" and
        .value.toolName == "memoryd_submit" and .value.isError == false and
        .value.result.details == {schema:"memory.pi.effect",version:1,status:"settled"}) |
      .key as $index | (.value | parsed_receipt) as $receipt |
      select(($receipt | type) == "object" and
        ($receipt | keys | sort) == ["outcome","replayed","schema","version"] and
        $receipt.schema == "memory.agent.receipt" and $receipt.version == 1 and
        $receipt.outcome == "accepted" and $receipt.replayed == false) |
      {index:$index,receipt:$receipt}
    ] as $accepted |
    ($accepted | length) == 1 and
    all(.[($accepted[0].index + 1):][]; .type != "tool_execution_start")
  ' "$events" >/dev/null ||
    r7_live_fail 'Pi did not settle exactly one accepted Receipt and stop protocol actions'
  jq -s -e '
    all(.[] | select(.type == "tool_execution_start");
      .toolName == "read" or .toolName == "memoryd_current" or
      .toolName == "memoryd_submit")
  ' "$events" >/dev/null || r7_live_fail 'Pi used a tool outside the guide and native protocol surfaces'
  jq -s -e '
    ([.[] | select(.type == "tool_execution_start")] | length) <= 16
  ' "$events" >/dev/null || r7_live_fail 'Pi exceeded the bounded live attention budget'
  jq -s -e 'any(.[]; .type == "agent_end")' "$events" >/dev/null ||
    r7_live_fail 'Pi did not finish its bounded Agent turn'
}

r7_live_assert_committed_effect() {
  local hook=$R7_LIVE_ROOT/verify-hook.json view=$R7_LIVE_ROOT/verify-view.json
  if ! r7_live_boundary_envelope | (
    cd "$R7_LIVE_WORKSPACE" &&
      env -u DEEPSEEK_API_KEY PATH="$R7_LIVE_ROOT/bin:$PATH" \
        "$R7_LIVE_ROOT/bin/memory" agency hook attach --json
  ) >"$hook" 2>/dev/null; then
    r7_live_fail 'a fresh attachment could not inspect the post-Pi authority state'
  fi
  if ! (
    cd "$R7_LIVE_WORKSPACE" &&
      env -u DEEPSEEK_API_KEY PATH="$R7_LIVE_ROOT/bin:$PATH" \
        "$R7_LIVE_ROOT/bin/memory" agency agent current --json
  ) >"$view" 2>/dev/null; then
    r7_live_fail 'a fresh attachment could not obtain the post-Pi View'
  fi
  jq -e '
    .schema == "memory.agent.view" and .version == 8 and
    .current.semantic.kind == "live.pi.probe" and
    .current.semantic.payload == "persist one Pi-originated responsibility" and
    (.current.facts.handle | type == "string" and length > 0)
  ' "$view" >/dev/null ||
    r7_live_fail 'the accepted Pi Receipt did not leave the exact durable Handling effect'
}

r7_live_require_prerequisites
# Keep the Unix control socket below platform path limits (notably Darwin's
# 104-byte sockaddr_un). The physicalized /tmp path is also the only tree this
# script ever removes.
R7_LIVE_ROOT=$(mktemp -d /tmp/mnr7-live.XXXXXX)
R7_LIVE_ROOT=$(cd "$R7_LIVE_ROOT" && pwd -P)
trap r7_live_cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$R7_LIVE_ROOT/bin" "$R7_LIVE_ROOT/pi-state"

r7_live_build_binaries
r7_live_install_pi
r7_live_start_workspace
r7_live_prepare_pi_auth
r7_live_run_pi
r7_live_assert_pi_trace
r7_live_assert_committed_effect

printf 'r7 live pi: PASS (deterministic real-provider protocol smoke; hook, View, Intent, Receipt, durable effect)\n'
