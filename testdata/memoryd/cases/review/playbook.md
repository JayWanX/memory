# Review case

This case is a bounded, one-to-one generator--critic exchange. `kind` values
below are opaque case vocabulary. Only the listed consequences have machine
meaning.

## Actors and fixture rule

- `implementer` owns one local tracking responsibility and produces candidates.
- `reviewer` receives a separate local responsibility and checks the exact
  Artifact bytes delivered to its node.
- For this fixture, `total=42` is accepted. Any other total receives the exact
  contents of `artifacts/rework.txt`.
- At most one revision is requested.

## Event vocabulary

| Opaque kind | Closed consequence | Meaning in this case |
|---|---|---|
| `review.request` | `handling.create` | Create one local tracking Handling and one remote review request. |
| `review.rework` | `handling.resolve.declined` | Close the reviewer's first local Handling and return a correlated terminal result. |
| `review.revision` | `handling.advance` | Keep the implementer's tracking Handling open while sending a revised candidate. |
| `review.accept` | `handling.resolve.completed` | Close the reviewer's second local Handling and return verified acceptance evidence. |
| `review.adopt` | `handling.resolve.completed` | Locally adopt the observed result and close the implementer's tracking Handling. |

The nodes never share a Handling. A terminal response closes only the
reviewer's local responsibility; after receiver-local re-admission it appears
to the implementer as a zero-Handling observation. The implementer then freely
chooses rework or adoption from a fresh View. Transport acknowledgment, Runtime
exit, and remote completion never close the implementer's local Handling.

## Deterministic trace and oracle

1. `implementer` sends `candidate-v1.txt` and retains its local tracking
   Handling; `reviewer` replies `declined` with `rework.txt`.
2. `implementer` observes that result, advances the same local Handling, and
   sends `candidate-v2.txt`; `reviewer` replies `completed` with
   `acceptance.txt`.
3. `implementer` verifies and locally adopts the acceptance Artifact, then
   explicitly completes its own tracking Handling.

The case passes only when each reviewer result is a correlated terminal reply,
both returned Artifacts match exact local CAS bytes, the implementer remains
responsible until local adoption, and both independent nodes end with zero open
Handlings.
