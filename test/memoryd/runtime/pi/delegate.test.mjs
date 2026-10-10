import assert from "node:assert/strict";
import { readdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { createDelegateExtension } from "./delegate.ts";
import {
	DELEGATE_LIMITS,
	DelegateRuntimeError,
	runDelegateWithInvocation,
} from "./delegate-runtime.mjs";

const directory = path.dirname(fileURLToPath(import.meta.url));
const fakeChild = path.join(directory, "fake-child.mjs");
const secret = "test-only-secret-value";

function invocation(mode) {
	return { command: process.execPath, args: [fakeChild, mode] };
}

async function withTempParent(fn) {
	const parent = await mkdtemp(path.join(tmpdir(), "memory-pi-delegate-test-"));
	try {
		return await fn(parent);
	} finally {
		await rm(parent, { recursive: true, force: true });
	}
}

function options(tempParent, extra = {}) {
	return {
		task: "Review this evidence without taking action.",
		provider: "deepseek",
		model: "deepseek-v4-flash",
		thinkingLevel: "off",
		apiKey: secret,
		tempParent,
		...extra,
	};
}

test("delegate child is isolated, bounded, and leaves no private state", async () => {
	await withTempParent(async (parent) => {
		const result = await runDelegateWithInvocation(options(parent), invocation("success"));
		assert.equal(result.text, "independent bounded finding");
		assert.equal(result.details.provider, "deepseek");
		assert.equal(result.details.model, "deepseek-v4-flash");
		assert.equal(result.details.usage.totalTokens, 18);
		assert.equal(JSON.stringify(result).includes(secret), false);
		assert.deepEqual(await readdir(parent), []);
	});
});

test("delegate passes the trusted high reasoning level as one child argument", async () => {
	await withTempParent(async (parent) => {
		const result = await runDelegateWithInvocation(
			options(parent, { thinkingLevel: "high" }),
			invocation("require-high"),
		);
		assert.equal(result.text, "independent bounded finding");
		assert.deepEqual(await readdir(parent), []);
	});
});

test("delegate truncates model-visible output at the UTF-8 byte bound", async () => {
	await withTempParent(async (parent) => {
		const result = await runDelegateWithInvocation(options(parent), invocation("long-output"));
		assert.equal(result.details.outputTruncated, true);
		assert.ok(Buffer.byteLength(result.text, "utf8") <= DELEGATE_LIMITS.outputBytes);
		assert.deepEqual(await readdir(parent), []);
	});
});

test("delegate kills and joins a timed-out child and credential writer", async () => {
	await withTempParent(async (parent) => {
		const limits = { ...DELEGATE_LIMITS, timeoutMs: 100, shutdownGraceMs: 100 };
		await assert.rejects(
			runDelegateWithInvocation(options(parent), invocation("hang"), limits),
			(error) => error instanceof DelegateRuntimeError && error.code === "timeout",
		);
		assert.deepEqual(await readdir(parent), []);
	});
});

test("delegate timeout owns the complete child process group", async () => {
	await withTempParent(async (parent) => {
		const limits = { ...DELEGATE_LIMITS, timeoutMs: 100, shutdownGraceMs: 200 };
		const startedAt = Date.now();
		await assert.rejects(
			runDelegateWithInvocation(options(parent), invocation("descendant-hang"), limits),
			(error) => error instanceof DelegateRuntimeError && error.code === "timeout",
		);
		assert.ok(Date.now() - startedAt < 1_000, "descendant kept the delegate pipe alive");
		assert.deepEqual(await readdir(parent), []);
	});
});

test("delegate rejects a credential echoed as model output", async () => {
	await withTempParent(async (parent) => {
		await assert.rejects(
			runDelegateWithInvocation(options(parent), invocation("secret-output")),
			(error) => error instanceof DelegateRuntimeError && error.code === "secret_exposure",
		);
		assert.deepEqual(await readdir(parent), []);
	});
});

test("delegate fails closed when the raw child stream exceeds its bound", async () => {
	await withTempParent(async (parent) => {
		const limits = { ...DELEGATE_LIMITS, shutdownGraceMs: 100 };
		await assert.rejects(
			runDelegateWithInvocation(options(parent), invocation("raw-overflow"), limits),
			(error) => error instanceof DelegateRuntimeError && error.code === "raw_stream_limit",
		);
		assert.deepEqual(await readdir(parent), []);
	});
});

test("delegate rejects an oversized task before creating child state", async () => {
	await withTempParent(async (parent) => {
		await assert.rejects(
			runDelegateWithInvocation(
				options(parent, { task: "x".repeat(DELEGATE_LIMITS.taskBytes + 1) }),
				invocation("success"),
			),
			(error) => error instanceof DelegateRuntimeError && error.code === "task_invalid",
		);
		assert.deepEqual(await readdir(parent), []);
	});
});

test("extension exposes one slot per parent run and inherits trusted model identity", async () => {
	const handlers = new Map();
	let tool;
	const calls = [];
	const pi = {
		on(name, handler) {
			handlers.set(name, handler);
		},
		registerTool(definition) {
			tool = definition;
		},
	};
	const extension = createDelegateExtension(async (received) => {
		calls.push(received);
		return {
			text: "review",
			details: { provider: received.provider, model: received.model, outputBytes: 6 },
		};
	});
	extension(pi);
	const context = {
		model: { provider: "deepseek", id: "deepseek-v4-flash" },
		thinkingLevel: "high",
		modelRegistry: { async getApiKeyForProvider() { return secret; } },
	};

	await handlers.get("before_agent_start")();
	const first = await tool.execute("one", { task: "inspect" }, undefined, undefined, context);
	const second = await tool.execute("two", { task: "inspect again" }, undefined, undefined, context);
	assert.equal(first.details.status, "completed");
	assert.equal(second.details.status, "slot_used");
	assert.equal(await handlers.get("tool_result")({
		toolName: "delegate",
		details: first.details,
		isError: false,
	}), undefined);
	assert.deepEqual(await handlers.get("tool_result")({
		toolName: "delegate",
		details: second.details,
		isError: false,
	}), { isError: true });
	assert.equal(calls.length, 1);
	assert.equal(calls[0].provider, "deepseek");
	assert.equal(calls[0].model, "deepseek-v4-flash");
	assert.equal(calls[0].thinkingLevel, "high");
	assert.equal(JSON.stringify(first).includes(secret), false);
	assert.equal(tool.parameters.properties.task.maxLength, DELEGATE_LIMITS.taskBytes);

	await handlers.get("before_agent_start")();
	await tool.execute("three", { task: "new parent run" }, undefined, undefined, context);
	assert.equal(calls.length, 2);
});
