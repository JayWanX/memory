# Domain Operations Trace Adapter

This adapter turns stopped R7 authority stores and a sanitized runner report
into the protocol-neutral `memory.test.trace` format. It never reads a prompt,
provider stream, transcript, reasoning record, or semantic payload.

## Scenario identity

The trace header's `scenario.digest` is content addressed. It binds:

- every regular file under the domain-operations fixture tree, including the
  mission, five domain projections, Compose world, tools, tests, and fixtures;
- the paid runner, Agent Dockerfile, and load/world entry points that determine
  the attention schedule, Runtime image, and external oracle;
- the exact `domainctl`, `memory` Agency, and bounded Pi delegate
  asset digests observed in the Agent image.

Attention-wave counts, timestamps, model output, and successful outcomes are
deliberately not part of this identity. Changing the physical case or candidate
binaries changes the digest; merely rerunning the same case does not.

The runner integration is intentionally small. Preserve the existing
`sha256sum` output as a mode-0600 regular file and invoke:

```text
go run ./test/memoryd/domainops/trace \
  --report /absolute/sanitized-report.json \
  --authority /absolute/stopped-authority-root \
  --consolidation-authority /absolute/pre-consolidation-authority-root \
  --boundary-authority /absolute/episode-boundary-authority-root \
  --scenario-root /absolute/repository \
  --candidate-binaries /absolute/candidate-binaries.sha256 \
  --output /absolute/result.trace
```

The success adapter reads three independently stopped authority snapshots. The
final root proves accepted outcomes after both Episodes; the consolidation root
fixes the sequence before experience can be retained; and the boundary root
fixes the exact active Reference heads before Runtime restart. Report-owned
sequence fields cannot replace these snapshots. Failure traces need only the
final authority root because they make no evolution claim.

The candidate manifest must contain exactly the required absolute runtime
paths. The adapter independently hashes every fixture input and rejects missing,
unbound, duplicate, symlinked, oversized, or malformed inputs.

## Authority summary

For every accepted Event, the trace may expose only bounded protocol metadata:

- semantic kind;
- source Principal;
- target aliases or local Principal IDs and target count;
- Artifact digest/count;
- semantic payload byte length, never payload bytes;
- Handling or Reference effects;
- peer Delivery, re-admission, and Receipt lineage.

These fields come from canonical stopped authority state. Runtime counters never
invent a causal edge to an Event.

`turns[].delegate_calls` counts completed child-Pi effects, not tool attempts.
The Runtime may return a closed `slot_used` result for a repeated attempt, but
the report still requires at most one completed delegate per parent turn.
An exploration call stopped by the Host attention gate is a closed Runtime
disposition, not a delegate effect; it may close the turn but never increments
`delegate_calls` or supplies evidence for an Event.
The `submit_*`, `intent_submits`, and `*_receipts` turn counters describe paired
Pi Bash envelopes that visibly contained submit traffic; they do not count
shell processes or canonical Effects. Sequential corrections inside one
envelope collapse by precedence to accepted, rejected, control denial, or
invocation failure. `turns[].submit_invocation_failures` therefore counts
envelopes that exposed no Receipt or admission diagnostic. These Runtime
observations never prove or contribute evidence of an Effect; only stopped
authority state does that. `turns[].submit_control_denials` retains only a
bounded closed code and count; diagnostic messages, submitted Intents, and
provider prose are never retained.

`turns[].domain_operations` contains bounded outcome counters for the neutral
`read`, `probe`, and `mutation` classes. `attempts` counts exact `domainctl`
occurrences in Bash input; it does not claim that shell control flow reached
each occurrence. Every attempt is classified as `success`, `tool_error`,
`invalid_result`, or `batched_unattributed`, and the counts must balance. A
success requires a non-error tool result containing the closed `domainctl`
role/result envelope for the current Agent role. Multiple operations in one
Bash call are deliberately unattributed; a wrong-role result or `|| true`
becomes invalid rather than successful. The sanitizer never retains paths,
endpoints, payloads, results, or error text.
These counters are Runtime observations: they do not assert why an external
state changed and cannot prove a business repair. The external world oracle
remains the sole evidence for that result.

`turns[].accepted_events` is an exact test-runner binding, not model-visible
authority. The runner diffs accepted local operation Events immediately before
and after one turn, retains only Event ID and digest, and requires the stopped
authority database to prove the same accepted operation. It never retains the
operation key, request digest, attachment, fence, submitted Intent, or Receipt
body. The binding labels an accepted Event with its producing Runtime turn; it
does not make the Runtime observation a cause of that Event.

This authority binding is deliberately independent from `accepted_receipts`.
The latter counts only submit traffic syntactically visible in paired Pi Bash
envelopes and can miss a valid nested shell form; an accepted Receipt can also
be an exact replay that creates no new Event. Neither observation is promoted
to the other. T0 still permits at most one newly accepted local operation Event
inside a turn boundary. The runner enforces one exclusive turn window per node;
without that exclusivity this is temporal attribution rather than a private
operation-to-turn binding and would not be sufficient.

The passed runner report is `memory.r7.domain-ops.live-report` version 7. It
contains two ordered service-world episodes and a bounded authority boundary
between them. After the lead's interactive entry and before either full business
oracle, its attention envelope
records only protocol-derived `state = 'open' AND claim_attachment_id IS NULL`
and `state = 'open' AND claim_attachment_id IS NOT NULL` counts, the bounded
neutral turns given to unclaimed Principals, and a runner-owned closed goal
observation. Its historical component is read-only. Once that component is
satisfied, the runner issues one server-named, globally bounded canary checkout
and retains both its pre-reconciliation and settled ledger observations. A
previously claimed but still-open Handling remains eligible after occupancy is
released. Any occupied claim fails closed and is preserved in separate failure
evidence. `outcome_observed` may retain open Handlings: durable responsibility is
not a workflow terminal and does not need to disappear when the external outcome
is observed. The envelope never records Event kinds, payloads, or expected
remediation, and fixed all-node rounds are not part of the schedule. A
runner-attested sequence captured after the external recovery oracle starts the
consolidation interval; the adapter independently verifies
that every reported boundary head was accepted after that sequence, exists in
the stopped Reference lineage, and is no newer than the end boundary. Every
reported later use must be an exact causation or supersede/retract edge from a
post-boundary accepted Event. It does not inspect Artifact bytes, semantic kinds, or remediation
choices. Earlier passed-report versions are intentionally not accepted as two-episode
evidence; failed reports use the same version-7 wire below.

## Failed-run input

After authority state exists, a failed live run should still stop and copy all
five stores, then write a sanitized input with this closed shape:

```json
{
  "schema": "memory.r7.domain-ops.failure-report",
  "version": 6,
  "status": "failed",
  "model": "bounded-model-token",
  "run": {
    "id": "bounded-run-token",
    "started_at": "canonical UTC RFC3339Nano",
    "finished_at": "canonical UTC RFC3339Nano",
    "candidate_digest": "sha256:..."
  },
  "failure": {
    "code": "bounded.machine-readable-code",
    "observed_at": "canonical UTC RFC3339Nano"
  },
  "world": [],
  "attention_envelope": null,
  "turns": [],
  "raw_provider_streams_retained": false
}
```

`turns` may contain only the same completed, sanitized counter records used by
the passed report. Partial text, tool input/output, prompts, and provider errors
do not belong in this file. Invoke the adapter with `--failure-report` instead
of `--report` and the same three evidence paths.

An `attention-budget-exhausted-before-outcome` failure replaces
`attention_envelope: null` with the already captured waves, the final snapshot,
and a closed false goal observation. An `attention-quiescent-without-outcome`
failure records that the goal remains false while no Principal has eligible
work. An
`attention-claim-occupied` failure does the same with a `claim_occupied`
snapshot before any goal I/O or further turn is scheduled. Goal-based objects carry only closed
episode/status tokens plus per-node `open_unclaimed` and `occupied_claims`
counts, the turn limit, turns used, wave numbers, and a digest-bound goal result.
The occupied snapshot deliberately has `goal: null`: authority safety evidence
cannot depend on a potentially failing external oracle.
The trace projects these as `test.attention.wave`, `test.attention.exhausted`,
`test.attention.quiescent`, and the distinct `test.attention.occupied` assertion
Facts; it does not retain Event kinds,
payloads, paths, commands, or a proposed remediation. The failed
`scenario.run` gate cites the final snapshot Facts.

When an external incident snapshot already exists, `world` retains at most one
five-count aggregate for each episode: charges, active and voided charges,
unique businesses, and businesses with duplicate active charges. It never
retains business IDs, receipts, paths, payloads, logs, or provider results.
These counts are runner observations for diagnosing a failed scenario; they are
not R7 facts and cannot satisfy a success gate.

Handling Facts cover both the subject changed by an Event and every successor
Handling whose durable `created_sequence` names that Event. An advance or
resolve that creates follow-up responsibility therefore emits both its
advanced/resolved Fact and separate `r7.handling.created` Facts for successors.

A failure trace preserves whatever accepted Event, target, Artifact, Handling,
Receipt, and peer Delivery chain exists in the stopped stores. Its terminal
result is always `failed`; the scenario gate is `fail`, unevaluated R7 gates are
`unknown`, and no failed run can be rendered as a passed scenario merely because
its authority databases are internally valid.
