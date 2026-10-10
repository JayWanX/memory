# Evidence Blackboard case

This case is an asynchronous evidence exchange with no shared Blackboard
database. Each node sees only locally admitted Events, verified Artifacts, and
local Reference heads. The `kind` values are opaque case vocabulary; memoryd
does not interpret claims, conflicts, or resolutions.

## Actors and fixture rule

- `evidence` publishes the local Reference key `blackboard.signal` with
  `finding-v1.txt`, then opens work for all peers while retaining `self`.
- `challenger` returns `challenge.txt` to the other actors.
- `evidence` reacts by superseding the exact v1 head with `finding-v2.txt` and
  sends that Artifact to the peers. Receiving peers use the exact version for
  this request; they do not adopt it as a local Reference implicitly.
- `verifier` returns `verification.txt`.
- `resolver` emits `resolution.txt` only after seeing the challenge, revised
  finding, and verification.

## Event vocabulary

| Opaque kind | Closed consequence | Meaning in this case |
|---|---|---|
| `blackboard.finding.publish` | `reference.publish` | Create the first local finding head with one verified Artifact. |
| `blackboard.finding.supersede` | `reference.supersede` | Replace the exact offered head with the revised finding. |
| `blackboard.observe` | `handling.create` | Fan out a referenced evidence Artifact and retain `self`. |
| `blackboard.challenge` | `handling.advance` | Send the contradiction Artifact to peers while retaining the current local Handling. |
| `blackboard.revision` | `handling.advance` | Send the revised finding Artifact without mutating any remote Reference. |
| `blackboard.verify` | `handling.advance` | Send the verification Artifact to the resolver and origin. |
| `blackboard.resolve` | `handling.advance` | Return the resolution Artifact to the origin. |
| `blackboard.done` | `handling.resolve.completed` | Close successful local responsibility with a verified Artifact. |
| `blackboard.unresolved` | `handling.resolve.unresolved` | Close honestly when the bounded evidence remains contradictory. |

Remote-directed root actions include `self`; remote replies use
`handling.advance`. No remote Event changes `blackboard.signal`. Only the
evidence node's separate, local `reference.supersede` Intent may move its head,
using the exact View-offered v1 head. A stale competing head mutation must fail
closed rather than overwrite the winner.

## Deterministic trace and oracle

Peer deliveries may arrive in any order, but the bounded fixture result is
fixed. The public case oracle requires:

- the evidence node's active Reference points to `finding-v2.txt`, and an old
  View's v1 head cannot authorize another mutation;
- the challenger, verifier, and resolver obtain every consumed Artifact from
  their own CAS after local peer re-admission;
- conflict content is preserved explicitly and never treated as authority;
- a stale supersede of the v1 head is rejected;
- the resolver's accepted result bytes equal `resolution.txt` exactly;
- only explicit completed Intents with verified Artifacts project completion;
  and
- delivery or operation replay creates no duplicate local Event, Handling, or
  Reference mutation.

The independent P-08 Core conformance oracle, rather than this public Agent
surface case, proves that v1 remains in immutable lineage after supersession.
