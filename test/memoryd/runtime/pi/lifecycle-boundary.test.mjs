import assert from "node:assert/strict";
import { getEventListeners } from "node:events";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

const extensionPath = process.env.MEMORY_PI_EXTENSION;
if (!extensionPath) throw new Error("MEMORY_PI_EXTENSION is required");
const { default: memorydExtension } = await import(extensionPath);

const acceptedReceipt =
  '{"schema":"memory.agent.receipt","version":1,"outcome":"accepted","replayed":false}';
const rejectedReceipt =
  '{"schema":"memory.agent.receipt","version":1,"outcome":"rejected","replayed":false,"diagnostic":"stale View"}';
const invalidArgumentControl =
  '{"code":"invalid_argument","message":"Intent consequence is invalid","operation_id":null,"replayed":false,"retryable":false,"schema_version":1,"status":"error"}';
const unavailableControl =
  '{"code":"memoryd_unavailable","message":"Memory Agency local control is unavailable","operation_id":null,"replayed":false,"retryable":true,"schema_version":1,"status":"error"}';

async function withFakeMemoryd(fn) {
  const directory = await mkdtemp(path.join(tmpdir(), "memory-pi-boundary-"));
  const executable = path.join(directory, "memory");
  const log = path.join(directory, "calls.log");
  const submitInput = path.join(directory, "submit.jsonl");
  const old = new Map();
  for (const name of [
    "PATH", "MEMORY_HOOK_LOG", "MEMORY_HOOK_FAIL_ATTACH", "MEMORY_HOOK_FAIL_END",
    "MEMORY_SUBMIT_FAIL", "MEMORY_SUBMIT_HANG", "MEMORY_SUBMIT_INPUT",
    "MEMORY_SUBMIT_OUTPUT", "MEMORY_SUBMIT_PID", "MEMORY_SUBMIT_STDERR",
    "MEMORY_SUBMIT_EXIT",
  ]) old.set(name, process.env[name]);
  await writeFile(executable, `#!/bin/sh
input=$(cat)
printf '%s|%s\n' "$*" "$input" >>"$MEMORY_HOOK_LOG"
case "$*" in
  "agency hook attach --json") test "\${MEMORY_HOOK_FAIL_ATTACH:-0}" != 1 ;;
  "agency hook end --json") test "\${MEMORY_HOOK_FAIL_END:-0}" != 1 ;;
  "agency agent submit --json")
    printf '%s\n' "$input" >>"$MEMORY_SUBMIT_INPUT"
    if test "\${MEMORY_SUBMIT_HANG:-0}" = 1; then
      trap '' TERM
      printf '%s\n' "$$" >"$MEMORY_SUBMIT_PID"
      mkfifo "$MEMORY_SUBMIT_PID.pipe"
      read ignored <"$MEMORY_SUBMIT_PID.pipe"
    fi
    test -z "\${MEMORY_SUBMIT_STDERR:-}" || printf '%s' "$MEMORY_SUBMIT_STDERR" >&2
    printf '%s' "$MEMORY_SUBMIT_OUTPUT"
    test -z "\${MEMORY_SUBMIT_EXIT:-}" || exit "$MEMORY_SUBMIT_EXIT"
    test "\${MEMORY_SUBMIT_FAIL:-0}" != 1
    ;;
  *) exit 2 ;;
esac
`);
  await chmod(executable, 0o755);
  process.env.PATH = `${directory}:${old.get("PATH") ?? ""}`;
  process.env.MEMORY_HOOK_LOG = log;
  process.env.MEMORY_SUBMIT_INPUT = submitInput;
  process.env.MEMORY_SUBMIT_OUTPUT = `${acceptedReceipt}\n`;
  for (const name of [
    "MEMORY_HOOK_FAIL_ATTACH", "MEMORY_HOOK_FAIL_END", "MEMORY_SUBMIT_FAIL",
    "MEMORY_SUBMIT_HANG", "MEMORY_SUBMIT_PID", "MEMORY_SUBMIT_STDERR",
    "MEMORY_SUBMIT_EXIT",
  ]) delete process.env[name];
  try {
    await fn({ directory, log, submitInput });
  } finally {
    for (const [name, value] of old) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
    await rm(directory, { recursive: true, force: true });
  }
}

function fakePi() {
  const handlers = new Map();
  const registeredTools = new Map();
  const pi = {
    on(name, handler) {
      assert.equal(handlers.has(name), false, `duplicate ${name} handler`);
      handlers.set(name, handler);
    },
    registerTool(tool) {
      assert.equal(registeredTools.has(tool.name), false, `duplicate ${tool.name} tool`);
      registeredTools.set(tool.name, tool);
    },
  };
  memorydExtension(pi);
  return { handlers, tool: (name) => registeredTools.get(name) };
}

function parseCalls(raw) {
  return raw.trim().split("\n").filter(Boolean).map((line) => {
    const separator = line.indexOf("|");
    return { command: line.slice(0, separator), input: line.slice(separator + 1) };
  });
}

function boundary(call) {
  const envelope = JSON.parse(call.input);
  assert.deepEqual(Object.keys(envelope).sort(), ["boundary", "schema", "version"]);
  assert.equal(envelope.schema, "memory.hook.boundary");
  assert.equal(envelope.version, 1);
  assert.match(envelope.boundary, /^[A-Za-z0-9_-]{43}$/);
  return envelope.boundary;
}

async function waitForPid(pidFile) {
  const deadline = Date.now() + 1000;
  while (Date.now() < deadline) {
    try {
      return Number.parseInt((await readFile(pidFile, "utf8")).trim(), 10);
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }
  throw new Error("timed out waiting for fake Submit child");
}

function assertProcessGone(pid) {
  assert.throws(() => process.kill(pid, 0), (error) => error?.code === "ESRCH");
}

test("Pi maps settled runs to exact attach and end lifecycle boundaries", async () => {
  await withFakeMemoryd(async ({ log }) => {
    const runtime = fakePi();
    assert.deepEqual([...runtime.handlers.keys()].sort(),
      ["agent_settled", "before_agent_start", "session_shutdown", "tool_result"]);

    const first = await runtime.handlers.get("before_agent_start")({}, {});
    assert.deepEqual(first, { message: { customType: "memoryd", content:
      "memoryd state is available; read .pi/skills/memoryd/SKILL.md and use its exact Pi tools and artifact commands.",
    display: false } });
    await runtime.handlers.get("agent_settled")({}, {});
    await runtime.handlers.get("session_shutdown")({}, {});

    let calls = parseCalls(await readFile(log, "utf8"));
    assert.deepEqual(calls.map((call) => call.command), [
      "agency hook attach --json", "agency hook end --json",
    ]);
    const firstBoundary = boundary(calls[0]);
    assert.equal(boundary(calls[1]), firstBoundary);

    await runtime.handlers.get("before_agent_start")({}, {});
    await runtime.handlers.get("session_shutdown")({}, {});
    calls = parseCalls(await readFile(log, "utf8"));
    assert.deepEqual(calls.slice(2).map((call) => call.command), [
      "agency hook attach --json", "agency hook end --json",
    ]);
    const secondBoundary = boundary(calls[2]);
    assert.notEqual(secondBoundary, firstBoundary);
    assert.equal(boundary(calls[3]), secondBoundary);
  });
});

test("failed attach emits no cue and reuses one nonce for its bounded retry", async () => {
  await withFakeMemoryd(async ({ log }) => {
    const runtime = fakePi();
    process.env.MEMORY_HOOK_FAIL_ATTACH = "1";
    assert.equal(await runtime.handlers.get("before_agent_start")({}, {}), undefined);
    await runtime.handlers.get("agent_settled")({}, {});
    await runtime.handlers.get("session_shutdown")({}, {});

    const calls = parseCalls(await readFile(log, "utf8"));
    assert.deepEqual(calls.map((call) => call.command), [
      "agency hook attach --json", "agency hook attach --json",
    ]);
    assert.equal(boundary(calls[0]), boundary(calls[1]));
  });
});

test("failed end retains the exact boundary and blocks replacement attach", async () => {
  await withFakeMemoryd(async ({ log }) => {
    const runtime = fakePi();
    await runtime.handlers.get("before_agent_start")({}, {});
    process.env.MEMORY_HOOK_FAIL_END = "1";
    await runtime.handlers.get("agent_settled")({}, {});
    assert.equal(await runtime.handlers.get("before_agent_start")({}, {}), undefined);
    delete process.env.MEMORY_HOOK_FAIL_END;
    await runtime.handlers.get("session_shutdown")({}, {});

    const calls = parseCalls(await readFile(log, "utf8"));
    assert.deepEqual(calls.map((call) => call.command), [
      "agency hook attach --json", "agency hook end --json",
      "agency hook end --json", "agency hook end --json",
    ]);
    const active = boundary(calls[0]);
    assert.equal(boundary(calls[1]), active);
    assert.equal(boundary(calls[2]), active);
    assert.equal(boundary(calls[3]), active);
  });
});

test("Submit sends one bounded Intent and returns only a validated Receipt", async () => {
  await withFakeMemoryd(async ({ log, submitInput }) => {
    const runtime = fakePi();
    const submit = runtime.tool("memoryd_submit");
    assert.deepEqual(submit.parameters.required, ["intent"]);
    const intent = { kind: "opaque.signal", payload: "bounded", consequence: "handling.advance" };

    let result = await submit.execute("submit-accepted", { intent }, new AbortController().signal);
    assert.deepEqual(result, {
      content: [{ type: "text", text: acceptedReceipt }],
      details: { schema: "memory.pi.effect", version: 1, status: "settled" },
    });
    assert.equal(await runtime.handlers.get("tool_result")({
      toolName: "memoryd_submit", details: result.details,
    }), undefined);

    process.env.MEMORY_SUBMIT_OUTPUT = `${rejectedReceipt}\n`;
    result = await submit.execute("submit-rejected", { intent }, new AbortController().signal);
    assert.equal(result.details.status, "settled");
    assert.equal(result.content[0].text, rejectedReceipt);
    assert.deepEqual((await readFile(submitInput, "utf8")).trim().split("\n"), [
      JSON.stringify(intent), JSON.stringify(intent),
    ]);
    assert.deepEqual(parseCalls(await readFile(log, "utf8")).map((call) => call.command), [
      "agency agent submit --json", "agency agent submit --json",
    ]);
  });
});

test("Submit fails closed on input, framing, envelope, process, and tool-result errors", async () => {
  await withFakeMemoryd(async () => {
    const runtime = fakePi();
    const submit = runtime.tool("memoryd_submit");
    const signal = new AbortController().signal;
    const invalid = await submit.execute("invalid", { intent: {} }, signal);
    assert.equal(invalid.details.status, "input_invalid");

    for (const output of [
      "{}\n",
      '{"schema":"wrong","version":1,"outcome":"accepted","replayed":false}\n',
      `${acceptedReceipt.slice(0, -1)},"extra":true}\n`,
      '{"schema":"memory.agent.receipt","version":1,"outcome":"rejected","replayed":false}\n',
      `${acceptedReceipt}\n${acceptedReceipt}\n`,
      `${acceptedReceipt}\ntrailing`,
    ]) {
      process.env.MEMORY_SUBMIT_OUTPUT = output;
      const result = await submit.execute("malformed", {
        intent: { kind: "opaque.signal" },
      }, signal);
      assert.equal(result.details.status, "failed");
      assert.equal(result.content[0].text, "Submit unavailable.");
    }

    process.env.MEMORY_SUBMIT_OUTPUT = `${acceptedReceipt}\n`;
    process.env.MEMORY_SUBMIT_STDERR = "unexpected";
    let failed = await submit.execute("stderr", { intent: { kind: "opaque.signal" } }, signal);
    assert.equal(failed.details.status, "failed");
    delete process.env.MEMORY_SUBMIT_STDERR;
    process.env.MEMORY_SUBMIT_FAIL = "1";
    failed = await submit.execute("process", { intent: { kind: "opaque.signal" } }, signal);
    assert.equal(failed.details.status, "failed");

    assert.deepEqual(await runtime.handlers.get("tool_result")({
      toolName: "memoryd_submit",
      details: { schema: "memory.pi.effect", version: 1, status: "failed" },
    }), { isError: true });
    assert.deepEqual(await runtime.handlers.get("tool_result")({
      toolName: "memoryd_submit", details: { schema: "wrong", version: 1, status: "settled" },
    }), { isError: true });
  });
});

test("Submit projects only exact input control errors for bounded correction", async () => {
  await withFakeMemoryd(async () => {
    const runtime = fakePi();
    const submit = runtime.tool("memoryd_submit");
    const signal = new AbortController().signal;

    process.env.MEMORY_SUBMIT_OUTPUT = `${invalidArgumentControl}\n`;
    process.env.MEMORY_SUBMIT_EXIT = "2";
    let result = await submit.execute("invalid-intent", {
      intent: { kind: "opaque.signal" },
    }, signal);
    assert.deepEqual(result, {
      content: [{ type: "text", text: invalidArgumentControl }],
      details: { schema: "memory.pi.effect", version: 1, status: "input_invalid" },
    });

    process.env.MEMORY_SUBMIT_OUTPUT = `${unavailableControl}\n`;
    process.env.MEMORY_SUBMIT_EXIT = "5";
    result = await submit.execute("unavailable", {
      intent: { kind: "opaque.signal" },
    }, signal);
    assert.deepEqual(result, {
      content: [{ type: "text", text: "Submit unavailable." }],
      details: { schema: "memory.pi.effect", version: 1, status: "failed" },
    });

    for (const [output, exitStatus] of [
      [`${invalidArgumentControl.slice(0, -1)},"extra":true}\n`, "2"],
      [`${invalidArgumentControl.replace('"retryable":false', '"retryable":true')}\n`, "2"],
      [`${invalidArgumentControl.replace("Intent consequence is invalid", "x".repeat(513))}\n`, "2"],
      [`${invalidArgumentControl}\n`, "3"],
      [`${invalidArgumentControl}\n`, ""],
      [`${invalidArgumentControl}\n${invalidArgumentControl}\n`, "2"],
    ]) {
      process.env.MEMORY_SUBMIT_OUTPUT = output;
      process.env.MEMORY_SUBMIT_EXIT = exitStatus;
      result = await submit.execute("untrusted-control", {
        intent: { kind: "opaque.signal" },
      }, signal);
      assert.deepEqual(result, {
        content: [{ type: "text", text: "Submit unavailable." }],
        details: { schema: "memory.pi.effect", version: 1, status: "failed" },
      });
    }
  });
});

test("Submit abort joins a child that ignores SIGTERM", async () => {
  await withFakeMemoryd(async ({ directory }) => {
    const runtime = fakePi();
    const submit = runtime.tool("memoryd_submit");
    const controller = new AbortController();
    const pidFile = path.join(directory, "submit.pid");
    process.env.MEMORY_SUBMIT_HANG = "1";
    process.env.MEMORY_SUBMIT_PID = pidFile;
    const listeners = getEventListeners(controller.signal, "abort").length;
    const started = Date.now();
    const pending = submit.execute("abort", { intent: { kind: "opaque.signal" } }, controller.signal);
    const pid = await waitForPid(pidFile);
    controller.abort();
    const result = await pending;
    assert.equal(result.details.status, "failed");
    assert.ok(Date.now() - started >= 80, "Submit returned before its TERM grace elapsed");
    assert.ok(Date.now() - started < 2000, "Submit abort did not remain bounded");
    assert.equal(getEventListeners(controller.signal, "abort").length, listeners);
    assertProcessGone(pid);
  });
});

test("Submit timeout escalates to SIGKILL and waits for callback completion", async () => {
  await withFakeMemoryd(async ({ directory }) => {
    const runtime = fakePi();
    const submit = runtime.tool("memoryd_submit");
    const controller = new AbortController();
    const pidFile = path.join(directory, "timeout.pid");
    process.env.MEMORY_SUBMIT_HANG = "1";
    process.env.MEMORY_SUBMIT_PID = pidFile;
    const started = Date.now();
    const pending = submit.execute("timeout", { intent: { kind: "opaque.signal" } }, controller.signal);
    const pid = await waitForPid(pidFile);
    const result = await pending;
    const elapsed = Date.now() - started;
    assert.equal(result.details.status, "failed");
    assert.ok(elapsed >= 4900, `Submit timeout fired early after ${elapsed}ms`);
    assert.ok(elapsed < 7000, `Submit timeout did not remain bounded: ${elapsed}ms`);
    assertProcessGone(pid);
  });
});
