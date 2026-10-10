#!/usr/bin/env bash

set -euo pipefail

runtime_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
runner_dir=$(cd "$runtime_dir/../../domainops" && pwd -P)
repository_root=$(cd "$runtime_dir/../../../.." && pwd -P)
lifecycle_extension="$repository_root/internal/agency/attach/assets/pi/memoryd.ts"
current_extension="$repository_root/internal/agency/attach/assets/pi/memoryd-current.ts"
current_provider="$runtime_dir/current-rpc-provider.ts"
image="memory-pi-delegate-oracle:$$"
scratch=$(mktemp -d /tmp/memory-pi-runtime-oracle.XXXXXX)

cleanup() {
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf "$scratch"
}
trap cleanup EXIT HUP INT TERM

command -v docker >/dev/null 2>&1 || {
  printf 'pi delegate oracle: docker is required\n' >&2
  exit 1
}
docker info >/dev/null 2>&1 || {
  printf 'pi delegate oracle: Docker Engine is unavailable\n' >&2
  exit 1
}

docker build --quiet --target agent -f "$runner_dir/Dockerfile" \
  -t "$image" "$repository_root" >/dev/null
mkdir -p "$scratch/bin"
install -m 0755 "$runtime_dir/current-rpc-memoryd.sh" "$scratch/bin/memory"
smoke=$(printf '%s\n' '{"id":"state","type":"get_state"}' |
  docker run --rm -i --entrypoint pi "$image" \
    --mode rpc --no-session --no-extensions \
    -e /opt/memory/pi-delegate/delegate.ts \
    --no-skills --no-prompt-templates --no-themes --no-context-files \
    --no-tools --no-approve)
printf '%s\n' "$smoke" | grep -Eq \
  '"id":"state".*"command":"get_state".*"success":true' || {
  printf 'pi delegate oracle: Pi rejected the extension surface\n' >&2
  exit 1
}
run_current_rpc() {
  local mode=$1
  docker run --rm --entrypoint node \
      --env "MEMORY_CURRENT_RPC_MODE=$mode" \
      --env 'PATH=/oracle-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
      --mount "type=bind,src=$scratch/bin,dst=/oracle-bin,readonly" \
      --mount "type=bind,src=$runtime_dir,dst=/delegate-test,readonly" \
      --mount "type=bind,src=$current_extension,dst=/current-test/memoryd-current.ts,readonly" \
      --mount "type=bind,src=$current_provider,dst=/current-test/current-rpc-provider.ts,readonly" \
      "$image" /delegate-test/current-rpc-smoke.mjs
}

assert_current_rpc() {
  local mode=$1 expected_error=$2
  local stream
  stream=$(run_current_rpc "$mode")
  printf '%s\n' "$stream" | jq -s -e --arg mode "$mode" \
    --argjson expected_error "$expected_error" '
    ([.[] | select(.type == "tool_execution_start" and
      .toolName == "memoryd_current" and .args == {})] | length) == 1 and
    ([.[] | select(.type == "tool_execution_end" and
      .toolName == "memoryd_current" and
      (.result | keys | sort) == ["content", "details"] and
      .result.details == {schema:"memory.pi.current",version:1,status:$mode} and
      .isError == $expected_error and
      (.result.content | type == "array" and length == 1) and
      (.result.content[0] | keys | sort) == ["text", "type"] and
      .result.content[0].type == "text" and
      (if $mode == "projected" then
        (.result.content[0].text | fromjson) |
          .schema == "memory.agent.view" and .version == 8
       else .result.content[0].text == "Current unavailable." end))] | length) == 1 and
    any(.[]; .type == "agent_settled")
  ' >/dev/null
}

assert_current_rpc projected false
assert_current_rpc failed true
docker run --rm --entrypoint node \
  --mount "type=bind,src=$runtime_dir,dst=/delegate-test,readonly" \
  "$image" --experimental-strip-types /delegate-test/delegate.test.mjs
docker run --rm --entrypoint node \
  --env MEMORY_PI_EXTENSION=/lifecycle-test/memoryd.ts \
  --mount "type=bind,src=$runtime_dir,dst=/delegate-test,readonly" \
  --mount "type=bind,src=$lifecycle_extension,dst=/lifecycle-test/memoryd.ts,readonly" \
  "$image" --experimental-strip-types /delegate-test/lifecycle-boundary.test.mjs
docker run --rm --entrypoint node \
  --env MEMORY_PI_CURRENT_EXTENSION=/current-test/memoryd-current.ts \
  --mount "type=bind,src=$runtime_dir,dst=/delegate-test,readonly" \
  --mount "type=bind,src=$current_extension,dst=/current-test/memoryd-current.ts,readonly" \
  "$image" --experimental-strip-types /delegate-test/current-tool.test.mjs

printf 'pi Runtime oracle: PASS\n'
