# Contract Net case

This case is one bounded request-for-proposals round followed by one award.
The event `kind` values are opaque case vocabulary. memoryd neither ranks bids
nor recognizes a market; only the closed R7 consequences are authoritative.

## Actors and fixture rule

- `initiator` owns the request and keeps the local responsibility anchor.
- `bidder-a` and `bidder-b` return the exact proposal Artifacts in this
  directory.
- `bidder-c` declines without inventing a completion Artifact.
- For this fixture only, the lowest integer `cost` wins. A tie is broken by the
  lexical node name. Thus `bidder-b` must win.

## Event vocabulary

| Opaque kind | Closed consequence | Meaning in this case |
|---|---|---|
| `contract-net.request` | `handling.create` | Fan out the task Artifact to all bidders and retain `self`. |
| `contract-net.proposal` | `handling.advance` | Return one proposal to `initiator` while retaining the bidder's current Handling. |
| `contract-net.decline` | `handling.advance` | Report a decline to `initiator` without claiming completion. |
| `contract-net.award` | `handling.advance` | Send the exact award Artifact to the selected bidder. |
| `contract-net.result` | `handling.advance` | Return the selected bidder's result Artifact to `initiator`. |
| `contract-net.done` | `handling.resolve.completed` | Close a successful local responsibility with a verified result Artifact. |
| `contract-net.closed` | `handling.resolve.declined` | Close a declined or non-selected local responsibility without an Artifact. |

All remote replies use `handling.advance`, so the current Handling remains the
local anchor until an explicit terminal Intent. Proposal order has no semantic
effect. The initiator waits for the three fixture responses, interprets their
content, then selects the winner; memoryd performs no join or selection.

## Deterministic trace and oracle

1. The initiator sends `task.txt` to all three remote targets plus `self`.
2. A and B return `proposal-a.txt` and `proposal-b.txt`; C returns the semantic
   decline and closes locally as declined.
3. The initiator emits `award.txt` only to B. B returns `result.txt`.
4. Each successful contribution is completed with the exact relevant
   Artifact. Declined participation closes as declined; the initiator's local
   delivery anchors close unresolved after their remote work is durably sent.

The case passes only when three independent peer deliveries become three local
Handlings, arrival order cannot change B as the selected node, exactly one
award and one result Event are accepted, the result bytes equal `result.txt`,
decline never projects as completed, and operation replay creates no duplicate
proposal, award, result, or Handling.
