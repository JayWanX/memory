# Memory test observer

This directory contains a local-only, static evidence viewer for memoryd test
runs. It is deliberately outside `daemon`, `authority`, and `peerlink`:
displaying a record must never create or alter an R7 fact.

Open `index.html` directly in a browser, then load one or more
`memory.test.trace` v2 JSONL files with the file picker or drag-and-drop surface.
No server, package installation, build, network access, or browser storage is
required. The synthetic `.trace` file under `fixtures/` exercises the R7
collaboration views without masquerading as a generated run transcript. The
browser validates the closed record shapes, order, bounds,
backward-only causes, gate references, and exact SHA-256 footer before it
renders anything. A malformed or unverifiable file is rejected rather than
shown as partial evidence.

## What the four views mean

1. **Run integrity** shows the terminal trace status and explicit test gates.
   Missing evidence is `unknown` or `incomplete`, never an inferred pass.
2. **Agent turns** places runtime observations in `(node, Agent)` lanes and
   keeps node-local machine effects in separate machine lanes, without treating
   model output as authority.
3. **Event causality** draws only recorded `causes` edges. Wall-clock order is
   not used to invent cross-node causality.
4. **Collaboration evidence** shows case-neutral connected components formed
   only by explicit backward `causes`. Standalone Runtime observations remain
   in the Agent-turn view, so they cannot consume the bounded collaboration
   component budget. Components with cross-node causes and accepted effects
   are displayed first. Bare Event, correlation, Delivery, Handling, and
   Reference tokens are annotations, not cross-node identity edges. The view
   does not assume a request/review/result workflow or invent missing stages.
   Open semantic kinds and bounded targets are displayed as labels. Terminal
   Handlings, Reference changes, and later Artifact reads are listed separately
   with their recorded outcome and state.
The observer is not an oracle. Test runners and independent validators produce
gate outcomes; the page only renders them.

Large traces remain valid inputs, but every visual surface has a fixed rendering
budget. When a lane, graph, component list, or observation list
is truncated, the page says so explicitly and renders an exact sequence prefix.
Truncation never changes trace integrity or the reported test result.

## Trace contract

The Go `Writer` and strict decoder are the single closed definition of one
JSONL trace. A complete file has exactly this shape:

```text
run header
fact 1
fact 2
...
fact N
result footer
```

Facts use a contiguous, runner-local `seq`. The observer recognizes cross-node
causality only through explicit backward `causes`, never through timestamps or
bare Event, correlation, Delivery, Handling, or Reference labels. Those labels
remain visible as annotations.
Every fact declares one evidence class:

| `truth` | Meaning |
|---|---|
| `observation` | Runtime, transport, or runner observation; not authority |
| `accepted_local_fact` | A fact committed by one local R7 authority |
| `derived_projection` | A bounded view derived from committed state |
| `assertion` | Independent test-oracle result |

The Go validator closes the relation between each known `kind`, its allowed
`source.class`, and its `truth` class. A runtime or transport observation cannot
rename itself as an accepted R7 effect; `r7.delivery.readmitted` is authored by
the receiving local authority after re-admission, not by transport. Kinds
required by the visual evidence contract also carry their minimum display
fields: accepted Events name their semantic kind and consequence, and resolved
Handlings carry an outcome. Attention snapshots report the exact authority predicates
`open_unclaimed` and `occupied_claims`. Goal-based final assertions bind the
goal projection digest and observed result: an outcome may retain open
responsibility but cannot retain an occupied claim, exhaustion and quiescence
cannot claim goal satisfaction, and quiescence additionally proves that no open
work remains on that node. The dedicated `test.attention.occupied` kind carries
no goal observation: it preserves the authority safety boundary before any
external goal I/O can hide it, without mislabeling the claim as a live Runtime.

The result footer covers the exact preceding JSONL bytes, including their line
terminators, with `trace_digest`. Its `record_count` counts only `fact` lines.
A missing footer, sequence gap, dangling trace cause, duplicate ID, count
mismatch, or digest mismatch makes a trace incomplete.

## Mandatory redaction

The trace is metadata-only. A conforming trace must not contain:

- prompts, messages, transcripts, model reasoning, or chain of thought;
- shell commands, command arguments, environment contents, or tool results;
- credentials, API keys, attachment credentials, private keys, or signatures;
- private operation keys, Artifact bytes, or unrestricted semantic payloads.

Allowed content is limited to bounded labels, stable references and digests,
counts, closed state names, timestamps, and protocol outcomes. Artifact content
remains in CAS and is represented only by digest, size, and structural role.

The HTML never inserts trace data as markup. Every untrusted label is assigned
through `textContent`; the page has no CDN, external asset, network request,
dynamic code evaluation, or persistence. If a future packer embeds a trace in
a self-contained report, it must use bounded base64 rather than placing JSON
inside a script element.

## Integration boundary

Runners may sanitize a runtime's temporary JSON stream into this format before
destroying the raw stream. A test-only, read-only exporter may snapshot durable
R7 objects after a run and validate their canonical bytes. None of these paths may:

- add a daemon debug endpoint;
- write an authority store;
- run in the admission transaction;
- make trace loss change a system fact;
- infer a successful gate from absent evidence.

Trace capture failure makes the test report `incomplete`. It does not roll back
or manufacture Event, Handling, Reference, Receipt, or Delivery state.

Test-only exporters can use the small Go `Writer` in this package after they
have independently sanitized their source evidence. The caller supplies a
typed run header, typed metadata-only facts with explicit backward causes, and
terminal gate results. The writer assigns contiguous sequence numbers, rejects
dangling or duplicate causes, enforces the observer bounds and evidence
classification, and writes the deterministic SHA-256 footer. It deliberately
does not read authority databases, parse Runtime transcripts, infer causality,
or understand scenario-specific Event kinds.

## Validation

Run the focused observer checks with:

```sh
go test ./test/memoryd/observer
```

The deterministic memoryd test sweep includes this package. The observer is a
diagnostic surface, not a protocol oracle and not a second evidence authority.
The checks validate strict JSONL decoding, bounds,
redaction, trace linkage, footer digests, fixtures, Content Security Policy,
and the absence of external resources or markup injection APIs.
