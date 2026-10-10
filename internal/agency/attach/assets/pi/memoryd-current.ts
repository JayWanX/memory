import { execFile, type ChildProcess } from "node:child_process";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const CURRENT_TOOL = "memoryd_current";
const CURRENT_TIMEOUT_MS = 5000;
const CURRENT_SHUTDOWN_GRACE_MS = 100;
const CURRENT_ATTEMPTS = 2;
// Agent View canonical JSON is bounded at 16 KiB; stdout adds one newline.
const MAX_CURRENT_OUTPUT_BYTES = (16 << 10) + 1;
const CURRENT_FAILED_TEXT = "Current unavailable.";

const CurrentParameters = {
  type: "object",
  properties: {},
  additionalProperties: false,
} as const;

class CurrentInterruptedError extends Error {}

function currentResult(text: string, status: "projected" | "failed") {
  return {
    content: [{ type: "text" as const, text }],
    details: { schema: "memory.pi.current", version: 1, status },
  };
}

function parseCurrentOutput(stdout: string): string | undefined {
  if (Buffer.byteLength(stdout, "utf8") > MAX_CURRENT_OUTPUT_BYTES ||
      !stdout.endsWith("\n") || stdout.indexOf("\n") !== stdout.length - 1) {
    return undefined;
  }
  const raw = stdout.slice(0, -1);
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) return undefined;
  const view = value as { schema?: unknown; version?: unknown; view?: unknown };
  if (view.schema !== "memory.agent.view" || view.version !== 8 ||
      typeof view.view !== "string" || view.view.length === 0) return undefined;
  return raw;
}

function signalOwnedChild(child: ChildProcess, signal: NodeJS.Signals): void {
  if (child.exitCode === null && child.signalCode === null) child.kill(signal);
}

function readCurrent(signal: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    let timeout: ReturnType<typeof setTimeout> | undefined;
    let killTimer: ReturnType<typeof setTimeout> | undefined;
    let interrupted = false;
    let listeningForAbort = false;
    let child: ChildProcess;
    const interrupt = () => {
      if (interrupted) return;
      interrupted = true;
      signalOwnedChild(child, "SIGTERM");
      killTimer = setTimeout(() => signalOwnedChild(child, "SIGKILL"),
        CURRENT_SHUTDOWN_GRACE_MS);
      killTimer.unref?.();
    };
    const stdinError = () => interrupt();
    const cleanup = () => {
      if (timeout !== undefined) clearTimeout(timeout);
      if (killTimer !== undefined) clearTimeout(killTimer);
      if (listeningForAbort) signal.removeEventListener("abort", interrupt);
      child.stdin?.off("error", stdinError);
    };
    child = execFile("memory", ["agency", "agent", "current", "--json"], {
      encoding: "utf8",
      maxBuffer: MAX_CURRENT_OUTPUT_BYTES,
      shell: false,
    }, (error, stdout, stderr) => {
      cleanup();
      const view = !interrupted && error === null && stderr === "" ?
        parseCurrentOutput(stdout) : undefined;
      if (interrupted) reject(new CurrentInterruptedError("current interrupted"));
      else if (view === undefined) reject(new Error("current unavailable"));
      else resolve(view);
    });
    timeout = setTimeout(interrupt, CURRENT_TIMEOUT_MS);
    timeout.unref?.();
    if (signal.aborted) interrupt();
    else {
      signal.addEventListener("abort", interrupt, { once: true });
      listeningForAbort = true;
    }
    if (child.stdin === null) {
      interrupt();
      return;
    }
    child.stdin.once("error", stdinError);
    try {
      child.stdin.end();
    } catch {
      interrupt();
    }
  });
}

async function readCurrentWithReplay(signal: AbortSignal): Promise<string> {
  for (let attempt = 0; attempt < CURRENT_ATTEMPTS; attempt += 1) {
    try {
      return await readCurrent(signal);
    } catch (error) {
      if (signal.aborted || error instanceof CurrentInterruptedError) break;
    }
  }
  throw new Error("current unavailable");
}

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: CURRENT_TOOL,
    label: "Read memoryd View",
    description: "Read the bounded current View from the attached local memoryd. This is Pi's only Current surface; do not retry through bash.",
    parameters: CurrentParameters as never,
    async execute(_toolCallId, params, signal) {
      if (params === null || typeof params !== "object" || Array.isArray(params) ||
          Object.keys(params).length !== 0) return currentResult(CURRENT_FAILED_TEXT, "failed");
      try {
        return currentResult(await readCurrentWithReplay(signal), "projected");
      } catch {
        return currentResult(CURRENT_FAILED_TEXT, "failed");
      }
    },
  });

  pi.on("tool_result", async (event) => {
    if (event.toolName !== CURRENT_TOOL) return;
    const details = event.details as
      | { schema?: unknown; version?: unknown; status?: unknown }
      | undefined;
    if (details?.schema !== "memory.pi.current" || details.version !== 1 ||
        details.status !== "projected") return { isError: true };
  });
}
