#!/bin/bash
# memory Codex SessionStart hook
# Same context as before (memory-active status / missing-binary warning / guide),
# wrapped in one SessionStart JSON object so Codex does not mis-parse the leading
# "[memory]". python3 is already required by the sibling stop.sh.
PROMPT_DIR="${MEMORY_DATA_DIR:-$HOME/.memory}/prompt"
# Fall back to legacy location if the resolved path has no guide yet but the
# legacy ~/.memory/prompt/ does — preserves existing installs that pre-date
# MEMORY_DATA_DIR-aware setup.
if [ ! -f "${PROMPT_DIR}/guide.md" ] && [ -f "${HOME}/.memory/prompt/guide.md" ]; then
  PROMPT_DIR="${HOME}/.memory/prompt"
fi

{
  if ! command -v memory >/dev/null 2>&1; then
    echo "[memory] Warning: memory not found in PATH."
  else
    STATS=$(memory status 2>/dev/null)
    if [ -n "$STATS" ]; then
      INSIGHTS=$(echo "$STATS" | sed -n 's/.*"total_insights": *\([0-9]*\).*/\1/p' | head -1)
      EDGES=$(echo "$STATS" | sed -n 's/.*"edge_count": *\([0-9]*\).*/\1/p' | head -1)
      echo "[memory] Memory active (${INSIGHTS:-0} insights, ${EDGES:-0} edges)."
    else
      echo "[memory] Memory active."
    fi
  fi

  [ -f "${PROMPT_DIR}/guide.md" ] && cat "${PROMPT_DIR}/guide.md"
} | python3 -c 'import json, sys; print(json.dumps({"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": sys.stdin.buffer.read().decode("utf-8", "replace")}}))'
