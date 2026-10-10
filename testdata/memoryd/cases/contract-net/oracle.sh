#!/usr/bin/env bash

contract_net_artifact_handle() {
  local node=$1 view=$2 expected=$3 handle temporary
  temporary=$(mktemp)
  while IFS= read -r handle; do
    r7_read_artifact "$node" "$handle" >"$temporary"
    if cmp -s "$expected" "$temporary"; then
      rm -f "$temporary"
      printf '%s\n' "$handle"
      return 0
    fi
  done < <(printf '%s' "$view" | jq -r '.current.facts.artifacts[]?.handle')
  rm -f "$temporary"
  r7_fail "node $node did not offer the expected Contract Net Artifact"
}

contract_net_complete_current() {
  local node=$1 view=$2 artifact_file=$3 label=$4 subject artifact intent receipt
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  artifact=$(contract_net_artifact_handle "$node" "$view" "$artifact_file")
  intent=$(jq -cn --arg subject "$subject" --arg artifact "$artifact" \
    '{kind:"contract-net.done",payload:"the local contribution is durably recorded",consequence:"handling.resolve.completed",subject_handling:$subject,artifacts:[{kind:"view_handle",handle:$artifact}]}')
  receipt=$(r7_submit "$node" "$intent")
  r7_expect_accepted "$receipt" "$label"
}

contract_net_propose() {
  local node=$1 proposal_file=$2 view subject peer playbook proposal_capture proposal
  local intent receipt
  view=$(r7_next_current "$node")
  r7_assert_view_artifacts_match_files "$node" "$view" \
    "$R7_CASE_DIR/playbook.md" "$R7_CASE_DIR/artifacts/task.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  peer=$(r7_remote_alias "$view" initiator)
  test -n "$peer" || r7_fail "initiator target was absent for $node"
  playbook=$(contract_net_artifact_handle "$node" "$view" "$R7_CASE_DIR/playbook.md")
  proposal_capture=$(r7_capture "$node" "$proposal_file")
  proposal=$(printf '%s' "$proposal_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg peer "$peer" \
    --arg playbook "$playbook" --arg proposal "$proposal" \
    '{kind:"contract-net.proposal",payload:"one bounded proposal",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$peer}],artifacts:[{kind:"view_handle",handle:$playbook},{kind:"candidate",handle:$proposal}]}')
  receipt=$(r7_submit "$node" "$intent")
  r7_expect_accepted "$receipt" "$node proposal"

  view=$(r7_next_current "$node")
  r7_assert_view_artifacts_match_files "$node" "$view" \
    "$R7_CASE_DIR/playbook.md" "$proposal_file"
  contract_net_complete_current "$node" "$view" "$proposal_file" "$node proposal completion"
}

contract_net_decline() {
  local node=$1 view subject peer intent receipt
  view=$(r7_next_current "$node")
  r7_assert_view_artifacts_match_files "$node" "$view" \
    "$R7_CASE_DIR/playbook.md" "$R7_CASE_DIR/artifacts/task.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  peer=$(r7_remote_alias "$view" initiator)
  test -n "$peer" || r7_fail "initiator target was absent for $node"
  intent=$(jq -cn --arg subject "$subject" --arg peer "$peer" \
    '{kind:"contract-net.decline",payload:"bidder-c declines this bounded request",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$peer}]}')
  receipt=$(r7_submit "$node" "$intent")
  r7_expect_accepted "$receipt" "$node decline"

  view=$(r7_next_current "$node")
  test "$(printf '%s' "$view" | jq '.current.facts.artifacts | length')" = 0 || \
    r7_fail "$node decline unexpectedly carried an Artifact"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  intent=$(jq -cn --arg subject "$subject" \
    '{kind:"contract-net.closed",payload:"the bidder declined without claiming completion",consequence:"handling.resolve.declined",subject_handling:$subject}')
  receipt=$(r7_submit "$node" "$intent")
  r7_expect_accepted "$receipt" "$node decline closure"
}

r7_run_case() {
  local case_dir=$1 node view initial receipt subject peer_a peer_b peer_c
  local playbook_capture task_capture playbook task intent kind artifact
  local seen_a=0 seen_b=0 seen_decline=0 proposal_file cost_a cost_b
  local award_capture award result_capture result

  for node in bidder-a bidder-b bidder-c; do
    view=$(r7_fresh_current "$node")
    test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
      r7_fail "$node did not begin with an empty View"
  done

  initial=$(r7_fresh_current initiator)
  test "$(printf '%s' "$initial" | jq -r '.current // "none"')" = none || \
    r7_fail "initiator did not begin with an empty View"

  cd "$case_dir"
  playbook_capture=$(r7_capture initiator playbook.md)
  task_capture=$(r7_capture initiator artifacts/task.txt)
  playbook=$(printf '%s' "$playbook_capture" | jq -r .handle)
  task=$(printf '%s' "$task_capture" | jq -r .handle)
  peer_a=$(r7_remote_alias "$initial" bidder-a)
  peer_b=$(r7_remote_alias "$initial" bidder-b)
  peer_c=$(r7_remote_alias "$initial" bidder-c)
  test -n "$peer_a" && test -n "$peer_b" && test -n "$peer_c" || \
    r7_fail "one or more bidder targets were absent"
  intent=$(jq -cn --arg a "$peer_a" --arg b "$peer_b" --arg c "$peer_c" \
    --arg playbook "$playbook" --arg task "$task" \
    '{kind:"contract-net.request",payload:"return one bounded proposal or decline",consequence:"handling.create",successors:[{self:true},{alias:$a},{alias:$b},{alias:$c}],artifacts:[{kind:"candidate",handle:$playbook},{kind:"candidate",handle:$task}]}')
  receipt=$(r7_submit initiator "$intent")
  r7_expect_accepted "$receipt" "request for proposals"

  view=$(r7_next_current initiator)
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  intent=$(jq -cn --arg subject "$subject" \
    '{kind:"contract-net.wait",payload:"remote proposals remain independently pending",consequence:"handling.resolve.unresolved",subject_handling:$subject}')
  receipt=$(r7_submit initiator "$intent")
  r7_expect_accepted "$receipt" "request local anchor disposition"

  # Deliberately produce replies in non-ranking order. Selection belongs to
  # this oracle and its fixture, not to delivery order or memoryd.
  contract_net_propose bidder-b "$case_dir/artifacts/proposal-b.txt"
  contract_net_decline bidder-c
  contract_net_propose bidder-a "$case_dir/artifacts/proposal-a.txt"

  r7_restart_node initiator
  for node in 1 2 3; do
    view=$(r7_next_current initiator)
    kind=$(printf '%s' "$view" | jq -r .current.semantic.kind)
    case "$kind" in
      contract-net.proposal)
        if artifact=$(contract_net_artifact_handle initiator "$view" \
          "$case_dir/artifacts/proposal-a.txt" 2>/dev/null); then
          test "$seen_a" = 0 || r7_fail "proposal A was delivered more than once"
          seen_a=1
          proposal_file="$case_dir/artifacts/proposal-a.txt"
        elif artifact=$(contract_net_artifact_handle initiator "$view" \
          "$case_dir/artifacts/proposal-b.txt" 2>/dev/null); then
          test "$seen_b" = 0 || r7_fail "proposal B was delivered more than once"
          seen_b=1
          proposal_file="$case_dir/artifacts/proposal-b.txt"
        else
          r7_fail "initiator received an unknown proposal Artifact"
        fi
        r7_assert_view_artifacts_match_files initiator "$view" \
          "$case_dir/playbook.md" "$proposal_file"
        contract_net_complete_current initiator "$view" "$proposal_file" \
          "received proposal completion"
        ;;
      contract-net.decline)
        test "$seen_decline" = 0 || r7_fail "decline was delivered more than once"
        seen_decline=1
        test "$(printf '%s' "$view" | jq '.current.facts.artifacts | length')" = 0 || \
          r7_fail "received decline unexpectedly carried an Artifact"
        subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
        intent=$(jq -cn --arg subject "$subject" \
          '{kind:"contract-net.closed",payload:"the remote bidder declined",consequence:"handling.resolve.declined",subject_handling:$subject}')
        receipt=$(r7_submit initiator "$intent")
        r7_expect_accepted "$receipt" "received decline closure"
        ;;
      *) r7_fail "initiator received unexpected kind $kind" ;;
    esac
  done
  test "$seen_a:$seen_b:$seen_decline" = 1:1:1 || \
    r7_fail "initiator did not receive the complete bounded response set"

  cost_a=$(awk -F= '$1 == "cost" { print $2 }' "$case_dir/artifacts/proposal-a.txt")
  cost_b=$(awk -F= '$1 == "cost" { print $2 }' "$case_dir/artifacts/proposal-b.txt")
  test "$cost_b" -lt "$cost_a" || r7_fail "fixture no longer selects bidder-b"

  view=$(r7_fresh_current initiator)
  test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
    r7_fail "initiator retained an unexpected response Handling"
  playbook_capture=$(r7_capture initiator "$case_dir/playbook.md")
  award_capture=$(r7_capture initiator "$case_dir/artifacts/award.txt")
  playbook=$(printf '%s' "$playbook_capture" | jq -r .handle)
  award=$(printf '%s' "$award_capture" | jq -r .handle)
  peer_b=$(r7_remote_alias "$view" bidder-b)
  intent=$(jq -cn --arg peer "$peer_b" --arg playbook "$playbook" --arg award "$award" \
    '{kind:"contract-net.award",payload:"perform the selected bounded job",consequence:"handling.create",successors:[{self:true},{alias:$peer}],artifacts:[{kind:"candidate",handle:$playbook},{kind:"candidate",handle:$award}]}')
  receipt=$(r7_submit initiator "$intent")
  r7_expect_accepted "$receipt" "contract award"

  view=$(r7_next_current initiator)
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  intent=$(jq -cn --arg subject "$subject" \
    '{kind:"contract-net.wait",payload:"the awarded result remains remotely pending",consequence:"handling.resolve.unresolved",subject_handling:$subject}')
  receipt=$(r7_submit initiator "$intent")
  r7_expect_accepted "$receipt" "award local anchor disposition"

  r7_restart_node bidder-b
  view=$(r7_next_current bidder-b)
  r7_assert_view_artifacts_match_files bidder-b "$view" \
    "$case_dir/playbook.md" "$case_dir/artifacts/award.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  peer_b=$(r7_remote_alias "$view" initiator)
  playbook=$(contract_net_artifact_handle bidder-b "$view" "$case_dir/playbook.md")
  result_capture=$(r7_capture bidder-b "$case_dir/artifacts/result.txt")
  result=$(printf '%s' "$result_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg peer "$peer_b" \
    --arg playbook "$playbook" --arg result "$result" \
    '{kind:"contract-net.result",payload:"the awarded job produced its bounded result",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$peer}],artifacts:[{kind:"view_handle",handle:$playbook},{kind:"candidate",handle:$result}]}')
  receipt=$(r7_submit bidder-b "$intent")
  r7_expect_accepted "$receipt" "awarded result"

  view=$(r7_next_current bidder-b)
  contract_net_complete_current bidder-b "$view" "$case_dir/artifacts/result.txt" \
    "selected bidder completion"

  view=$(r7_next_current initiator)
  r7_assert_view_artifacts_match_files initiator "$view" \
    "$case_dir/playbook.md" "$case_dir/artifacts/result.txt"
  contract_net_complete_current initiator "$view" "$case_dir/artifacts/result.txt" \
    "initiator result completion"
}
