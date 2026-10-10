import assert from "node:assert/strict"
import { spawnSync } from "node:child_process"
import { copyFile, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { test } from "node:test"

for (const npm of [false, true]) {
  test(`timeout kills a SIGTERM-ignoring ${npm ? "npm" : "native"} command`, { skip: process.platform === "win32" }, async () => {
    const root = await mkdtemp(path.join(os.tmpdir(), "memory-opencode-timeout-"))
    const bin = path.join(root, "bin")
    const pidFile = path.join(root, "pid")
    try {
      await mkdir(bin)
      let native = path.join(bin, "memory")
      if (npm) {
        const modules = path.join(root, "node_modules")
        const pkg = path.join(modules, "@jaywanx", "memory-cli")
        const runtime = path.join(modules, "fixture-runtime")
        await mkdir(path.join(pkg, "bin"), { recursive: true })
        await mkdir(runtime, { recursive: true })
        await writeFile(path.join(pkg, "targets.json"), JSON.stringify([
          { platform: process.platform, arch: process.arch, alias: "fixture-runtime", binary: "memory" },
        ]))
        await writeFile(path.join(runtime, "package.json"), '{"name":"fixture-runtime"}')
        const entry = path.join(pkg, "bin", "memory.js")
        await writeFile(entry, 'throw new Error("must run the native child directly")')
        await symlink(entry, native)
        native = path.join(runtime, "memory")
      }
      await writeFile(native, `#!/usr/bin/env node
if (process.argv[2] === "status") { console.log("{}"); process.exit(0) }
process.on("SIGTERM", () => {})
require("node:fs").writeFileSync(process.env.MEMORY_TEST_PID, String(process.pid))
setInterval(() => {}, 1000)
`, { mode: 0o755 })
      await copyFile(new URL("../../../internal/memory/setup/assets/opencode/memory.js", import.meta.url), path.join(root, "plugin.mjs"))
      await writeFile(path.join(root, "worker.mjs"), `import plugin from "./plugin.mjs"
import { writeFileSync } from "node:fs"
let context
await plugin.setup({ location: { directory: process.cwd() }, shell: { hook: async () => {} }, session: { hook: async (name, fn) => { if (name === "context") context = fn } } })
const event = { sessionID: "s", messages: [{ id: "u", role: "user", content: [{ type: "text", text: "query" }] }] }
await context(event)
writeFileSync("result.json", JSON.stringify(event))
`)
      const started = Date.now()
      // Ignored stdio keeps this outer deadline independent of inherited pipes
      // if a broken plugin leaves the fixture process running.
      const child = spawnSync(process.execPath, [path.join(root, "worker.mjs")], {
        cwd: root,
        env: { ...process.env, PATH: bin + path.delimiter + process.env.PATH, MEMORY_TEST_PID: pidFile },
        stdio: "ignore", timeout: 9000, killSignal: "SIGKILL",
      })
      assert.equal(child.status, 0, `plugin exceeded its deadline: ${child.error}`)
      assert.ok(Date.now() - started < 8000, "five-second timeout must remain bounded")
      const result = JSON.parse(await readFile(path.join(root, "result.json"), "utf8"))
      assert.match(result.messages[0].content[0].text, /<memory_context>/)
      const pid = Number(await readFile(pidFile, "utf8"))
      assert.throws(() => process.kill(pid, 0), { code: "ESRCH" }, "native child must be reaped")
    } finally {
      try { process.kill(Number(await readFile(pidFile, "utf8")), "SIGKILL") } catch {}
      await rm(root, { recursive: true, force: true })
    }
  })
}
