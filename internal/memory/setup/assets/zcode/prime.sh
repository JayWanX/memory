#!/bin/bash
# memory ZCode SessionStart hook.

cat >/dev/null || true

PROMPT_DIR="${MEMORY_DATA_DIR:-$HOME/.memory}/prompt"
if [ ! -f "${PROMPT_DIR}/guide.md" ] && [ -f "${HOME}/.memory/prompt/guide.md" ]; then
  PROMPT_DIR="${HOME}/.memory/prompt"
fi

resolve_memory() {
  if [ -n "${MEMORY_BIN:-}" ] && [ -x "${MEMORY_BIN}" ]; then
    printf "%s" "${MEMORY_BIN}"
    return 0
  fi
  if command -v memory >/dev/null 2>&1; then
    command -v memory
    return 0
  fi
  if [ -x "${HOME}/go/bin/memory" ]; then
    printf "%s" "${HOME}/go/bin/memory"
    return 0
  fi
  return 1
}

{
  if MEMORY="$(resolve_memory)"; then
    STATS="$("${MEMORY}" status 2>/dev/null || true)"
    if [ -n "${STATS}" ]; then
      INSIGHTS=$(echo "${STATS}" | sed -n 's/.*"total_insights": *\([0-9]*\).*/\1/p' | head -1)
      EDGES=$(echo "${STATS}" | sed -n 's/.*"edge_count": *\([0-9]*\).*/\1/p' | head -1)
      echo "[memory] Memory active (${INSIGHTS:-0} insights, ${EDGES:-0} edges)."
    else
      echo "[memory] Memory active."
    fi
  else
    echo "[memory] Warning: memory not found in PATH. Set MEMORY_BIN or add memory to PATH."
  fi

  if [ -f "${PROMPT_DIR}/guide.md" ]; then
    cat "${PROMPT_DIR}/guide.md"
  fi
} | python3 -c 'import json, sys; text = sys.stdin.read().strip(); print(json.dumps({"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": text}} if text else {}))'
