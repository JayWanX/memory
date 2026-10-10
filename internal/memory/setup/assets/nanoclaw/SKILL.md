---
name: add-memory
description: Add persistent graph-based memory to NanoClaw agents using memory. Agents recall context before responding and remember insights after. Each group gets isolated memory with optional global shared store.
---

# /add-memory

Add [memory](https://github.com/JayWanX/memory) persistent memory to your NanoClaw installation. After running this skill, every agent session will have access to a per-group memory graph that persists across conversations.

## Architecture

```
Host                              Container
~/.memory/data/{group}/ ──rw──→ /home/node/.memory/data/default/  (private)
~/.memory/data/global/  ──ro──→ /home/node/.memory/data/global/   (shared)
```

Each group gets its own isolated memory store. An optional global store provides shared read-only knowledge across all groups.

---

## Phase 1: Pre-flight

1. Verify memory is installed on the host:
   ```bash
   memory --version
   ```
   If not installed:
   - **Recommended (npm)**: `npm install --global @jaywanx/memory-cli`
   - **Alternative (Homebrew)**: `brew install --cask JayWanX/tap/memory`

2. Verify the container image exists:
   ```bash
   docker image inspect nanoclaw-agent:latest >/dev/null 2>&1 && echo "OK"
   ```

3. Fetch the latest memory version for the Dockerfile:
   ```bash
   curl -s https://api.github.com/repos/JayWanX/memory/releases/latest | grep -o '"tag_name": "v[^"]*"' | cut -d'"' -f4 | sed 's/^v//'
   ```

---

## Phase 2: Apply Code Changes

### 2a. Install memory in the container image

**File**: `container/Dockerfile`

Add the following block **after** the `apt-get install` section and **before** the `npm install -g` line. Replace `0.1.1` with the version from Phase 1 step 3:

```dockerfile
# Install memory for persistent agent memory
ARG MEMORY_VERSION=0.1.1
RUN ARCH=$(dpkg --print-architecture) && \
    curl -fsSL "https://github.com/JayWanX/memory/releases/download/v${MEMORY_VERSION}/memory_${MEMORY_VERSION}_linux_${ARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin memory && \
    chmod +x /usr/local/bin/memory
```

This works for both `amd64` and `arm64` architectures.

### 2b. Add the container skill

**File**: `container/skills/memory/SKILL.md`

Create this file with the memory container skill content. This skill teaches the agent inside the container when and how to use memory. It should include:

- **Memory stores section**: Explain that the default store is per-group (private, read-write) and the global store is shared (read-only, accessed via `--store global --readonly`).
- **Recall guide**: Default recall on every new user message. Use `memory recall "<query>" --limit 5`. Also check the global store: `memory recall "<query>" --store global --readonly --limit 5`. Craft focused keyword-rich queries.
- **Remember guide**: Decision tree — Step 1: Does this exchange contain a user directive, reasoning conclusion, or durable observed state? Step 2: Does a memory already exist (create/update/skip)? Step 3: Is it worth storing?
- **Workflow**: remember → link (evaluate semantic/causal candidates with judgment) → recall.
- **Commands**: Full memory command reference (remember, link, recall, search, forget, related, gc, status, log).
- **Guardrails**: Never store secrets. Never write to the global store. Categories: preference, decision, insight, fact, context. Max 8,000 chars per insight.

### 2c. Add volume mounts for memory data

**File**: `src/container-runner.ts`

In the function that builds volume mounts (where the existing group folder and Claude session mounts are defined), add two new mounts **after** the Claude sessions mount:

```typescript
// Per-group memory memory store (private, read-write)
const groupMemoryDir = path.join(homedir(), '.memory', 'data', group.folder);
fs.mkdirSync(groupMemoryDir, { recursive: true });
mounts.push({
  hostPath: groupMemoryDir,
  containerPath: '/home/node/.memory/data/default',
  readonly: false,
});

// Global shared memory memory (read-only, optional)
const globalMemoryDir = path.join(homedir(), '.memory', 'data', 'global');
if (fs.existsSync(globalMemoryDir)) {
  mounts.push({
    hostPath: globalMemoryDir,
    containerPath: '/home/node/.memory/data/global',
    readonly: true,
  });
}
```

Adapt the mount syntax to match the existing pattern in `container-runner.ts` (it may use string format like `hostPath:containerPath:ro` or an object format — match whichever the file uses).

**Important**: The `mkdirSync` call ensures the per-group memory directory exists on the host before the container starts, preventing mount failures.

### 2d. Add lifecycle hook scripts

Create `container/hooks/memory/` with four shell scripts. These run inside the container at Claude Code lifecycle events to actively drive memory operations.

**File**: `container/hooks/memory/prime.sh`

```bash
#!/bin/bash
# memory SessionStart hook — report memory stats on session init.
STATS=$(memory status 2>/dev/null)
if [ -n "$STATS" ]; then
  INSIGHTS=$(echo "$STATS" | sed -n 's/.*"total_insights": *\([0-9]*\).*/\1/p' | head -1)
  EDGES=$(echo "$STATS" | sed -n 's/.*"edge_count": *\([0-9]*\).*/\1/p' | head -1)
  echo "[memory] Memory active (${INSIGHTS:-0} insights, ${EDGES:-0} edges)."
else
  echo "[memory] Memory active."
fi
```

**File**: `container/hooks/memory/user_prompt.sh`

```bash
#!/bin/bash
# memory UserPromptSubmit hook — remind agent to evaluate recall/remember.
echo "[memory] Evaluate: recall needed? After responding, evaluate: remember needed?"
```

**File**: `container/hooks/memory/stop.sh`

```bash
#!/bin/bash
# memory Stop hook — remind agent to consider remember after responding.
INPUT=$(cat)
MSG=$(echo "$INPUT" | jq -r '.last_assistant_message // ""' 2>/dev/null)
if echo "$MSG" | grep -qiE "memory remember|sub-agent.*remember|Stored.*imp="; then
  exit 0
fi
echo "[memory] Consider: does this exchange warrant a remember sub-agent?"
```

**File**: `container/hooks/memory/compact.sh`

```bash
#!/bin/bash
# memory PreCompact hook — save key insights before context compaction.
echo "[memory] Context compaction starting. Review this session and remember the most valuable insights (up to 5) before context is compressed. Delegate to Task sub-agents now."
```

Make all scripts executable: `chmod +x container/hooks/memory/*.sh`

### 2e. Copy hooks into container and register in settings.json

**File**: `container/Dockerfile`

Add after the memory binary install block:

```dockerfile
# Copy memory hook scripts
COPY hooks/memory/ /app/hooks/memory/
RUN chmod +x /app/hooks/memory/*.sh
```

**File**: `src/container-runner.ts`

In the block where `settings.json` is created for each group session (look for `writeFileSync` with `settings.json`), merge memory hooks into the settings object:

```typescript
// Register memory lifecycle hooks
const memoryHooks = {
  SessionStart: [{
    hooks: [{ type: 'command', command: '/app/hooks/memory/prime.sh' }]
  }],
  UserPromptSubmit: [{
    hooks: [{ type: 'command', command: '/app/hooks/memory/user_prompt.sh' }]
  }],
  Stop: [{
    hooks: [{ type: 'command', command: '/app/hooks/memory/stop.sh' }]
  }],
  PreCompact: [{
    hooks: [{ type: 'command', command: '/app/hooks/memory/compact.sh' }]
  }],
};

// Merge into existing settings.hooks (preserve any existing hooks)
const existingHooks = settings.hooks || {};
for (const [event, entries] of Object.entries(memoryHooks)) {
  existingHooks[event] = [...(existingHooks[event] || []), ...entries];
}
settings.hooks = existingHooks;
```

Adapt this to match the existing settings.json construction pattern in `container-runner.ts`.

---

## Phase 3: Setup

1. Initialize the global shared store on the host (optional — skip if you don't need cross-group shared memory):
   ```bash
   memory store create global
   ```

2. Rebuild the container image:
   ```bash
   ./container/build.sh
   ```

3. Restart the NanoClaw service:
   ```bash
   # macOS with launchd:
   launchctl kickstart -k "gui/$(id -u)/com.nanoclaw"
   # Or manually:
   npm run dev
   ```

---

## Phase 4: Verify

1. Verify memory works inside the container:
   ```bash
   docker run --rm --entrypoint memory nanoclaw-agent:latest --version
   ```

2. Verify memory status inside a running container:
   ```bash
   docker run --rm --entrypoint memory nanoclaw-agent:latest status
   ```

3. Send a test message to the WhatsApp bot and check that the agent mentions memory operations in its reasoning.

4. Verify data persistence on the host:
   ```bash
   ls ~/.memory/data/
   # Should show directories for each active group
   ```

---

## Removal

To remove memory from your NanoClaw installation:

1. Remove from Dockerfile: delete the `ARG MEMORY_VERSION` + `RUN ... memory` block and the `COPY hooks/memory/` line
2. Remove container skill: `rm -rf container/skills/memory/`
3. Remove hook scripts: `rm -rf container/hooks/memory/`
4. Remove volume mounts from `src/container-runner.ts`: delete the memory mount blocks
5. Remove hooks registration from `src/container-runner.ts`: delete the memory hooks merge in settings.json
6. Rebuild: `./container/build.sh`
7. (Optional) Remove data: `rm -rf ~/.memory/data/`

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
