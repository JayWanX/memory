---
name: memory
description: Persistent memory CLI for Kimi Code. Store facts, recall past knowledge, link related memories, manage lifecycle.
---

# memory

## Workflow

1. **Remember**: `memory remember "<fact>" --cat <cat> --imp <1-5> --entities "e1,e2" --source agent`
   - Only exact content repeats are skipped; distinct content is stored and diff suggestions are advisory.
   - To retire a superseded memory, store and verify the new fact, then explicitly run `memory forget <old-id>`.
   - Output includes `action` (added/skipped), `semantic_candidates`, and `causal_candidates`.
2. **Link** (evaluate candidates from step 1 using judgment):
   - Review `causal_candidates`: link only when the memories are genuinely causally related.
   - Review `semantic_candidates`: high `similarity` alone is not enough; skip unrelated keyword matches.
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

- Use memory only when it can materially improve continuity or task quality.
- Do not store secrets, passwords, tokens, private keys, or short-lived operational noise.
- Categories: `preference`, `decision`, `insight`, `fact`, `context`
- Edge types: `temporal`, `semantic`, `causal`, `entity`
- Max 8,000 chars per insight.
