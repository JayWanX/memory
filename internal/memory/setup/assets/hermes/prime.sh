#!/bin/bash
# memory Hermes on_session_start hook.
# Hermes shell hooks return JSON. Session hooks are observational in Hermes, so
# this records lightweight state for the next pre_llm_call injection.

STATE_DIR="${HERMES_HOME:-$HOME/.hermes}/memory"
mkdir -p "$STATE_DIR" 2>/dev/null || true

PROMPT_DIR="${MEMORY_DATA_DIR:-$HOME/.memory}/prompt"
if [ ! -f "${PROMPT_DIR}/guide.md" ] && [ -f "${HOME}/.memory/prompt/guide.md" ]; then
  PROMPT_DIR="${HOME}/.memory/prompt"
fi

{
  echo "[memory] Memory lifecycle is active for Hermes."
  if command -v memory >/dev/null 2>&1; then
    STATS=$(memory status 2>/dev/null || true)
    if [ -n "$STATS" ]; then
      INSIGHTS=$(echo "$STATS" | sed -n 's/.*"total_insights": *\([0-9]*\).*/\1/p' | head -1)
      EDGES=$(echo "$STATS" | sed -n 's/.*"edge_count": *\([0-9]*\).*/\1/p' | head -1)
      echo "[memory] Memory active (${INSIGHTS:-0} insights, ${EDGES:-0} edges)."
    else
      echo "[memory] Memory active."
    fi
  else
    echo "[memory] Warning: memory not found in PATH."
  fi
  [ -f "${PROMPT_DIR}/guide.md" ] && cat "${PROMPT_DIR}/guide.md"
} > "${STATE_DIR}/prime-context.txt" 2>/dev/null || true

printf '{}\n'
