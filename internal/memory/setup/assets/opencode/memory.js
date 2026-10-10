import { spawnSync } from "node:child_process"
import { existsSync, readFileSync, realpathSync } from "node:fs"
import { createRequire } from "node:module"
import path from "node:path"
import process from "node:process"

const MAX_RECALL_CHARS = 4000
const MAX_QUERY_CHARS = 8000
const MAX_SESSIONS = 128
const CONTEXT_MARKER = "<memory_context>"
const COMPACTION_GUIDANCE = `## Memory System

Before compaction completes, preserve durable preferences, decisions, insights, facts, or context with memory remember/link when they will improve future continuity. Do not store secrets, credentials, or short-lived operational noise.`

function npmBinary(entry) {
  // Bypass npm's JS launcher so timeout kills the native process itself. Read
  // its installed target registry instead of keeping a second platform list.
  const root = path.dirname(path.dirname(entry))
  const targets = JSON.parse(readFileSync(path.join(root, "targets.json"), "utf8"))
  const target = targets.find((item) => item.platform === process.platform && item.arch === process.arch)
  if (!target) throw new Error("Unsupported Memory platform")
  const require = createRequire(entry)
  const native = path.dirname(require.resolve(`${target.alias}/package.json`))
  return path.join(native, target.binary)
}

function memoryCommand() {
  for (const directory of (process.env.PATH || "").split(path.delimiter)) {
    if (!directory) continue
    const binary = path.join(directory, process.platform === "win32" ? "memory.exe" : "memory")
    if (existsSync(binary)) {
      const resolved = realpathSync(binary)
      return resolved.endsWith(`${path.sep}bin${path.sep}memory.js`) ? npmBinary(resolved) : binary
    }
    if (process.platform === "win32") {
      // Node cannot execute npm's .cmd shim directly. Locate the package
      // without invoking a shell or interpreting the user's recall text.
      for (const modules of [path.join(directory, "node_modules"), path.dirname(directory)]) {
        const entry = path.join(modules, "@jaywanx", "memory-cli", "bin", "memory.js")
        if (existsSync(entry)) return npmBinary(entry)
      }
    }
  }
  return "memory"
}

function runMemory(args, options = {}) {
  try {
    const proc = spawnSync(memoryCommand(), args, {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      env: process.env,
      cwd: options.cwd || process.cwd(),
      timeout: 5000,
      killSignal: "SIGKILL",
      maxBuffer: 256 * 1024,
      windowsHide: true,
    })
    if (proc.error || proc.status !== 0) return ""
    return proc.stdout.trim()
  } catch {
    return ""
  }
}

function textFromPart(part) {
  if (!part || typeof part !== "object") return ""
  if (part.type === "text" && typeof part.text === "string") return part.text
  if (typeof part.content === "string") return part.content
  return ""
}

function lastUserMessage(output, partKey) {
  const messages = Array.isArray(output?.messages) ? output.messages : []
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i]
    const role = msg?.info?.role || msg?.role
    if (role !== "user") continue
    if (!Array.isArray(msg[partKey])) continue
    return { msg, parts: msg[partKey] }
  }
  return null
}

function buildRecallContext(query, cwd) {
  const status = runMemory(["status"], { cwd })
  const recall = query.trim() === "" ? "" : runMemory(["recall", query, "--limit", "5"], { cwd })
  const sections = []
  if (status) sections.push(`Status:\n${status.slice(0, MAX_RECALL_CHARS)}`)
  if (recall) sections.push(`Relevant recall:\n${recall.slice(0, MAX_RECALL_CHARS)}`)
  sections.push("Use memory when it materially improves continuity. After responding, decide whether durable preferences, decisions, insights, facts, or context should be stored with memory remember/link.")
  return `\n\n<memory_context>\n${sections.join("\n\n")}\n</memory_context>\n\n`
}

function recallInjector(directory) {
  // OpenCode rebuilds model messages for tool continuations. Reuse the recall
  // for the latest user message, but attach it to every fresh outgoing copy.
  const sessions = new Map()
  return {
    inject(sessionID, messageID, parts) {
      const text = parts.map(textFromPart).filter(Boolean).join("\n")
      if (text.includes(CONTEXT_MARKER)) return
      const query = text.slice(0, MAX_QUERY_CHARS)
      const cached = sessionID && messageID ? sessions.get(sessionID) : undefined
      const context = cached?.messageID === messageID && cached?.query === query
        ? cached.context
        : buildRecallContext(query, directory)
      if (sessionID && messageID) {
        sessions.delete(sessionID)
        sessions.set(sessionID, { messageID, query, context })
        if (sessions.size > MAX_SESSIONS) sessions.delete(sessions.keys().next().value)
      }
      parts.unshift({ type: "text", text: context })
    },
    clear() { sessions.clear() },
  }
}

async function setup(ctx) {
  const recall = recallInjector(ctx.location.directory)
  await ctx.shell.hook("create.before", (event) => {
    event.env.MEMORY_OPENCODE = "1"
  })
  await ctx.session.hook("context", (event) => {
    const current = lastUserMessage(event, "content")
    if (current) recall.inject(event.sessionID, current.msg.id, current.parts)
  })
  await ctx.session.hook("compaction", (event) => {
    if (!event.system.some((part) => part.type === "text" && part.text === COMPACTION_GUIDANCE)) {
      event.system.push({ type: "text", text: COMPACTION_GUIDANCE })
    }
  })
  // Registrations belong to OpenCode's plugin scope. No event loop or timer is
  // needed; cap cached sessions and release their text when the plugin unloads.
  return () => recall.clear()
}

async function server({ directory, client }) {
  const recall = recallInjector(directory)
  await client?.app?.log?.({
    body: {
      service: "memory",
      level: "info",
      message: "Memory OpenCode plugin loaded",
    },
  })

  return {
    "shell.env": async (_input, output) => {
      if (!output.env) output.env = {}
      output.env.MEMORY_OPENCODE = "1"
    },

    "experimental.chat.messages.transform": async (_input, output) => {
      const current = lastUserMessage(output, "parts")
      if (!current) return
      recall.inject(current.msg.info?.sessionID, current.msg.info?.id, current.parts)
    },

    "experimental.session.compacting": async (_input, output) => {
      if (!Array.isArray(output.context)) output.context = []
      if (!output.context.includes(COMPACTION_GUIDANCE)) output.context.push(COMPACTION_GUIDANCE)
    },

    event: async ({ event }) => {
      if (event?.type !== "session.idle") return
      await client?.app?.log?.({
        body: {
          service: "memory",
          level: "info",
          message: "OpenCode session idle; evaluate whether durable memory should be written with memory",
        },
      })
    },
    dispose: () => recall.clear(),
  }
}

// One export avoids double registration. OpenCode 1.18.29+ calls server();
// OpenCode 2 calls setup() and ignores the legacy adapter.
export default { id: "memory", setup, server }
