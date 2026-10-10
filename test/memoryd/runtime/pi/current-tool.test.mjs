import assert from "node:assert/strict";
import { getEventListeners } from "node:events";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

const extensionPath = process.env.MEMORY_PI_CURRENT_EXTENSION;
if (!extensionPath) throw new Error("MEMORY_PI_CURRENT_EXTENSION is required");
const { default: currentExtension } = await import(extensionPath);

const view = '{"schema":"memory.agent.view","version":8,"view":"view:test",' +
  '"outstanding":{"open_total":0,"related_total":0,"related_projected":0,' +
  '"truncated":false},"allowed_intents":[]}';

async function withFakeMemoryd(fn) {
  const directory = await mkdtemp(path.join(tmpdir(), "memory-pi-current-"));
  const executable = path.join(directory, "memory");
  const log = path.join(directory, "calls.log");
  const oldPath = process.env.PATH;
  const oldLog = process.env.MEMORY_CURRENT_LOG;
  const oldOutput = process.env.MEMORY_CURRENT_OUTPUT;
  const oldFail = process.env.MEMORY_CURRENT_FAIL;
  const oldFailOnce = process.env.MEMORY_CURRENT_FAIL_ONCE;
  const oldAttempts = process.env.MEMORY_CURRENT_ATTEMPTS;
  const oldHang = process.env.MEMORY_CURRENT_HANG;
  const oldPid = process.env.MEMORY_CURRENT_PID;
  await writeFile(executable, '#!/bin/sh\ninput=$(cat)\nattempt=0\n' +
    'test ! -f "$MEMORY_CURRENT_ATTEMPTS" || attempt=$(cat "$MEMORY_CURRENT_ATTEMPTS")\n' +
    'attempt=$((attempt + 1))\nprintf "%s\\n" "$attempt" >"$MEMORY_CURRENT_ATTEMPTS"\n' +
    'printf "%s|%s\\n" "$*" "${#input}" >>"$MEMORY_CURRENT_LOG"\n' +
    'if test "${MEMORY_CURRENT_HANG:-0}" = 1; then\n' +
    '  trap "" TERM\n' +
    '  printf "%s\\n" "$$" >"$MEMORY_CURRENT_PID"\n' +
    '  mkfifo "$MEMORY_CURRENT_PID.pipe"\n' +
    '  read ignored <"$MEMORY_CURRENT_PID.pipe"\n' +
    'fi\n' +
    'if test "${MEMORY_CURRENT_FAIL_ONCE:-0}" = 1 && test "$attempt" = 1; then exit 1; fi\n' +
    'printf "%s" "$MEMORY_CURRENT_OUTPUT"\n' +
    'test "${MEMORY_CURRENT_FAIL:-0}" != 1\n');
  await chmod(executable, 0o755);
  process.env.PATH = `${directory}:${oldPath ?? ""}`;
  process.env.MEMORY_CURRENT_LOG = log;
  process.env.MEMORY_CURRENT_ATTEMPTS = path.join(directory, "attempts");
  process.env.MEMORY_CURRENT_OUTPUT = `${view}\n`;
  delete process.env.MEMORY_CURRENT_FAIL;
  delete process.env.MEMORY_CURRENT_FAIL_ONCE;
  delete process.env.MEMORY_CURRENT_HANG;
  delete process.env.MEMORY_CURRENT_PID;
  try {
    await fn({ directory, log });
  } finally {
    if (oldPath === undefined) delete process.env.PATH;
    else process.env.PATH = oldPath;
    if (oldLog === undefined) delete process.env.MEMORY_CURRENT_LOG;
    else process.env.MEMORY_CURRENT_LOG = oldLog;
    if (oldOutput === undefined) delete process.env.MEMORY_CURRENT_OUTPUT;
    else process.env.MEMORY_CURRENT_OUTPUT = oldOutput;
    if (oldFail === undefined) delete process.env.MEMORY_CURRENT_FAIL;
    else process.env.MEMORY_CURRENT_FAIL = oldFail;
    if (oldFailOnce === undefined) delete process.env.MEMORY_CURRENT_FAIL_ONCE;
    else process.env.MEMORY_CURRENT_FAIL_ONCE = oldFailOnce;
    if (oldAttempts === undefined) delete process.env.MEMORY_CURRENT_ATTEMPTS;
    else process.env.MEMORY_CURRENT_ATTEMPTS = oldAttempts;
    if (oldHang === undefined) delete process.env.MEMORY_CURRENT_HANG;
    else process.env.MEMORY_CURRENT_HANG = oldHang;
    if (oldPid === undefined) delete process.env.MEMORY_CURRENT_PID;
    else process.env.MEMORY_CURRENT_PID = oldPid;
    await rm(directory, { recursive: true, force: true });
  }
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
  throw new Error("timed out waiting for fake Current child");
}

function assertProcessGone(pid) {
  assert.throws(() => process.kill(pid, 0), (error) => error?.code === "ESRCH");
}

function fakePi() {
  let tool;
  let toolResult;
  currentExtension({
    registerTool(value) {
      assert.equal(tool, undefined);
      tool = value;
    },
    on(name, handler) {
      assert.equal(name, "tool_result");
      assert.equal(toolResult, undefined);
      toolResult = handler;
    },
  });
  return { tool, toolResult };
}

test("native Current executes one fixed argv and returns one View v8", async () => {
  await withFakeMemoryd(async ({ log }) => {
    const runtime = fakePi();
    assert.equal(runtime.tool.name, "memoryd_current");
    assert.deepEqual(runtime.tool.parameters, {
      type: "object", properties: {}, additionalProperties: false,
    });
    const controller = new AbortController();
    const listeners = getEventListeners(controller.signal, "abort").length;
    const result = await runtime.tool.execute("current-1", {}, controller.signal);
    assert.deepEqual(result, {
      content: [{ type: "text", text: view }],
      details: { schema: "memory.pi.current", version: 1, status: "projected" },
    });
    assert.equal(await runtime.toolResult({
      toolName: "memoryd_current", details: result.details,
    }), undefined);
    assert.equal(getEventListeners(controller.signal, "abort").length, listeners);
    assert.equal(await readFile(log, "utf8"), "agency agent current --json|0\n");
  });
});

test("native Current internally replays one journaled operation after transport failure", async () => {
  await withFakeMemoryd(async ({ log }) => {
    const runtime = fakePi();
    process.env.MEMORY_CURRENT_FAIL_ONCE = "1";
    const result = await runtime.tool.execute(
      "current-replay", {}, new AbortController().signal,
    );
    assert.equal(result.details.status, "projected");
    assert.equal(result.content[0].text, view);
    assert.deepEqual((await readFile(log, "utf8")).trim().split("\n"), [
      "agency agent current --json|0",
      "agency agent current --json|0",
    ]);
  });
});

test("native Current fails closed on parameters, framing, schema, and process failure", async () => {
  await withFakeMemoryd(async ({ log }) => {
    const runtime = fakePi();
    const signal = new AbortController().signal;
    const invalidParameters = await runtime.tool.execute("current-params", { extra: true }, signal);
    assert.equal(invalidParameters.details.status, "failed");
    assert.equal(invalidParameters.content[0].text, "Current unavailable.");

    for (const output of [
      '{}\n',
      '{"schema":"memory.agent.view","version":5,"view":"view:test"}\n',
      `${view}\n${view}\n`,
      '{"schema":"memory.agent.view","version":8,"view":"view:test"\n',
      `${JSON.stringify({
        schema: "memory.agent.view", version: 8, view: "x".repeat(16 << 10),
      })}\n`,
    ]) {
      process.env.MEMORY_CURRENT_OUTPUT = output;
      const result = await runtime.tool.execute("current-invalid", {}, signal);
      assert.deepEqual(result.details,
        { schema: "memory.pi.current", version: 1, status: "failed" });
      assert.equal(result.content[0].text, "Current unavailable.");
    }

    process.env.MEMORY_CURRENT_OUTPUT = `${view}\n`;
    process.env.MEMORY_CURRENT_FAIL = "1";
    const failed = await runtime.tool.execute("current-process", {}, signal);
    assert.equal(failed.details.status, "failed");
    assert.deepEqual(await runtime.toolResult({
      toolName: "memoryd_current", details: failed.details,
    }), { isError: true });
    assert.deepEqual(await runtime.toolResult({
      toolName: "memoryd_current", details: { schema: "wrong", version: 1, status: "projected" },
    }), { isError: true });
    assert.equal((await readFile(log, "utf8")).trim().split("\n").length, 12);
  });
});

test("native Current aborts and joins a child that ignores SIGTERM", async () => {
  await withFakeMemoryd(async ({ directory }) => {
    const runtime = fakePi();
    const controller = new AbortController();
    const pidFile = path.join(directory, "abort.pid");
    process.env.MEMORY_CURRENT_HANG = "1";
    process.env.MEMORY_CURRENT_PID = pidFile;
    const listeners = getEventListeners(controller.signal, "abort").length;
    const started = Date.now();
    const pending = runtime.tool.execute("current-abort", {}, controller.signal);
    const pid = await waitForPid(pidFile);
    controller.abort();
    const result = await pending;
    assert.equal(result.details.status, "failed");
    assert.ok(Date.now() - started >= 80, "Current returned before its TERM grace elapsed");
    assert.ok(Date.now() - started < 2000, "Current abort did not remain bounded");
    assert.equal(getEventListeners(controller.signal, "abort").length, listeners);
    assertProcessGone(pid);
  });
});

test("native Current timeout escalates to SIGKILL and waits for callback completion", async () => {
  await withFakeMemoryd(async ({ directory }) => {
    const runtime = fakePi();
    const controller = new AbortController();
    const pidFile = path.join(directory, "timeout.pid");
    process.env.MEMORY_CURRENT_HANG = "1";
    process.env.MEMORY_CURRENT_PID = pidFile;
    const listeners = getEventListeners(controller.signal, "abort").length;
    const started = Date.now();
    const pending = runtime.tool.execute("current-timeout", {}, controller.signal);
    const pid = await waitForPid(pidFile);
    const result = await pending;
    const elapsed = Date.now() - started;
    assert.equal(result.details.status, "failed");
    assert.ok(elapsed >= 4900, `Current timeout fired early after ${elapsed}ms`);
    assert.ok(elapsed < 7000, `Current timeout did not remain bounded: ${elapsed}ms`);
    assert.equal(getEventListeners(controller.signal, "abort").length, listeners);
    assertProcessGone(pid);
  });
});
