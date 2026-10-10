# Go Engineering Standard

- **Status:** project-wide engineering contract
- **Applies to:** hand-written Go code in both the Memory and Agency product
  surfaces
- **Priority:** correctness and safety > clarity > simplicity > reuse > source-line reduction

This document defines how Memory uses Go to keep long-lived code understandable,
changeable, and verifiable. The keywords **MUST**, **SHOULD**, and **MAY** are
normative.

Unless a stricter product contract sets an absolute convergence gate, these
rules govern new and meaningfully modified code. Untouched legacy debt is
adopted through a scoped ratchet; that migration rule never excuses a known
correctness, security, data-loss, or release-isolation defect.

The standard does not require a quota of design patterns, generics, channels, or
callbacks. An abstraction is valuable only when it reduces at least one of:

- independent sources of truth;
- valid and invalid state combinations;
- places that must change for one feature;
- ownership ambiguity around state, goroutines, or resources.

Total lines of code are evidence, not a quality objective. A smaller diff that
hides authority, ordering, or failure semantics is worse than explicit code.

## 1. Structured authority and natural-language content

Memory crosses boundaries between deterministic code and language models. Those
responsibilities MUST remain distinct:

- Memory's deterministic layer—Go runtime plus reviewed schema assets—owns
  identities, kinds, revisions, timestamps, membership, state transitions,
  limits, digests, signatures, fences, and persistence outcomes.
- A model may select a closed verb or bounded alias and provide bounded natural
  language. Model-produced JSON remains untrusted content unless deterministic
  code validates and reconstructs every authoritative field.
- Natural-language content MUST be carried as opaque, bounded data. It MUST NOT
  be parsed into implicit capability, trust, routing, ranking, or lifecycle
  authority unless a separately reviewed product contract explicitly requires
  that behavior.
- A structured Event or record MUST name one canonical closed-set authority.
  Depending on the product contract, that authority may be a tracked schema
  asset or a typed Go definition. CLI, wire, storage, rendering, and test
  projections MUST be generated from it or exhaustively parity-validated
  against it; independent unchecked discriminator lists are prohibited.

This split applies to every new Event-like design, not only to the current
Agency protocol.

## 2. Package and ownership design

- A package MUST have one coherent reason to change and a documented dependency
  direction. Command packages parse, call a service, and render; they do not own
  domain policy.
- Mutable state MUST have one identifiable owner. Other components interact
  through narrow methods, immutable snapshots, or messages with explicit
  completion semantics.
- Interfaces SHOULD be small and declared by the consuming package. Do not add
  provider-wide interfaces merely to make every implementation look alike.
- Domain-specific invariants MUST remain visible at the domain boundary. Do not
  introduce a generic repository, ORM, dependency-injection container, or
  reflection-based validator that erases Event, Work, Artifact, identity, or
  authorization semantics.
- A proposed abstraction SHOULD be rejected when a new feature would still
  require editing the old branches plus the abstraction. That increases change
  amplification instead of reducing it.

For a closed extension point, the expected shape is normally one typed
descriptor, one handler or policy, and focused tests. If adding a kind still
requires synchronized edits to several switches, maps, schemas, and renderers,
the review MUST either consolidate the source or explain why the explicit
dispatch is safer.

## 3. Choosing control flow and patterns

Clear `if` and `switch` statements are idiomatic Go. Guard clauses, error
checks, bounds, digest comparisons, fence checks, CAS cardinality checks, and
fail-closed decisions MUST remain explicit. Removing `if` is not a goal.

A pattern SHOULD be introduced when a protocol is repeated, an extension point
is closed, or two copies have already drifted semantically. Typical mappings
are:

| Repeated problem | Preferred shape |
|---|---|
| durable transition with common fencing and different effects | template function with named strategy steps |
| closed Action/Event/frame/route set | immutable typed descriptor registry |
| long-lived goroutines with shared shutdown | supervisor with explicit component specs |
| external system or platform variants | narrow adapter owned by the consumer |
| prepare, external I/O, fenced settlement | explicit saga/compensation protocol |
| repeated typed framing or HTTP/JSON mechanics | generic codec or endpoint shell |
| large repeated test setup | builder plus domain-specific matchers |

Patterns MUST NOT hide transaction order, callback ownership, blocking behavior,
or domain vocabulary. Prefer a small concrete function over a framework when
the behavior is local and unlikely to vary.

## 4. Go feature boundaries

### 4.1 Channels, goroutines, and `select`

- Every goroutine MUST have an owner, a cancellation path, bounded work, a way
  to wait for exit, and a defined responsibility for closing channels and other
  resources. Fire-and-forget goroutines are prohibited in production paths.
- Channels SHOULD express handoff, bounded concurrency, backpressure, or
  lifecycle signals. They SHOULD NOT replace a database transaction or serialize
  state that already has a stronger durable owner.
- Queue capacity, overflow behavior, and shutdown behavior MUST be explicit.
  Unbounded in-memory work queues are prohibited.
- `select` MUST NOT rely on pseudo-random ready-case selection to express
  priority. Priority requires an explicit scheduling rule.
- A send or receive that can outlive its caller MUST also observe cancellation
  or a bounded deadline.

### 4.2 Locks, atomics, and shared maps

- A normal map protected by `sync.Mutex` or `sync.RWMutex` is the default for
  shared mutable state. The protected fields and lock ordering MUST be
  reviewable.
- Do not perform network, filesystem, database, channel, or unknown callback
  work while holding a lock unless the invariant and bounded blocking behavior
  are documented and tested.
- Do not block on a channel while holding a lock.
- `sync.Map` is reserved for key-independent workloads whose invariants do not
  span entries. Atomics are reserved for independently explainable state or
  immutable-snapshot publication; they are not a substitute for a state
  machine.

### 4.3 Generics

Generics SHOULD remove mechanical duplication while preserving domain types.
Appropriate examples include typed transaction shells, row collection, frame
codecs, endpoint clients, bounded parallel iteration, and test helpers.

Generics MUST NOT create an `any`-shaped authority layer, generic domain
repository, reflection-heavy schema, or type hierarchy that obscures concrete
failure behavior. If callers immediately switch on a type parameter's runtime
shape, a concrete API is usually clearer.

### 4.4 Functions and callbacks

- Function values are appropriate for named strategy steps, adapters, visitors,
  and test seams.
- A callback contract MUST state whether it may block, return an error, re-enter
  the caller, or run inside a lock or transaction.
- Unknown callbacks MUST NOT run while a lock or transaction is held.
- Avoid bags of anonymous functions that make the real state machine visible
  only at construction time. Give important steps domain names and concrete
  types.

### 4.5 Maps as registries

- Closed registries MUST be constructed once and treated as immutable.
- Registration MUST reject duplicate keys and tests MUST prove completeness,
  uniqueness, and deterministic projection.
- Order-sensitive behavior MUST sort explicit keys; it MUST NOT depend on Go map
  iteration order.

## 5. Durable state and external I/O

Durable transitions SHOULD share a narrow mechanical shell while retaining
explicit domain decisions. The reviewable order is normally:

```text
validate request
begin transaction
load authoritative state
classify fresh / replay / stale / conflict
apply fenced CAS and require the expected cardinality
write receipt, provenance, pins, and derived obligations
commit
```

- Replay, stale fence, digest conflict, and `RowsAffected == 1` checks MUST NOT
  be hidden behind a success-shaped generic helper.
- External I/O MUST NOT occur inside a database transaction. Use an explicit
  `prepare -> I/O -> fenced commit` protocol, including durable retry or
  compensation where required.
- Retried work MUST carry an immutable identity and digest. A retry may recover
  a prior result; it must not silently reinterpret changed input.
- Time, randomness, and filesystem/process effects SHOULD enter through narrow
  injectable ports when deterministic tests need control.

## 6. Runtime composition

Long-lived components SHOULD be owned by one supervisor that defines:

- construction order and readiness;
- context cancellation and first-error propagation;
- per-component resource budgets;
- reverse-order shutdown and wait behavior;
- whether restart is forbidden, local, or process-wide.

Adding a worker SHOULD add a component descriptor and its tests, not another
ad-hoc `go func`, private done channel, and bespoke shutdown branch. A supervisor
does not own durable business policy; it owns process lifecycle.

## 7. Errors, observability, and security

- Errors MUST preserve a stable category where callers make a decision and add
  local context with `%w` where appropriate.
- Logs are diagnostic evidence, not state. A logged transition is incomplete
  until its durable receipt or terminal outcome commits.
- Secrets MUST NOT enter logs, command arguments, metrics labels, or error
  strings. Natural-language input may use arguments only where an existing,
  reviewed local-user CLI contract explicitly defines that surface, as the root
  `memory` CLI does. New Agent/remote surfaces SHOULD use bounded stdin or files,
  MUST NOT forward content through child-process arguments, and MUST NOT copy
  unrestricted content into logs, metrics labels, or error strings. Other
  user-local CLI diagnostics SHOULD avoid content and must follow their reviewed
  privacy/debug contract.
- Retry loops require a bounded attempt or deadline, cancellation, and an
  observable terminal state. Do not use silent infinite retry.
- `panic` is reserved for impossible programmer/configuration errors during
  construction, not malformed user, peer, disk, or network input.

## 8. Tests and change discipline

- A behavior change MUST carry a focused test in the same logical commit.
- Protocol documents describe required behavior; ordinary tests directly
  assert its observable consequences. Do not introduce a second manifest that
  binds prose, test symbols, commands, and commit history merely to certify the
  tests themselves.
- A behavior-preserving refactor SHOULD begin with characterization tests when
  the current invariant is not already executable.
- Test builders MAY compress setup. They MUST NOT merge independent replay,
  stale-fence, restart, corruption, authorization, or race oracles merely to
  reduce test lines.
- Concurrent lifecycle changes MUST run the race detector. Codec, parser, path,
  frame, and canonicalization boundaries SHOULD add fuzz tests when malformed
  input is a meaningful risk.
- Prefer real SQLite and real boundary formats for persistence contracts. Mocks
  belong at true external ports and must not replace the transaction oracle.
- Feature work and broad behavior-neutral refactoring SHOULD be separate logical
  commits. Each commit remains buildable, reviewable, and revertible.

## 9. Review thresholds

The following values are review signals for new or meaningfully rewritten
production code. They help reviewers find unclear ownership or excessive
change amplification; they are not a separate source of architectural truth.

For new or meaningfully rewritten production code:

| Signal | Preferred | New-code violation |
|---|---:|---:|
| cyclomatic complexity per function | <= 15 | > 20; never introduce > 30 |
| cognitive complexity per function | <= 20 | > 25 |
| logical function length | <= 80 lines | > 80 without an exact exception |
| statements per function | <= 50 | > 50 without an exact exception |
| control-flow nesting | <= 4 | > 4 without an exact exception |
| normalized duplicate block | none | >= 150 tokens |

Large files and functions SHOULD be split when that creates clearer ownership,
not to satisfy a line-count score. Security guards, protocol stages, and
independent failure oracles do not count as wasteful duplication. If a future
analyzer materially improves review, it must remain a direct check with one
configuration source; it must not grow a parallel evidence ledger.

## 10. Executable gates

The repository exposes three independently invoked test levels:

```sh
make test              # deterministic CI: unit, projection, and static architecture
make test-integration  # opt-in CLI E2E, timing, race, process, and Docker boundaries
make test-live         # explicit paid Pi/DeepSeek evaluation
```

Regular CI runs only `make test` and must not depend on real daemon readiness,
wall-clock scenario outcomes, Docker, or a provider. Integration deliberately
rechecks the complete Agency surface under race, process, and Docker
boundaries; paid provider evaluation remains a separate explicit gate. Focused
packages may be invoked directly during development, but do not add another
umbrella target.

Any added analyzer MUST have a repository-owned version and configuration and
be reproducible in CI. Do not depend on a developer's global tool version or
`@latest`.

## 11. Review checklist

Before accepting a Go change, answer:

1. Is there one authoritative owner for each new state and structured kind?
2. Does the abstraction reduce sources, state combinations, or modification
   points without hiding guards and failure order?
3. Does every goroutine have owner, cancellation, bounded work, and wait?
4. Are locks, callbacks, channel operations, and external I/O ordered safely?
5. Are replay, stale fence, digest conflict, CAS cardinality, and durable
   evidence explicit where applicable?
6. Can a new kind, route, frame, or worker be added through one typed extension
   point with completeness tests?
7. Did the change preserve independent failure oracles and run the proportional
   build, test, race, and static gates?
8. Did the change avoid introducing a second test registry, evidence ledger, or
   duplicate orchestration path?
