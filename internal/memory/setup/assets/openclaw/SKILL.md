---
name: memory
description: "Persistent memory CLI for LLM agents. Store facts, recall past knowledge, link related memories, manage lifecycle."
metadata:
  openclaw:
    emoji: "🧠"
    requires:
      bins: ["memory"]
    install:
      - id: "node"
        kind: "node"
        package: "@jaywanx/memory-cli@latest"
        bins: ["memory"]
        label: "Install memory (npm)"
---

# memory

## Install & Configure

### 1. Install the binary

**npm** (macOS / Linux / Windows, Node.js 22+):

```bash
npm install --global @jaywanx/memory-cli
```

Upgrade an npm-managed installation with:

```bash
memory update
```

### 2. Set up OpenClaw integration

```bash
memory setup --target openclaw --yes
```

This single command deploys all components:
- **Skill** → `~/.openclaw/skills/memory/SKILL.md`
- **Hook** → `~/.openclaw/hooks/memory-prime/` (agent:bootstrap — injects behavioral guide)
- **Plugin** → `~/.openclaw/extensions/memory/` (remind, nudge, compact hooks)
- **Prompts** → `~/.memory/prompt/` (guide.md, skill.md)

Restart the OpenClaw gateway to activate.

### 3. Customize (optional)

Edit `~/.memory/prompt/guide.md` to tune recall/remember behavior.

Plugin hooks are configured in `~/.openclaw/openclaw.json`:

```json
{
  "plugins": {
    "entries": {
      "memory": {
        "enabled": true,
        "config": {
          "remind": true,
          "nudge": true,
          "compact": false
        }
      }
    }
  }
}
```

| Hook | Default | Description |
|------|---------|-------------|
| `remind` | on | Recall relevant memories + remind agent on each message |
| `nudge` | on | Suggest remember sub-agent after each reply |
| `compact` | off | Save key insights before context compaction |

### 4. Uninstall

```bash
memory setup --eject --target openclaw --yes
```

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

## Import Historical Chats

When the user asks to import old chats, notes, or exported context, create a
`memory_draft.json` with `schema_version: "1"`, `insights` entries containing
`content`, `category`, `importance`, `tags`, `entities`, and optional
`created_at`, plus optional `edges` using `source_index`, `target_index`,
`edge_type`, `weight`, and `reason`. Run `memory import --dry-run <file>`,
then run `memory import <file>` only after validation passes. After import,
verify with `memory status` and a focused `memory search` or `memory recall`.
Check the output `errors` field because imports can partially succeed.

## Guardrails

- Use the `exec` tool to run memory commands.
- Do not store secrets, passwords, or tokens.
- Categories: `preference` · `decision` · `insight` · `fact` · `context`
- Edge types: `temporal` · `semantic` · `causal` · `entity`
- Max 8,000 chars per insight.
