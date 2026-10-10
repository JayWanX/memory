#!/bin/bash
PROMPT_DIR="${MEMORY_DATA_DIR:-$HOME/.memory}/prompt"
if [ ! -f "${PROMPT_DIR}/guide.md" ] && [ -f "${HOME}/.memory/prompt/guide.md" ]; then
  PROMPT_DIR="${HOME}/.memory/prompt"
fi

if ! command -v memory >/dev/null 2>&1; then
  echo "[memory] Warning: memory not found in PATH."
  [ -f "${PROMPT_DIR}/guide.md" ] && cat "${PROMPT_DIR}/guide.md"
  exit 0
fi

STATS=$(memory status 2>/dev/null)
if [ -n "$STATS" ]; then
  INSIGHTS=$(echo "$STATS" | sed -n 's/.*"total_insights": *\([0-9]*\).*/\1/p' | head -1)
  EDGES=$(echo "$STATS" | sed -n 's/.*"edge_count": *\([0-9]*\).*/\1/p' | head -1)
  echo "[memory] Memory active (${INSIGHTS:-0} insights, ${EDGES:-0} edges)."
else
  echo "[memory] Memory active."
fi

[ -f "${PROMPT_DIR}/guide.md" ] && cat "${PROMPT_DIR}/guide.md"
