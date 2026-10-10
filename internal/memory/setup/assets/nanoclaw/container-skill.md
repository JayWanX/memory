---
name: memory
description: Persistent graph-based memory. Recall context before responding, remember insights after. Each group has private memory; global memory is read-only.
---

# memory — Persistent Memory

## Memory Stores

- **Private** (default): Per-group, read-write. All writes go here.
- **Global**: Shared across all groups, read-only. Use `--store global --readonly` to query.

## Recall — before responding

**Default: recall on every new user message**, unless ALL of these apply:
- Direct follow-up within a topic already fully in context
- No reference to past sessions, decisions, or preferences
- No knowledge dependency beyond the current conversation

To recall:
```bash
memory recall "<query>" --limit 5
# Also check shared knowledge:
memory recall "<query>" --store global --readonly --limit 5
```

Craft a focused, keyword-rich query — do not pass the raw user prompt.

## Remember — after responding

Run this decision tree after every substantive response:

**Step 1 — Does this exchange contain any of these?**
  a) User directive — preference, decision, correction, explicit "remember this"
  b) Reasoning conclusion — non-trivial judgment from multi-source synthesis
  c) Durable observed state — system fact, environment detail, architectural finding
  → No to all → STOP.

**Step 2 — Does a highly overlapping memory already exist?**
  → Yes, incremental new info → STORE the new content; review the existing memory separately
  → Yes, but contradicts/supersedes → STORE and verify the new fact; explicitly forget the old ID only if superseded
  → No significant overlap → CREATE

**Step 3 — Is it worth storing?**
  Rebuilding from scratch costs more than storing + recalling?
  - Single-query public facts → No
  - Multi-source synthesis with non-obvious conclusions → Yes
  - User-specific context no search engine can recover → Yes
  → No → STOP.

**What to store**: conclusions and user-specific context, not raw facts.

## Workflow

1. **Remember**: `memory remember "<fact>" --cat <cat> --imp <1-5> --entities "e1,e2" --source agent`
   - Only exact content repeats are skipped; distinct content is stored and diff suggestions are advisory.
   - To retire a superseded memory, store and verify the new fact, then explicitly run `memory forget <old-id>`.
   - Output includes `action` (added/skipped), `semantic_candidates`, `causal_candidates`.
2. **Link** (evaluate candidates from step 1 — use judgment, not mechanical rules):
   - Review `causal_candidates`: does a genuine cause-effect relationship exist? `causal_signal` is regex-based and prone to false positives — only link if the memories are truly causally related.
   - Review `semantic_candidates`: are these memories meaningfully related? High `similarity` alone is not sufficient — skip candidates that share keywords but discuss unrelated topics.
   - Syntax: `memory link <id> <candidate> --type <causal|semantic> --weight <0-1> [--meta '<json>']`
3. **Recall**: `memory recall "<query>" --limit 10`

## Recall Intent

Keep focused queries and memories in their original language. When the user's
meaning is clear, choose `--intent WHY` (reasons), `--intent WHEN` (timing),
`--intent ENTITY` (what/who), or `--intent GENERAL` (neutral retrieval).
For example: `memory recall "<query>" --intent WHY`. The override works in any
language; `--verbose` reports
`meta.intent` and `meta.intent_source` (`auto` or `override`).

Automatic cues cover some forms in English, Mandarin Chinese (simplified and
traditional), Hindi (Devanagari), Spanish, Modern Standard Arabic, French,
Bengali (Bengali script), Portuguese, Indonesian (Latin script), Russian
(Cyrillic), and German. Unrecognized forms use GENERAL; conflicting cues involving
additional languages also use GENERAL. English/Chinese-only scoring is preserved.
This is a lexical heuristic, not full language understanding. See
[the supported forms, script variants, and limits](https://github.com/JayWanX/memory/blob/master/docs/USAGE.md#recall-intent-detection).

## Commands

```bash
memory remember "<fact>" --cat <cat> --imp <1-5> --entities "e1,e2" --source agent
memory link <id1> <id2> --type <type> --weight <0-1> [--meta '<json>']
memory recall "<query>" --limit 10
memory recall "<query>" --store global --readonly --limit 10
memory search "<query>" --limit 10
memory forget <id>
memory related <id> --edge causal
memory gc --threshold 0.4
memory gc --keep <id>
memory status
memory log
```

## Guardrails

- Do not store secrets, passwords, or tokens.
- Never write to the global store — it is mounted read-only.
- Categories: `preference` · `decision` · `insight` · `fact` · `context`
- Edge types: `temporal` · `semantic` · `causal` · `entity`
- Max 8,000 chars per insight.
