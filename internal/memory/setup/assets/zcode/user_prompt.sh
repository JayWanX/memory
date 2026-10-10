#!/bin/bash
# memory ZCode UserPromptSubmit hook.

python3 -c 'import json, sys; sys.stdin.read(); print(json.dumps({"hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": "[memory] Evaluate: recall needed? After responding, evaluate: remember needed?"}}))'
