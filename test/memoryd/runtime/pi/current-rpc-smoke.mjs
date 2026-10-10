import { spawn } from "node:child_process";

const child = spawn("pi", [
  "--mode", "rpc", "--no-session", "--no-extensions",
  "-e", "/current-test/memoryd-current.ts",
  "-e", "/current-test/current-rpc-provider.ts",
  "--provider", "memory-current-oracle", "--model", "current-oracle",
  "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
  "--tools", "memoryd_current", "--no-approve",
], { env: process.env, stdio: ["pipe", "pipe", "pipe"] });

let output = "";
let pending = "";
let stderr = "";
let settled = false;
const timeout = setTimeout(() => child.kill("SIGKILL"), 10_000);

child.stdout.on("data", (chunk) => {
  const text = chunk.toString("utf8");
  output += text;
  pending += text;
  const lines = pending.split("\n");
  pending = lines.pop() ?? "";
  for (const line of lines) {
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      continue;
    }
    if (event.type === "agent_settled" && !settled) {
      settled = true;
      child.stdin.end();
    }
  }
});
child.stderr.on("data", (chunk) => {
  if (Buffer.byteLength(stderr, "utf8") < 16 * 1024) stderr += chunk.toString("utf8");
});
child.stdin.on("error", () => {
  // Process completion below owns the RPC outcome.
});

child.stdin.write(JSON.stringify({
  id: "current", type: "prompt", message: "Observe current.",
}) + "\n");

const result = await new Promise((resolve) => {
  child.once("error", () => resolve({ code: null, signal: null }));
  child.once("close", (code, signal) => resolve({ code, signal }));
});
clearTimeout(timeout);
if (!settled || result.code !== 0 || result.signal !== null) {
  throw new Error(`Pi RPC Current smoke failed: ${JSON.stringify({ result, settled, stderr })}`);
}
process.stdout.write(output);
