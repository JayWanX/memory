# Memory — Design & Architecture

> **Memory** (/ˈniːmɒn/), from Ancient Greek μνήμων (memory), formed by μνάομαι ("to remember") and the agent suffix -μων, meaning "one who remembers, a person of good memory." Homer uses "καὶ γὰρ μνήμων εἰμί" ("I remember it well") in the *Odyssey* to describe this quality. In the city-states of Ancient Greece, Memoryes were officials dedicated to record-keeping, serving as witnesses and archivists in property transactions and legal proceedings — institutional memory carriers during the transition from oral tradition to written records.
>
> The word shares its root with Mnemosyne (Μνημοσύνη), the goddess of memory — from her union with Zeus the nine Muses were born, symbolizing memory as the wellspring of all knowledge and creativity.

Memory is a persistent memory system designed for LLM agents. It adopts the **LLM-Supervised** pattern: the host LLM acts as external orchestrator of a standalone memory binary through symbolic CLI interfaces, while the binary handles deterministic storage, graph indexing, and lifecycle management. Memory is organized as a four-graph knowledge structure with temporal, entity, causal, and semantic edges. The `memory` memory engine remains one Go binary + SQLite, with no external API dependencies.

This document describes the Memory engine. The same `memory` executable also
provides [Memory Agency](AGENCY.md), an optional local authority for durable
Agent work and peer collaboration. Memory and Agency keep separate state and
authority even though they share one executable.

---

## Table of Contents

### [1. Vision & Problem](design/01-vision.md)

Why Memory exists — the amnesia problem in LLM agents, structural bottlenecks of traditional approaches, and a comparison with existing solutions (Mem0, MemGPT, Claude Code Memory).

### [2. Engine Design Philosophy](design/02-philosophy.md)

The current engine's LLM-Supervised pattern, Hook-native / LLM-led / Protocol-constrained principle, Organs vs Textbooks metaphor, Memory Gateway protocol (the MCP analogy for LLM↔DB interaction), key design insights, and theoretical foundations from RLM, MAGMA, and Graph-LLM structural analysis.

### [3. Core Concepts & Architecture](design/03-concepts.md)

The Insight/Edge data model, database schema (SQLite WAL), single-executable
Memory/Agency boundary, current package structure, and Memory isolation through
named stores.

### [4. Graph Model & Structural Theory](design/04-graph-model.md)

MAGMA four-graph model (temporal, entity, causal, semantic), structural isomorphism between LLM attention and graph storage, the Extract→Candidate→Associate paradigm, read-write symmetry, `remember/link/recall` as universal algebra, the LLM↔DB protocol gap, and academic positioning.

### [5. Read & Write Pipelines](design/05-pipelines.md)

The write pipeline (`remember` with built-in diff), read pipeline (Smart Recall with intent detection, RRF anchor fusion, Beam Search traversal, multi-factor re-ranking), and deduplication/conflict detection.

### [6. Lifecycle & Embedding](design/06-lifecycle.md)

Effective Importance (EI) decay formula, immunity rules, auto-pruning, GC commands, and optional embedding support via Ollama (nomic-embed-text).

### [7. LLM CLI Integration](design/07-integration.md)

Runtime-native integration through runtime-specific `SKILL.md`, a shared
`guide.md`, supported hooks or extensions, agent-led memory decisions, setup
automation, and lightweight reviewed markdown evolution.

### [Memory Agency Preview](AGENCY.md)

How to add durable work, receipts, and optional peer collaboration to an
existing Agent Runtime, including its current maturity and compatibility boundary.

### [8. Design Decisions & Future Direction](design/08-decisions.md)

Key trade-offs (LLM-Supervised vs embedded, SQLite WAL vs graph DB, Beam Search vs BFS, soft delete), deviations from the MAGMA paper, storage-side pluggability roadmap, and the vision toward a memory gateway.
