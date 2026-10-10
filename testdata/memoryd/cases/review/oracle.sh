#!/usr/bin/env bash

r7_run_case() {
  local case_dir=$1 view initial_implementer receipt peer subject artifact related reply_to
  local playbook_capture first_capture rework_capture revision_capture acceptance_capture
  local playbook_handle first_handle rework_handle revision_handle acceptance_handle intent

  view=$(r7_fresh_current implementer)
  test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
    r7_fail "implementer did not begin with an empty View"
  initial_implementer=$view

  # Rotate the reviewer's empty Current before work arrives. A later boundary
  # must recover the remotely created local responsibility.
  view=$(r7_fresh_current reviewer)
  test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
    r7_fail "reviewer did not begin with an empty View"

  cd "$case_dir"
  playbook_capture=$(r7_capture implementer playbook.md)
  first_capture=$(r7_capture implementer artifacts/candidate-v1.txt)
  playbook_handle=$(printf '%s' "$playbook_capture" | jq -r .handle)
  first_handle=$(printf '%s' "$first_capture" | jq -r .handle)
  peer=$(r7_remote_alias "$initial_implementer" reviewer)
  test -n "$peer" || r7_fail "reviewer target was absent"
  intent=$(jq -cn --arg peer "$peer" --arg playbook "$playbook_handle" --arg candidate "$first_handle" \
    '{kind:"review.request",payload:"review the bounded candidate",consequence:"handling.create",successors:[{self:true},{alias:$peer}],artifacts:[{kind:"candidate",handle:$playbook},{kind:"candidate",handle:$candidate}]}')
  receipt=$(r7_submit implementer "$intent")
  r7_expect_accepted "$receipt" "initial review request"

  # Delivery cannot settle the requester's local responsibility. It remains
  # open while the reviewer independently owns and processes its own Handling.
  view=$(r7_next_current implementer)
  test "$(printf '%s' "$view" | jq -r '.current.facts.reply_observation_pending')" = true || \
    r7_fail "implementer did not retain a pending local review responsibility"

  r7_restart_node reviewer
  view=$(r7_next_current reviewer)
  r7_assert_view_artifacts_match_files reviewer "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/candidate-v1.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  peer=$(printf '%s' "$view" | jq -r .current.facts.reply_target)
  reply_to=$(printf '%s' "$view" | jq -r .current.facts.reply_to)
  rework_capture=$(r7_capture reviewer "$case_dir/artifacts/rework.txt")
  rework_handle=$(printf '%s' "$rework_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg peer "$peer" --arg reply_to "$reply_to" \
    --arg artifact "$rework_handle" \
    '{kind:"review.rework",payload:"the first candidate needs one bounded revision",consequence:"handling.resolve.declined",subject_handling:$subject,successors:[{alias:$peer}],artifacts:[{kind:"candidate",handle:$artifact}],correlation_handle:$reply_to}')
  receipt=$(r7_submit reviewer "$intent")
  r7_expect_accepted "$receipt" "declined review reply"

  view=$(r7_next_terminal_reply implementer declined)
  test "$(printf '%s' "$view" | jq -r '.current.facts.reply_observation_pending')" = false || \
    r7_fail "declined terminal reply did not settle the pending observation"
  r7_assert_terminal_reply_artifacts_match_files implementer "$view" declined \
    "$case_dir/artifacts/rework.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  peer=$(r7_remote_alias "$view" reviewer)
  reply_to=$(printf '%s' "$view" | jq -r .current.facts.reply_to)
  related=$(printf '%s' "$view" | jq -r \
    '.related[] | select(.facts.relation == "terminal_reply" and .facts.outcome == "declined") | .facts.event')
  playbook_capture=$(r7_capture implementer "$case_dir/playbook.md")
  revision_capture=$(r7_capture implementer "$case_dir/artifacts/candidate-v2.txt")
  playbook_handle=$(printf '%s' "$playbook_capture" | jq -r .handle)
  revision_handle=$(printf '%s' "$revision_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg peer "$peer" --arg reply_to "$reply_to" \
    --arg related "$related" --arg playbook "$playbook_handle" --arg candidate "$revision_handle" \
    '{kind:"review.revision",payload:"review the revised candidate",consequence:"handling.advance",subject_handling:$subject,successors:[{alias:$peer}],artifacts:[{kind:"candidate",handle:$playbook},{kind:"candidate",handle:$candidate}],causation_handles:[$related],correlation_handle:$reply_to}')
  receipt=$(r7_submit implementer "$intent")
  r7_expect_accepted "$receipt" "revised candidate"

  view=$(r7_next_current reviewer)
  r7_assert_view_artifacts_match_files reviewer "$view" "$case_dir/playbook.md" \
    "$case_dir/artifacts/candidate-v2.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  peer=$(printf '%s' "$view" | jq -r .current.facts.reply_target)
  reply_to=$(printf '%s' "$view" | jq -r .current.facts.reply_to)
  acceptance_capture=$(r7_capture reviewer "$case_dir/artifacts/acceptance.txt")
  acceptance_handle=$(printf '%s' "$acceptance_capture" | jq -r .handle)
  intent=$(jq -cn --arg subject "$subject" --arg peer "$peer" --arg reply_to "$reply_to" \
    --arg artifact "$acceptance_handle" \
    '{kind:"review.accept",payload:"the revised candidate is accepted",consequence:"handling.resolve.completed",subject_handling:$subject,successors:[{alias:$peer}],artifacts:[{kind:"candidate",handle:$artifact}],correlation_handle:$reply_to}')
  receipt=$(r7_submit reviewer "$intent")
  r7_expect_accepted "$receipt" "completed review reply"

  view=$(r7_next_terminal_reply implementer completed)
  test "$(printf '%s' "$view" | jq -r '.current.facts.reply_observation_pending')" = false || \
    r7_fail "completed terminal reply did not settle the pending observation"
  r7_assert_terminal_reply_artifacts_match_files implementer "$view" completed \
    "$case_dir/artifacts/acceptance.txt"
  subject=$(printf '%s' "$view" | jq -r .current.facts.handle)
  related=$(printf '%s' "$view" | jq -r \
    '.related[] | select(.facts.relation == "terminal_reply" and .facts.outcome == "completed") | .facts.event')
  artifact=$(printf '%s' "$view" | jq -r \
    '.related[] | select(.facts.relation == "terminal_reply" and .facts.outcome == "completed") | .facts.artifacts[0].handle')
  intent=$(jq -cn --arg subject "$subject" --arg related "$related" --arg artifact "$artifact" \
    '{kind:"review.adopt",payload:"the accepted remote review result was locally verified",consequence:"handling.resolve.completed",subject_handling:$subject,artifacts:[{kind:"view_handle",handle:$artifact}],causation_handles:[$related]}')
  receipt=$(r7_submit implementer "$intent")
  r7_expect_accepted "$receipt" "local adoption of review result"

  for peer in implementer reviewer; do
    view=$(r7_fresh_current "$peer")
    test "$(printf '%s' "$view" | jq -r '.current // "none"')" = none || \
      r7_fail "$peer retained an unexpected open Handling"
    test "$(printf '%s' "$view" | jq -r '.outstanding.open_total')" = 0 || \
      r7_fail "$peer did not drain all local responsibilities"
  done
}
