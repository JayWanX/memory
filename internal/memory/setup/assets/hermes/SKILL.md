---
name: memory
description: Persistent memory CLI for Hermes Agent. Store facts, recall past knowledge, link related memories, manage lifecycle.
---

# memory

Use `memory` when durable memory can materially improve continuity across
Hermes sessions. Hooks may inject recalled context before an LLM call, but the
agent decides what is worth storing.

## Workflow

1. Recall when prior decisions, preferences, or facts may affect the current task:
   `memory recall "<query>" --limit 10`
2. Remember only stable, reusable knowledge:
   `memory remember "<fact>" --cat <cat> --imp <1-5> --entities "e1,e2" --source agent`
3. Link related memories after reviewing candidates from `remember`:
   `memory link <id> <candidate> --type <causal|semantic> --weight <0-1>`

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
memory search "<query>" --limit 10
memory import --dry-run <file>
memory import <file>
memory forget <id>
memory related <id> --edge causal
memory gc --threshold 0.4
memory gc --keep <id>
memory status
memory log
memory store list
memory store create <name>
memory store set <name>
memory store remove <name>
```

## Guardrails

- Do not store secrets, passwords, tokens, private keys, or short-lived operational noise.
- Prefer concise insights over transcript dumps.
- Categories: `preference` · `decision` · `insight` · `fact` · `context`
- Edge types: `temporal` · `semantic` · `causal` · `entity`
- Max 8,000 chars per insight.
