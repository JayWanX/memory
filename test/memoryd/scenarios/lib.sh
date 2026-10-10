#!/usr/bin/env bash

# Generic R7 Docker mechanics. Case semantics belong in testdata/memoryd/cases.

set -euo pipefail

R7_RUNNER_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
R7_REPOSITORY_ROOT=$(cd "$R7_RUNNER_DIR/../../.." && pwd -P)
R7_IMAGE=${R7_IMAGE:-memory-r7-case:$$}
R7_KEEP=${R7_KEEP:-0}
R7_CASE_DIR=
R7_CASE_NAME=
R7_RUN_PREFIX=
R7_NETWORK=
R7_NODES=
R7_RUNTIME_DIR=
R7_IMAGE_ID=
R7_BINARY_DIGESTS=

r7_fail() {
  printf 'r7 case: %s\n' "$*" >&2
  return 1
}

r7_require_tools() {
  command -v docker >/dev/null 2>&1 || r7_fail "docker is required"
  command -v jq >/dev/null 2>&1 || r7_fail "jq is required"
  docker info >/dev/null 2>&1 || r7_fail "Docker Engine is unavailable"
}

r7_build_image() {
  docker build --quiet -f "$R7_REPOSITORY_ROOT/test/memoryd/scenarios/Dockerfile" \
    -t "$R7_IMAGE" "$R7_REPOSITORY_ROOT" >/dev/null
  R7_IMAGE_ID=$(docker image inspect --format '{{.Id}}' "$R7_IMAGE")
  R7_BINARY_DIGESTS=$(docker run --rm --entrypoint sha256sum "$R7_IMAGE" \
    /usr/local/bin/memory)
  test -n "$R7_IMAGE_ID" && test -n "$R7_BINARY_DIGESTS" || \
    r7_fail "candidate image identity is unavailable"
}

r7_validate_token() {
  case "$1" in
    ''|*[!a-z0-9-]*) return 1 ;;
    *) return 0 ;;
  esac
}

r7_container() {
  local node=$1
  r7_validate_token "$node" || r7_fail "invalid node name"
  printf '%s-%s\n' "$R7_RUN_PREFIX" "$node"
}

r7_begin_case() {
  local case_dir=$1 node container remote origin
  R7_CASE_DIR=$(cd "$case_dir" && pwd -P)
  R7_CASE_NAME=$(basename "$R7_CASE_DIR")
  r7_validate_token "$R7_CASE_NAME" || r7_fail "invalid case name"
  test -f "$R7_CASE_DIR/nodes.txt" || r7_fail "nodes.txt is missing"
  R7_NODES=$(awk 'NF { print }' "$R7_CASE_DIR/nodes.txt")
  test -n "$R7_NODES" || r7_fail "nodes.txt is empty"
  test "$(printf '%s\n' "$R7_NODES" | sort -u | wc -l | tr -d ' ')" = \
    "$(printf '%s\n' "$R7_NODES" | wc -l | tr -d ' ')" || r7_fail "nodes must be unique"
  while IFS= read -r node; do
    r7_validate_token "$node" || r7_fail "invalid node in nodes.txt"
  done <<<"$R7_NODES"

  R7_RUN_PREFIX="mnr7-${R7_CASE_NAME}-$$"
  R7_NETWORK="${R7_RUN_PREFIX}-net"
  R7_RUNTIME_DIR=$(mktemp -d)
  docker network create "$R7_NETWORK" >/dev/null
  while IFS= read -r node; do
    container=$(r7_container "$node")
    docker run -d --name "$container" --hostname "$node" --network "$R7_NETWORK" \
      --label memory.r7.case="$R7_CASE_NAME" "$R7_IMAGE" >/dev/null
    test "$(docker inspect --format '{{.Image}}' "$container")" = "$R7_IMAGE_ID" || \
      r7_fail "node $node does not run the candidate image"
    test "$(docker exec "$container" sha256sum /usr/local/bin/memory)" = \
      "$R7_BINARY_DIGESTS" || r7_fail "node $node does not run the candidate binary"
  done <<<"$R7_NODES"

  while IFS= read -r node; do
    container=$(r7_container "$node")
    docker exec -w /workspace "$container" memory agency peer prepare \
      --listen 0.0.0.0:7447 --advertise "$node:7447" --project-root /workspace \
      >"$R7_RUNTIME_DIR/$node.card.json"
  done <<<"$R7_NODES"

  while IFS= read -r origin; do
    while IFS= read -r remote; do
      test "$origin" = "$remote" && continue
      docker exec -i -w /workspace "$(r7_container "$origin")" \
        memory agency peer enroll --alias "$remote" --project-root /workspace \
        <"$R7_RUNTIME_DIR/$remote.card.json" >/dev/null
    done <<<"$R7_NODES"
    docker exec -w /workspace "$(r7_container "$origin")" \
      memory agency setup --runtime pi --project-root /workspace >/dev/null
  done <<<"$R7_NODES"
}

r7_end_case() {
  local node container
  if test "$R7_KEEP" = 1; then
    printf 'r7 case retained: %s\n' "$R7_RUN_PREFIX" >&2
    return 0
  fi
  if test -n "$R7_NODES"; then
    while IFS= read -r node; do
      container=$(r7_container "$node")
      docker rm -f "$container" >/dev/null 2>&1 || true
    done <<<"$R7_NODES"
  fi
  if test -n "$R7_NETWORK"; then
    docker network rm "$R7_NETWORK" >/dev/null 2>&1 || true
  fi
  if test -n "$R7_RUNTIME_DIR" && test -d "$R7_RUNTIME_DIR"; then
    rm -f "$R7_RUNTIME_DIR"/*.card.json
    rmdir "$R7_RUNTIME_DIR"
  fi
  R7_CASE_DIR=
  R7_CASE_NAME=
  R7_RUN_PREFIX=
  R7_NETWORK=
  R7_NODES=
  R7_RUNTIME_DIR=
}

r7_exec() {
  local node=$1
  shift
  docker exec -w /workspace "$(r7_container "$node")" "$@"
}

r7_boundary_envelope() {
  local boundary
  boundary=$(dd if=/dev/urandom bs=32 count=1 2>/dev/null | base64 | tr '+/' '-_' | tr -d '=\n')
  test "${#boundary}" = 43 || r7_fail "Host boundary entropy is unavailable"
  printf '{"boundary":"%s","schema":"memory.hook.boundary","version":1}' "$boundary"
}

r7_attach() {
  r7_boundary_envelope | docker exec -i -w /workspace "$(r7_container "$1")" \
    memory agency hook attach --json >/dev/null
}

r7_current() {
  r7_exec "$1" memory agency agent current --json
}

r7_fresh_current() {
  local node=$1
  r7_attach "$node"
  r7_current "$node"
}

r7_next_current() {
  local node=$1 attempts=${2:-40} view index
  index=1
  while test "$index" -le "$attempts"; do
    view=$(r7_fresh_current "$node")
    if printf '%s' "$view" | jq -e '.current != null' >/dev/null; then
      printf '%s\n' "$view"
      return 0
    fi
    sleep 0.2
    index=$((index + 1))
  done
  r7_fail "node $node did not expose a current responsibility"
}

r7_next_terminal_reply() {
  local node=$1 outcome=$2 attempts=${3:-40} view index
  case "$outcome" in
    completed|declined|unresolved) ;;
    *) r7_fail "invalid terminal reply outcome: $outcome" ;;
  esac
  index=1
  while test "$index" -le "$attempts"; do
    view=$(r7_fresh_current "$node")
    if printf '%s' "$view" | jq -e --arg outcome "$outcome" \
      '.current != null and any(.related[]?;
        .facts.relation == "terminal_reply" and .facts.outcome == $outcome)' >/dev/null; then
      printf '%s\n' "$view"
      return 0
    fi
    sleep 0.2
    index=$((index + 1))
  done
  r7_fail "node $node did not expose terminal reply outcome $outcome"
}

r7_capture() {
  local node=$1 path=$2
  test -f "$path" || r7_fail "Artifact fixture is missing: $path"
  docker exec -i -w /workspace "$(r7_container "$node")" \
    memory agency artifact capture --json <"$path"
}

r7_read_artifact() {
  local node=$1 handle=$2
  r7_exec "$node" memory agency artifact read "$handle"
}

r7_submit() {
  local node=$1 intent=$2
  printf '%s' "$intent" | docker exec -i -w /workspace "$(r7_container "$node")" \
    memory agency agent submit --json
}

r7_expect_accepted() {
  local receipt=$1 label=$2
  test "$(printf '%s' "$receipt" | jq -r '.outcome')" = accepted || \
    r7_fail "$label was not accepted: $receipt"
}

r7_remote_alias() {
  local view=$1 node=$2
  printf '%s' "$view" | jq -r --arg alias "$node" \
    '.targets[] | select(. == $alias)' | head -1
}

r7_restart_node() {
  docker restart "$(r7_container "$1")" >/dev/null
}

r7_assert_artifacts_match_files() {
  local node=$1 view=$2 handles_filter=$3
  shift 3
  local temporary handle index expected actual matched
  temporary=$(mktemp -d)
  index=0
  while IFS= read -r handle; do
    r7_read_artifact "$node" "$handle" >"$temporary/actual-$index"
    index=$((index + 1))
  done < <(printf '%s' "$view" | jq -r "$handles_filter")
  test "$index" = "$#" || {
    rm -f "$temporary"/actual-*
    rmdir "$temporary"
    r7_fail "node $node received an unexpected Artifact count"
  }
  for expected in "$@"; do
    matched=
    for actual in "$temporary"/actual-*; do
      test -f "$actual" || continue
      if cmp -s "$expected" "$actual"; then
        mv "$actual" "$actual.matched"
        matched=1
        break
      fi
    done
    if test -z "$matched"; then
      rm -f "$temporary"/actual-* "$temporary"/actual-*.matched
      rmdir "$temporary"
      r7_fail "node $node did not receive expected Artifact bytes"
    fi
  done
  rm -f "$temporary"/actual-*.matched
  rmdir "$temporary"
}

r7_assert_view_artifacts_match_files() {
  local node=$1 view=$2
  shift 2
  r7_assert_artifacts_match_files "$node" "$view" \
    '.current.facts.artifacts[].handle' "$@"
}

r7_assert_terminal_reply_artifacts_match_files() {
  local node=$1 view=$2 outcome=$3
  shift 3
  r7_assert_artifacts_match_files "$node" "$view" \
    ".related[] | select(.facts.relation == \"terminal_reply\" and .facts.outcome == \"$outcome\") | .facts.artifacts[].handle" \
    "$@"
}
