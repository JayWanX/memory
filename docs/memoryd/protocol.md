# memoryd protocol

> **Stability: Preview.** This is the current authority contract implemented by
> Agency, not a long-term compatibility promise. Protocol fields, persistence,
> peer exchange, and Runtime projections may evolve until Agency declares an
> explicit stability milestone.

This document defines the small product contract behind Memory Agency. It is
an architecture boundary, not an Event-sourcing requirement and not a catalog
of built-in collaboration patterns.

## Purpose

`memoryd` gives a short-lived Agent turn a bounded view of durable local
responsibility and admits the effects the Agent proposes. It does not plan the
Agent's work, schedule its tools, or synchronize another node's state.

The protocol has one local loop:

```text
local authority -> View -> Intent -> admission -> Event + effect + Receipt
       ^                                                        |
       `----------------------- next View ----------------------'
```

The model owns the open semantic choice. The local authority owns identity,
offered handles, limits, routing, fences, persistence, and whether an effect
was accepted.

The loop is logical, not multiple actions in one model turn. An accepted
Receipt ends the current governed Host opportunity; a later eligible boundary
obtains the next View. A bounded input diagnostic is a control result, not a
Receipt.

## Core objects

| Object | Meaning | Owner |
|---|---|---|
| **View** | Bounded projection of the local world and the effects currently available to the Agent | Derived by local authority |
| **Intent** | Bounded semantic proposal selected from one exact View | Agent |
| **Event** | Immutable communicative act created only after local admission accepts an Intent or authenticated remote candidate | Local authority |
| **Receipt** | Durable accepted/rejected result for one exact operation; replay returns that prior outcome without a second effect | Local authority |
| **Handling** | Durable local responsibility that a Principal still needs to consider | Local authority only |
| **Reference** | Locally accepted persistent lineage with a CAS head and no owner, claim, or completion state; an active head points to an Artifact, while a retracted head remains as a tombstone | Local authority only |
| **Artifact** | Immutable content addressed and verified by digest; Events carry references, not the content bytes | Artifact store plus local authority catalog |

`Handling`, `Reference`, and `Artifact` may appear in a View, but a View is not
their canonical storage. Rendering `view.md` or JSON differently must not
change admission.

## Event boundary

An Event is appropriate when an accepted action must cross a turn, process,
Runtime, Principal, or node boundary, or when its causality and result must be
recoverable after the producing Agent disappears.

Queries, View rendering, prompt assembly, indexing, caches, transport ACKs,
claim maintenance, and private model reasoning are not Events.

Every Event separates three kinds of data:

```text
machine     identity, accepted time, closed consequence, resolved targets
semantic    opaque bounded kind and natural-language payload
evidence    Artifact digests, causation, and correlation
```

Semantic kind names are open. Durable consequences are closed. Natural
language can explain or recommend an action but cannot manufacture identity,
authority, routing, completion, or persistence outcomes.

## Local responsibility, not Agent state

`memoryd` records whether a Handling is open or terminal and whether a claim is
currently valid. It does not persist `agent.status = reviewing` or a workflow
step for the model. A claim is temporary occupancy; expiry releases occupancy
without declaring the responsibility complete.

The Agent sees the current Handling and relevant notes in its View, then freely
chooses the next offered Intent. New collaboration patterns are expressed by
semantic Event kinds and guides, not new Agent state machines in Core.

## Cross-node handoff

A cross-node handoff is a pair of local responsibility loops, not an atomic
move of one Handling:

```text
Node A                                         Node B

View A
  -> Intent(request)
  -> local admission
     + Event(request)
     + Handling A: wait for and assess B
                      |
                      | bounded delivery
                      v
                 authenticated candidate
                   -> local admission
                   -> Handling B: consider the request
                   -> View B
                   -> Intent(result / decline / unresolved)
                   -> Event + Artifact references
                      |
                      v
Node A receives candidate
  -> local re-admission
  -> View A'
  -> Intent(adopt / rework / decline)
  -> local Receipt and settlement of Handling A
```

The two nodes never share a canonical Task or Handling. Consequently:

1. transport delivery is not remote admission;
2. remote admission is not business completion;
3. a remote result is not local adoption;
4. a remote Event becomes local fact only through receiver-local admission;
5. network retry is at least once, while semantic effect is idempotent by
   operation identity and digest.

## Package ownership

```text
internal/agency           immutable protocol values and canonical projections
internal/agency/authority sealed View, Intent binding, admission, Handling and Reference state
internal/agency/artifact  immutable content bytes and digest verification
internal/agency/peerlink  replaceable authenticated transport only
internal/agency/client    Runtime-facing local terminal and replay journal
internal/agency/attach    host Hook, guide, and tool projection
internal/daemon           process composition and lifecycle
```

`internal/agency/authority` is the only durable fact writer. Runtime adapters and
transport may present candidates or observations but do not own protocol
state. `internal/agency` validates immutable values; policy that resolves View
handles or decides durable consequences belongs to `internal/agency/authority`.

## Capability boundary

Memory, teamwork, review, negotiation, and self-evolution are capabilities on
top of this protocol. They may add:

- bounded View projections;
- semantic Event kinds;
- Agent guides and examples;
- deterministic providers that do not create a second authority.

They must not make Core decide what knowledge is valuable, which Agent should
win a debate, how a Runtime should plan, or which collaboration pattern the
model must follow. A capability that requires a new canonical consequence must
be reviewed as an authority change, not loaded as data.
