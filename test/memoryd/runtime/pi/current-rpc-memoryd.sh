#!/bin/sh

set -eu

test "$#" = 4
test "$1" = agency
test "$2" = agent
test "$3" = current
test "$4" = --json
test -z "$(cat)"

if test "${MEMORY_CURRENT_RPC_MODE:-projected}" = failed; then
  exit 1
fi

printf '%s\n' '{"schema":"memory.agent.view","version":8,"view":"view:rpc-current","outstanding":{"open_total":0,"related_total":0,"related_projected":0,"truncated":false},"allowed_intents":[]}'
