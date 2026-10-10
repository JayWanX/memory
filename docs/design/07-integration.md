# 7. LLM CLI Integration

[< Back to Design Overview](../DESIGN.md)

---

![Integration Architecture](../diagrams/08-three-layer-integration.jpg)

Memory integrates with LLM CLIs through a small runtime-native projection, not
as a runtime-specific agent framework. The target runtime remains responsible
for conversation, planning, file edits, tool use, and semantic judgment. Memory
provides a durable memory protocol, a skill surface, shared guidance, and
lightweight lifecycle reminders where the runtime supports them.

The integration layer follows the **Hook-native, LLM-led, Protocol-constrained**
principle:

- **Hook-native**: lifecycle events are useful places to remind the agent about
  memory, but hooks should stay lightweight.
- **LLM-led**: the host agent decides whether recall or writeback is useful.
- **Protocol-constrained**: Memory owns deterministic commands, structured
  output, provenance, linking, deduplication, and lifecycle operations.

## 7.1 Installed Projection

The current integration projects one shared behavioral model into the closest
native surfaces of each runtime:

| Artifact | Role |
|---|---|
| Runtime-specific `SKILL.md` | Teaches command syntax, output interpretation, and hard guardrails |
| `<prompt-dir>/guide.md` | Carries shared recall, writeback, linking, and no-op guidance; the default prompt directory is `~/.memory/prompt/` |
| Native hooks or extensions | Surface bounded reminders at lifecycle points the runtime exposes |
| `memory` binary | Executes deterministic memory operations |

`memory setup` installs these assets for known runtimes. Their paths and hook
shapes are integration details: a runtime may use shell hooks, a plugin, a
TypeScript extension, persistent instructions, or only a skill. None of these
surfaces owns Memory state.

## 7.2 Four Hook Phases

Four hook phases define the lifecycle contract:

```text
Session starts
    |
    v
  Prime   -> load skill/guide stance and active store info
    |
    v
User prompt arrives
    |
    v
  Remind  -> ask whether recall could change the task
    |
    v
Agent works with Memory only when useful
    |
    v
  Nudge   -> ask whether durable writeback is justified
    |
    v
Before context compaction
    |
    v
  Compact -> preserve only critical continuity
```

The hook contract is behavioral. The script body is runtime-specific and should
be treated as an implementation detail.

| Phase | Typical Event | Required Behavior | Should Avoid |
|---|---|---|---|
| Prime | Session start / bootstrap | Make the Memory skill, guide, and active store visible | Bulk injecting historical memory |
| Remind | User prompt submit / before planning | Prompt a recall decision for memory-sensitive tasks | Auto-recalling every prompt |
| Nudge | Stop / after response | Prompt a writeback decision for durable insights | Saving ordinary chat logs |
| Compact | Before compaction | Preserve critical continuity before context is lost | Storing the full transcript |

When hooks are unavailable, encode the same checks as persistent rules. The
agent can self-check at task start, task end, and compaction boundaries.

## 7.3 Runtime Mapping

The same integration contract maps differently across runtimes:

| Runtime | Natural Installation Mechanism |
|---|---|
| Codex | `AGENTS.md`, skills, local instructions, and hooks when enabled |
| Claude Code | `CLAUDE.md`, skills, slash commands, settings hooks, and project/user memory files |
| OpenClaw | Plugin hooks and skills, without requiring a Memory-specific memory engine |
| Pi | `AGENTS.md`, native skills, and TypeScript extension lifecycle events |
| Skill-first agents | Skills, memory guidance, and lightweight reminders |
| Minimal CLIs | A skill, rules file, or system instruction that carries the same bounded guidance |

The mappings live in runtime-specific setup code and embedded assets. They are
not separate product architectures.

## 7.4 Agent-Led Memory Work

The agent should treat memory as a decision, not a reflex:

1. At task start, decide whether prior experience could change the work.
2. If yes, run a focused `memory recall` query and treat results as evidence.
3. Do the task using current user instructions and repository facts as higher
   authority than stale memory.
4. At task end, decide whether the session produced durable knowledge.
5. If yes, write a concise memory with provenance and link/supersede related
   memories when the relationship is useful.
6. If no, do nothing.

Delegation to a sub-agent can be useful when a runtime supports it, especially
for expensive writeback review or long sessions. It is an execution strategy,
not a required part of the architecture. A single capable agent may perform the
same memory decisions directly.

## 7.5 Markdown Self-Evolution

The integration layer should evolve primarily through reviewed markdown
patches:

```text
repeated experience
  -> Memory recall/writeback evidence
  -> LLM reflection
  -> candidate patch to SKILL.md / guide.md / project rule
  -> review
  -> installed behavior
```

This keeps self-evolution inspectable and reversible. Stable workflows become
skills. Stable judgment changes become guide edits. Changes to runtime setup
remain reviewed code or embedded-asset changes. Code, database schema, or
runtime internals should evolve only after the markdown loop proves that the
behavior is valuable.

## 7.6 Verification

An integration is acceptable when the target agent can:

1. Locate the Memory skill and explain command syntax.
2. Locate the memory guide and explain recall/writeback skip conditions.
3. Run `memory recall` for a task where memory is relevant.
4. Write one durable memory with provenance.
5. Skip memory for a trivial task.
6. Preserve only critical continuity before compaction when the runtime exposes
   that lifecycle point.

The integration is failing if hooks force memory use on every prompt, if memory
turns into a transcript dump, or if stale memory overrides current user
instructions and repository evidence.
