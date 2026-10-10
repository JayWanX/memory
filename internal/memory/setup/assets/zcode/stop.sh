#!/bin/bash
# memory ZCode Stop hook. Continue at most once so the agent can evaluate writeback.

python3 -c '
import json
import sys

try:
    payload = json.load(sys.stdin)
except Exception:
    payload = {}

if payload.get("stop_hook_active"):
    sys.exit(0)

last_message = (payload.get("last_assistant_message") or "").lower()
if any(token in last_message for token in ("memory", "durable memory", "持久记忆", "写入记忆")):
    sys.exit(0)

print(json.dumps({
    "decision": "block",
    "reason": (
        "[memory] Briefly evaluate whether this exchange warrants durable memory. "
        "If yes, use the memory skill/CLI to remember only durable, non-secret facts; "
        "otherwise say no durable memory is needed. "
        "Include [memory] in your evaluation, in any language."
    ),
}))
'
