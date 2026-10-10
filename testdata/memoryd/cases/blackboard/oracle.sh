#!/usr/bin/env bash

blackboard_artifact_handle() {
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
  r7_fail "node $node did not offer the expected Blackboard Artifact"
}

blackboard_reference_head() {
  local view=$1
  printf '%s' "$view" | jq -r \
    '.references[]? | select(.facts.key == "blackboard.signal" and .facts.state == "active") | .facts.head' | \
    head -1
}

blackboard_reference_artifact() {
  local view=$1
  printf '%s' "$view" | jq -r \
    '.references[]? | select(.facts.key == "blackboard.signal" and .facts.state == "active") | .facts.artifact.handle' | \
    head -1
}

blackboard_complete_current() {
  local node=$1 view=$2 artifact_file=$3 label=$4 subject artifact intent receipt
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  artifact=$(blackboard_artifact_handle "$node" "$view" "$artifact_file")
  intent=$(jq -cn --arg subject "$subject" --arg artifact "$artifact" \
    '{kind:"blackboard.done",payload:"the local evidence contribution is durably recorded",consequence:"handling.resolve.completed",subject_handling:$subject,artifacts:[{kind:"view_handle",handle:$artifact}]}')
  receipt=$(r7_submit "$node" "$intent")
  r7_expect_accepted "$receipt" "$label"
}

blackboard_receive_and_complete() {
  local node=$1 playbook=$2 artifact=$3 label=$4 view
  view=$(r7_next_current "$node")
  r7_assert_view_artifacts_match_files "$node" "$view" "$playbook" "$artifact"
  blackboard_complete_current "$node" "$view" "$artifact" "$label"
}

r7_run_case() {
  local case_dir=$1 node view receipt intent subject playbook_capture playbook
  local finding_capture finding peer_challenger peer_verifier peer_resolver peer_evidence
  local challenge_capture challenge head_v1 head_v2 finding_v2_capture finding_v2
  local stale_capture stale_candidate stale_result reference_artifact
  local verification_capture verification resolution_capture resolution

  for node in challenger verifier resolver; do
    view=$(r7_fresh_current "$node")
    test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
      r7_fail "$node did not begin with an empty View"
  done

  view=$(r7_fresh_current evidence)
  test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
    r7_fail "evidence did not begin with an empty View"

  cd "$case_dir"
  finding_capture=$(r7_capture evidence artifacts/finding-v1.txt)
  finding=$(printf '%s' "$finding_capture" | jq -r .handle)
  intent=$(jq -cn --arg finding "$finding" \
    '{kind:"blackboard.finding.publish",payload:"publish the initial bounded finding",consequence:"reference.publish",reference_key:"blackboard.signal",artifacts:[{kind:"candidate",handle:$finding}]}')
  receipt=$(r7_submit evidence "$intent")
  r7_expect_accepted "$receipt" "initial finding publication"

  # An accepted Reference ends its Host turn. A fresh turn projects the newly
  # accepted local head without inventing a second state store.
  view=$(r7_fresh_current evidence)
  head_v1=$(blackboard_reference_head "$view")
  reference_artifact=$(blackboard_reference_artifact "$view")
  test -n "$head_v1" && test -n "$reference_artifact" || \
    r7_fail "the initial active Reference was not projected"
  r7_read_artifact evidence "$reference_artifact" >"$R7_RUNTIME_DIR/finding-v1.actual"
  cmp -s "$case_dir/artifacts/finding-v1.txt" "$R7_RUNTIME_DIR/finding-v1.actual" || \
    r7_fail "the initial Reference projected incorrect bytes"
  rm -f "$R7_RUNTIME_DIR/finding-v1.actual"

  playbook_capture=$(r7_capture evidence "$case_dir/playbook.md")
  playbook=$(printf '%s' "$playbook_capture" | jq -r .handle)
  peer_challenger=$(r7_remote_alias "$view" challenger)
  peer_verifier=$(r7_remote_alias "$view" verifier)
  peer_resolver=$(r7_remote_alias "$view" resolver)
  test -n "$peer_challenger" && test -n "$peer_verifier" && test -n "$peer_resolver" || \
    r7_fail "one or more Blackboard targets were absent"
  intent=$(jq -cn --arg challenger "$peer_challenger" --arg verifier "$peer_verifier" \
    --arg resolver "$peer_resolver" --arg playbook "$playbook" \
    --arg finding "$reference_artifact" \
    '{kind:"blackboard.observe",payload:"inspect the bounded finding and its evidence",consequence:"handling.create",successors:[{self:true},{alias:$challenger},{alias:$verifier},{alias:$resolver}],artifacts:[{kind:"candidate",handle:$playbook},{kind:"view_handle",handle:$finding}]}')
  receipt=$(r7_submit evidence "$intent")
  r7_expect_accepted "$receipt" "initial evidence fan-out"

  view=$(r7_next_current evidence)
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  intent=$(jq -cn --arg subject "$subject" \
    '{kind:"blackboard.wait",payload:"remote evidence handling remains independently pending",consequence:"handling.resolve.unresolved",subject_handling:$subject}')
  receipt=$(r7_submit evidence "$intent")
  r7_expect_accepted "$receipt" "initial evidence local anchor disposition"

  # Two consumers finish their first observation before the challenge. Their
  # later turns therefore depend only on durable peer deliveries, not on a
  # shared transcript.
  blackboard_receive_and_complete verifier "$case_dir/playbook.md" \
    "$case_dir/artifacts/finding-v1.txt" "verifier initial observation"
  blackboard_receive_and_complete resolver "$case_dir/playbook.md" \
    "$case_dir/artifacts/finding-v1.txt" "resolver initial observation"

  view=$(r7_next_current challenger)
  r7_assert_view_artifacts_match_files challenger "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/finding-v1.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  playbook=$(blackboard_artifact_handle challenger "$view" "$case_dir/playbook.md")
  peer_evidence=$(r7_remote_alias "$view" evidence)
  peer_verifier=$(r7_remote_alias "$view" verifier)
  peer_resolver=$(r7_remote_alias "$view" resolver)
  challenge_capture=$(r7_capture challenger "$case_dir/artifacts/challenge.txt")
  challenge=$(printf '%s' "$challenge_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg evidence "$peer_evidence" \
    --arg verifier "$peer_verifier" --arg resolver "$peer_resolver" \
    --arg playbook "$playbook" --arg challenge "$challenge" \
    '{kind:"blackboard.challenge",payload:"the initial finding conflicts with an independent observation",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$evidence},{alias:$verifier},{alias:$resolver}],artifacts:[{kind:"view_handle",handle:$playbook},{kind:"candidate",handle:$challenge}]}')
  receipt=$(r7_submit challenger "$intent")
  r7_expect_accepted "$receipt" "evidence challenge"
  view=$(r7_next_current challenger)
  r7_assert_view_artifacts_match_files challenger "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/challenge.txt"
  blackboard_complete_current challenger "$view" "$case_dir/artifacts/challenge.txt" \
    "challenger contribution completion"

  view=$(r7_next_current evidence)
  r7_assert_view_artifacts_match_files evidence "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/challenge.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  head_v1=$(blackboard_reference_head "$view")
  test -n "$head_v1" || r7_fail "the v1 Reference head was absent during revision"
  finding_v2_capture=$(r7_capture evidence "$case_dir/artifacts/finding-v2.txt")
  finding_v2=$(printf '%s' "$finding_v2_capture" | jq -r .handle)
  intent=$(jq -cn --arg head "$head_v1" --arg finding "$finding_v2" \
    '{kind:"blackboard.finding.supersede",payload:"replace the challenged finding with verified source data",consequence:"reference.supersede",reference_head:$head,artifacts:[{kind:"candidate",handle:$finding}]}')
  receipt=$(r7_submit evidence "$intent")
  r7_expect_accepted "$receipt" "finding supersession"

  view=$(r7_fresh_current evidence)
  subject=$(printf '%s' "$view" | jq -r '.current.facts.handle // empty')
  test -n "$subject" || r7_fail "a fresh Handling was absent after supersession"
  head_v2=$(blackboard_reference_head "$view")
  reference_artifact=$(blackboard_reference_artifact "$view")
  test -n "$head_v2" && test "$head_v2" != "$head_v1" || \
    r7_fail "supersession did not move the active Reference head"
  r7_read_artifact evidence "$reference_artifact" >"$R7_RUNTIME_DIR/finding-v2.actual"
  cmp -s "$case_dir/artifacts/finding-v2.txt" "$R7_RUNTIME_DIR/finding-v2.actual" || \
    r7_fail "the superseded Reference projected incorrect bytes"
  rm -f "$R7_RUNTIME_DIR/finding-v2.actual"

  # The old opaque head came from a different View and must fail closed. This
  # case does not inspect SQLite; the P-08 suite independently proves exact
  # CAS lineage. Here the public surface proves that stale authority cannot be
  # carried forward by the Agent. Its rejection retains this exact View, so
  # the valid Handling Intent below is an amended retry in the same Host turn.
  stale_capture=$(r7_capture evidence "$case_dir/artifacts/finding-v1.txt")
  stale_candidate=$(printf '%s' "$stale_capture" | jq -r .handle)
  intent=$(jq -cn --arg head "$head_v1" --arg finding "$stale_candidate" \
    '{kind:"blackboard.finding.supersede",payload:"attempt a stale overwrite",consequence:"reference.supersede",reference_head:$head,artifacts:[{kind:"candidate",handle:$finding}]}')
  if stale_result=$(r7_submit evidence "$intent" 2>&1); then
    test "$(printf '%s' "$stale_result" | jq -r '.outcome // "error"')" != accepted || \
      r7_fail "a stale Reference head was accepted"
  fi

  playbook=$(blackboard_artifact_handle evidence "$view" "$case_dir/playbook.md")
  peer_challenger=$(r7_remote_alias "$view" challenger)
  peer_verifier=$(r7_remote_alias "$view" verifier)
  peer_resolver=$(r7_remote_alias "$view" resolver)
  intent=$(jq -cn --arg subject "$subject" --arg challenger "$peer_challenger" \
    --arg verifier "$peer_verifier" --arg resolver "$peer_resolver" \
    --arg playbook "$playbook" --arg finding "$reference_artifact" \
    '{kind:"blackboard.revision",payload:"the challenged finding now has a revised active version",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$challenger},{alias:$verifier},{alias:$resolver}],artifacts:[{kind:"view_handle",handle:$playbook},{kind:"view_handle",handle:$finding}]}')
  receipt=$(r7_submit evidence "$intent")
  r7_expect_accepted "$receipt" "revised evidence fan-out"
  view=$(r7_next_current evidence)
  r7_assert_view_artifacts_match_files evidence "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/finding-v2.txt"
  blackboard_complete_current evidence "$view" "$case_dir/artifacts/finding-v2.txt" \
    "evidence revision completion"

  blackboard_receive_and_complete verifier "$case_dir/playbook.md" \
    "$case_dir/artifacts/challenge.txt" "verifier challenge observation"
  view=$(r7_next_current verifier)
  r7_assert_view_artifacts_match_files verifier "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/finding-v2.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  playbook=$(blackboard_artifact_handle verifier "$view" "$case_dir/playbook.md")
  peer_evidence=$(r7_remote_alias "$view" evidence)
  peer_resolver=$(r7_remote_alias "$view" resolver)
  verification_capture=$(r7_capture verifier "$case_dir/artifacts/verification.txt")
  verification=$(printf '%s' "$verification_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg evidence "$peer_evidence" \
    --arg resolver "$peer_resolver" --arg playbook "$playbook" \
    --arg verification "$verification" \
    '{kind:"blackboard.verify",payload:"an independent check confirms the revised signal",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$evidence},{alias:$resolver}],artifacts:[{kind:"view_handle",handle:$playbook},{kind:"candidate",handle:$verification}]}')
  receipt=$(r7_submit verifier "$intent")
  r7_expect_accepted "$receipt" "independent verification"
  view=$(r7_next_current verifier)
  r7_assert_view_artifacts_match_files verifier "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/verification.txt"
  blackboard_complete_current verifier "$view" "$case_dir/artifacts/verification.txt" \
    "verifier completion"

  r7_restart_node resolver
  blackboard_receive_and_complete resolver "$case_dir/playbook.md" \
    "$case_dir/artifacts/challenge.txt" "resolver challenge observation"
  blackboard_receive_and_complete resolver "$case_dir/playbook.md" \
    "$case_dir/artifacts/finding-v2.txt" "resolver revision observation"
  view=$(r7_next_current resolver)
  r7_assert_view_artifacts_match_files resolver "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/verification.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  playbook=$(blackboard_artifact_handle resolver "$view" "$case_dir/playbook.md")
  peer_evidence=$(r7_remote_alias "$view" evidence)
  resolution_capture=$(r7_capture resolver "$case_dir/artifacts/resolution.txt")
  resolution=$(printf '%s' "$resolution_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg evidence "$peer_evidence" \
    --arg playbook "$playbook" --arg resolution "$resolution" \
    '{kind:"blackboard.resolve",payload:"resolve from the bounded verified evidence",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$evidence}],artifacts:[{kind:"view_handle",handle:$playbook},{kind:"candidate",handle:$resolution}]}')
  receipt=$(r7_submit resolver "$intent")
  r7_expect_accepted "$receipt" "evidence resolution"
  view=$(r7_next_current resolver)
  r7_assert_view_artifacts_match_files resolver "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/resolution.txt"
  blackboard_complete_current resolver "$view" "$case_dir/artifacts/resolution.txt" \
    "resolver completion"

  blackboard_receive_and_complete challenger "$case_dir/playbook.md" \
    "$case_dir/artifacts/finding-v2.txt" "challenger revision observation"
  blackboard_receive_and_complete evidence "$case_dir/playbook.md" \
    "$case_dir/artifacts/verification.txt" "evidence verification observation"
  blackboard_receive_and_complete evidence "$case_dir/playbook.md" \
    "$case_dir/artifacts/resolution.txt" "evidence resolution observation"

  view=$(r7_fresh_current evidence)
  test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
    r7_fail "evidence retained an unexpected Handling after resolution"
  head_v2=$(blackboard_reference_head "$view")
  reference_artifact=$(blackboard_reference_artifact "$view")
  test -n "$head_v2" && test "$head_v2" != "$head_v1" || \
    r7_fail "the revised Reference head was not durable"
  r7_read_artifact evidence "$reference_artifact" >"$R7_RUNTIME_DIR/final-reference.actual"
  cmp -s "$case_dir/artifacts/finding-v2.txt" "$R7_RUNTIME_DIR/final-reference.actual" || \
    r7_fail "the durable active Reference does not match finding v2"
  rm -f "$R7_RUNTIME_DIR/final-reference.actual"
}
