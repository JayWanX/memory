import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import path from "node:path"
import { mock, test } from "node:test"

const source = await readFile(new URL("../../../internal/memory/setup/assets/opencode/memory.js", import.meta.url), "utf8")
let generation = 0

async function loadPlugin(t, { platform = "linux", files = [], env = {}, links = {} } = {}) {
  const calls = []
  let result = (args) => args[0] === "status" ? "status fixture" : `recall fixture: ${args[1]}`
  mock.module("node:child_process", { namedExports: { spawnSync(command, args, options) {
    calls.push({ command, args, options })
    return { status: 0, stdout: result(args) }
  } } })
  const paths = platform === "win32" ? path.win32 : path.posix
  const nativeRoot = platform === "win32" ? "C:\\native" : "/native"
  const binary = platform === "win32" ? "bin/memory.exe" : "bin/memory"
  mock.module("node:fs", { namedExports: {
    existsSync: (file) => files.includes(file),
    realpathSync: (file) => links[file] || file,
    readFileSync: () => JSON.stringify([{ platform, arch: "x64", alias: "@jaywanx/fixture", binary }]),
  } })
  mock.module("node:module", { namedExports: { createRequire: () => ({ resolve: (target) => {
    assert.equal(target, "@jaywanx/fixture/package.json")
    return paths.join(nativeRoot, "package.json")
  } }) } })
  mock.module("node:path", { defaultExport: platform === "win32" ? path.win32 : path.posix })
  mock.module("node:process", { defaultExport: { platform, arch: "x64", env, cwd: () => "/fallback" } })
  t.after(() => mock.restoreAll())
  const module = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}#${++generation}`)
  assert.deepEqual(Object.keys(module), ["default"], "only one loader entrypoint")
  const plugin = module.default
  assert.equal(plugin.id, "memory")
  assert.equal(typeof plugin.server, "function")
  const hooks = new Map()
  const register = (domain) => async (name, callback) => {
    const key = `${domain}.${name}`
    assert.equal(hooks.has(key), false, `duplicate hook ${key}`)
    hooks.set(key, callback)
    return { dispose: async () => hooks.delete(key) }
  }
  const cleanup = await plugin.setup({ location: { directory: "/project" }, session: { hook: register("session") }, shell: { hook: register("shell") } })
  assert.deepEqual([...hooks.keys()], ["shell.create.before", "session.context", "session.compaction"])
  return { plugin, hooks, calls, cleanup, setResult(fn) { result = fn } }
}

function context(sessionID = "session-a", id = "user-a", text = "Orion release color") {
  return { sessionID, system: [], messages: [
    { id: "older", role: "user", content: [{ type: "text", text: "previous prompt" }] },
    { id, role: "user", content: [{ type: "image", url: "fixture" }, { type: "text", text }], metadata: { kept: true } },
    { id: "assistant", role: "assistant", content: [{ type: "text", text: "tool continuation" }] },
  ] }
}

test("v2 attaches once to the last user message and reuses recall for fresh continuation copies", async (t) => {
  const p = await loadPlugin(t)
  const hook = p.hooks.get("session.context")
  const event = context()
  hook(event)
  const current = event.messages[1]
  assert.match(current.content[0].text, /recall fixture: Orion release color/)
  assert.equal(current.content.length, 3)
  assert.deepEqual(current.metadata, { kept: true })
  assert.equal(event.messages[0].content.length, 1)
  assert.equal(event.messages[2].content.length, 1)
  hook(event)
  assert.equal(current.content.length, 3)
  const fresh = context()
  hook(fresh)
  assert.equal(fresh.messages[1].content[0].text, current.content[0].text)
  assert.equal(p.calls.length, 2, "one status and recall per user turn")
  assert.deepEqual(p.calls[1].args, ["recall", "Orion release color", "--limit", "5"])
  assert.equal(p.calls[0].options.cwd, "/project")
  assert.equal(p.calls[0].options.timeout, 5000)
  assert.equal(p.calls[0].options.killSignal, "SIGKILL")
  assert.equal(p.calls[0].options.maxBuffer, 256 * 1024)
  assert.equal(p.calls[0].options.windowsHide, true)
  assert.equal(p.calls[0].options.shell, undefined)
  hook(context("session-b", "user-a"))
  hook(context("session-a", "user-b", "next query"))
  assert.equal(p.calls.length, 6, "sessions and new user messages get their own recall")
  await p.cleanup()
  hook(context("session-a", "user-b", "next query"))
  assert.equal(p.calls.length, 8, "unload releases cached text")
})

test("recall cache and CLI input/output are bounded", async (t) => {
  const p = await loadPlugin(t)
  const hook = p.hooks.get("session.context")
  for (let i = 0; i < 129; i++) hook(context(`session-${i}`))
  hook(context("session-128"))
  assert.equal(p.calls.length, 258)
  hook(context("session-0"))
  assert.equal(p.calls.length, 260, "oldest session is evicted")
  p.setResult(() => "x".repeat(10000))
  const event = context("long", "long", "q".repeat(20000))
  hook(event)
  assert.equal(p.calls.at(-1).args[1].length, 8000)
  assert.ok(event.messages[1].content[0].text.length < 8500)
})

test("v2 shell and compaction hooks use native event shapes", async (t) => {
  const p = await loadPlugin(t)
  const shell = { env: { EXISTING: "keep" } }
  p.hooks.get("shell.create.before")(shell)
  assert.deepEqual(shell.env, { EXISTING: "keep", MEMORY_OPENCODE: "1" })
  const event = { system: [{ type: "text", text: "keep system" }], messages: [] }
  const hook = p.hooks.get("session.compaction")
  hook(event); hook(event)
  assert.equal(event.system.length, 2)
  assert.equal(event.system[1].type, "text")
  assert.match(event.system[1].text, /memory remember\/link/)
  assert.match(event.system[1].text, /Do not store secrets/)
})

test("v1 server adapter preserves legacy hooks and message parts", async (t) => {
  const p = await loadPlugin(t)
  const hooks = await p.plugin.server({ directory: "/legacy" })
  const output = { messages: [{ info: { role: "user", id: "u1", sessionID: "s1" }, parts: [{ type: "text", text: "legacy prompt" }] }] }
  await hooks["experimental.chat.messages.transform"]({}, output)
  await hooks["experimental.chat.messages.transform"]({}, output)
  assert.equal(output.messages[0].parts.length, 2)
  assert.match(output.messages[0].parts[0].text, /recall fixture: legacy prompt/)
  assert.equal(p.calls[0].options.cwd, "/legacy")
  const shell = {}
  await hooks["shell.env"]({}, shell)
  assert.equal(shell.env.MEMORY_OPENCODE, "1")
  const compaction = {}
  await hooks["experimental.session.compacting"]({}, compaction)
  await hooks["experimental.session.compacting"]({}, compaction)
  assert.equal(compaction.context.length, 1)
  assert.match(compaction.context[0], /memory remember\/link/)
  await hooks.dispose()
})

test("missing CLI and malformed messages do not break the model request", async (t) => {
  const p = await loadPlugin(t)
  const hook = p.hooks.get("session.context")
  p.setResult(() => { throw new Error("ENOENT") })
  for (const messages of [undefined, [], [{ role: "assistant", content: [] }], [{ role: "user" }]]) hook({ messages })
  assert.equal(p.calls.length, 0)
  const event = context()
  hook(event)
  assert.match(event.messages[1].content[0].text, /Use memory/)
  assert.doesNotMatch(event.messages[1].content[0].text, /Status:|Relevant recall:/)
})

for (const [name, entry, command] of [
  ["native", "C:\\bin\\memory.exe", "C:\\bin\\memory.exe"],
  ["npm global", "C:\\bin\\node_modules\\@jaywanx\\memory\\bin\\memory.js", "C:\\native\\bin\\memory.exe"],
  ["npm local", "C:\\project\\node_modules\\@jaywanx\\memory\\bin\\memory.js", "C:\\native\\bin\\memory.exe"],
]) {
  test(`Windows ${name} keeps recall text out of shell syntax`, async (t) => {
    const p = await loadPlugin(t, { platform: "win32", files: [entry], env: { PATH: "C:\\bin;C:\\project\\node_modules\\.bin" } })
    const query = 'Orion "quote" & echo %TOKEN% $(command)'
    p.hooks.get("session.context")(context("s", "u", query))
    assert.equal(p.calls[1].command, command)
    assert.deepEqual(p.calls[1].args, ["recall", query, "--limit", "5"])
    assert.equal(p.calls[1].options.shell, undefined)
  })
}

test("Unix npm symlinks resolve to the native child without an intermediate launcher", async (t) => {
  const p = await loadPlugin(t, { files: ["/bin/memory"], env: { PATH: "/bin" }, links: { "/bin/memory": "/npm/@jaywanx/memory-cli/bin/memory.js" } })
  p.hooks.get("session.context")(context())
  assert.equal(p.calls[0].command, "/native/bin/memory")
})
