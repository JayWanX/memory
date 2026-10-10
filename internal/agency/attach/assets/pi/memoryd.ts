import { execFile, execFileSync, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const HOOK_CUE = "memoryd state is available; read .pi/skills/memoryd/SKILL.md and use its exact Pi tools and artifact commands.";
const MAX_BOUNDARY_OUTPUT_BYTES = 4096;
const MAX_RECEIPT_OUTPUT_BYTES = (4 << 10) + 1;
const ATTACH_TIMEOUT_MS = 5000;
const SUBMIT_TIMEOUT_MS = 5000;
const SUBMIT_SHUTDOWN_GRACE_MS = 100;
const ATTACH_ATTEMPTS = 2;
const MAX_INTENT_BYTES = 12 * 1024;
const MAX_DIAGNOSTIC_BYTES = 512;
const SUBMIT_TOOL = "memoryd_submit";

const SubmitParameters = {
  type: "object",
  properties: {
    intent: {
      type: "object",
      description: "One Intent from the current View",
      additionalProperties: true,
    },
  },
  required: ["intent"],
  additionalProperties: false,
} as const;

function boundaryEnvelope(boundary: string) {
  return JSON.stringify({ boundary, schema: "memory.hook.boundary", version: 1 });
}

function runBoundary(args: string[], boundary: string): boolean {
  try {
    execFileSync("memory", ["agency", ...args], {
      input: boundaryEnvelope(boundary),
      maxBuffer: MAX_BOUNDARY_OUTPUT_BYTES,
      stdio: ["pipe", "ignore", "ignore"],
      timeout: ATTACH_TIMEOUT_MS,
    });
    return true;
  } catch {
    return false;
  }
}

function attachBoundary(boundary: string): boolean {
  for (let attempt = 0; attempt < ATTACH_ATTEMPTS; attempt += 1) {
    if (runBoundary(["hook", "attach", "--json"], boundary)) return true;
  }
  return false;
}

function endBoundary(boundary: string) { return runBoundary(["hook", "end", "--json"], boundary); }

function intentInput(value: unknown): string | undefined {
  if (value === null || typeof value !== "object" || Array.isArray(value) ||
      Object.keys(value).length === 0) return undefined;
  try {
    const encoded = JSON.stringify(value);
    if (Buffer.byteLength(encoded, "utf8") > MAX_INTENT_BYTES) return undefined;
    return encoded;
  } catch {
    return undefined;
  }
}

const INPUT_CODE = /^(invalid_argument|content_required|content_too_large|artifact_invalid|artifact_too_large)$/;

function parseOutput(stdout: string, exitStatus: unknown): {
  content: string;
  status: "settled" | "input_invalid";
} | undefined {
  if (Buffer.byteLength(stdout, "utf8") > MAX_RECEIPT_OUTPUT_BYTES ||
      !stdout.endsWith("\n") || stdout.indexOf("\n") !== stdout.length - 1) return undefined;
  const raw = stdout.slice(0, -1);
  let value: unknown;
  try { value = JSON.parse(raw); } catch { return undefined; }
  if (value === null || typeof value !== "object" || Array.isArray(value)) return undefined;
  const object = value as Record<string, unknown>;
  const keys = Object.keys(object).length;
  if (exitStatus === undefined) {
    const receipt = object;
    if (receipt.schema !== "memory.agent.receipt" || receipt.version !== 1 ||
        typeof receipt.replayed !== "boolean") return undefined;
    if (receipt.outcome === "accepted") {
      if (keys !== 4) return undefined;
    } else if (receipt.outcome !== "rejected" || keys !== 5 ||
      typeof receipt.diagnostic !== "string" || receipt.diagnostic.length === 0 ||
      Buffer.byteLength(receipt.diagnostic, "utf8") > MAX_DIAGNOSTIC_BYTES) return undefined;
    return { content: raw, status: "settled" };
  }
  if (exitStatus !== 2 || keys !== 7 || typeof object.code !== "string" ||
    !INPUT_CODE.test(object.code) ||
    typeof object.message !== "string" || object.message.length === 0 ||
    object.message.trim() !== object.message ||
    Buffer.byteLength(object.message, "utf8") > MAX_DIAGNOSTIC_BYTES ||
    object.operation_id !== null || object.replayed !== false || object.retryable !== false ||
    object.schema_version !== 1 || object.status !== "error") return undefined;
  return { content: raw, status: "input_invalid" };
}

function signalOwnedChild(child: ChildProcess, signal: NodeJS.Signals): void {
  if (child.exitCode === null && child.signalCode === null) child.kill(signal);
}

function submitIntent(encoded: string, signal: AbortSignal): Promise<{
  text: string;
  status: "settled" | "failed" | "input_invalid";
}> {
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
        SUBMIT_SHUTDOWN_GRACE_MS);
      killTimer.unref?.();
    };
    const stdinError = () => interrupt();
    const cleanup = () => {
      if (timeout !== undefined) clearTimeout(timeout);
      if (killTimer !== undefined) clearTimeout(killTimer);
      if (listeningForAbort) signal.removeEventListener("abort", interrupt);
      child.stdin?.off("error", stdinError);
    };
    child = execFile("memory", ["agency", "agent", "submit", "--json"], {
      encoding: "utf8",
      maxBuffer: MAX_RECEIPT_OUTPUT_BYTES,
      shell: false,
    }, (error, stdout, stderr) => {
      cleanup();
      if (interrupted) reject(new Error("submit interrupted"));
      else if (stderr !== "") reject(new Error("submit unavailable"));
      else {
        const parsed = parseOutput(stdout, error?.code);
        if (parsed === undefined) reject(new Error("submit unavailable"));
        else resolve({ text: parsed.content, status: parsed.status });
      }
    });
    timeout = setTimeout(interrupt, SUBMIT_TIMEOUT_MS);
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
      child.stdin.end(encoded);
    } catch {
      interrupt();
    }
  });
}

function submitResult(text: string, status: "settled" | "failed" | "input_invalid") {
  return {
    content: [{ type: "text" as const, text }],
    details: { schema: "memory.pi.effect", version: 1, status },
  };
}

export default function (pi: ExtensionAPI) {
  let activeBoundary: string | undefined;

  function releaseBoundary(): boolean {
    const boundary = activeBoundary;
    if (boundary === undefined) return true;
    if (!endBoundary(boundary)) return false;
    activeBoundary = undefined;
    return true;
  }

  pi.registerTool({
    name: SUBMIT_TOOL,
    label: "Submit memoryd Intent",
    description: "Submit one bounded Intent; only its validated Receipt reports the Effect.",
    parameters: SubmitParameters as never,

    async execute(_toolCallId, params, signal) {
      const encoded = intentInput(params?.intent);
      if (encoded === undefined) {
        return submitResult("Invalid bounded Intent object.", "input_invalid");
      }
      try {
        const result = await submitIntent(encoded, signal);
        return submitResult(result.text, result.status);
      } catch {
        return submitResult("Submit unavailable.", "failed");
      }
    },
  });

  pi.on("tool_result", async (event) => {
    if (event.toolName !== SUBMIT_TOOL) return;
    const details = event.details as
      | { schema?: unknown; version?: unknown; status?: unknown }
      | undefined;
    if (details?.schema !== "memory.pi.effect" || details.version !== 1 ||
        details.status !== "settled") return { isError: true };
  });

  pi.on("before_agent_start", async () => {
    if (!releaseBoundary()) return undefined;
    const boundary = randomBytes(32).toString("base64url");
    if (!attachBoundary(boundary)) return undefined;
    activeBoundary = boundary;
    return {
      message: {
        customType: "memoryd",
        content: HOOK_CUE,
        display: false,
      },
    };
  });

  pi.on("agent_settled", async () => {
    releaseBoundary();
  });

  pi.on("session_shutdown", async () => {
    releaseBoundary();
  });
}
