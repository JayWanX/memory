import { spawn, spawnSync } from "node:child_process";
import { chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

export const DELEGATE_LIMITS = Object.freeze({
	taskBytes: 4 * 1024,
	outputBytes: 8 * 1024,
	rawStreamBytes: 256 * 1024,
	stderrBytes: 16 * 1024,
	timeoutMs: 60_000,
	shutdownGraceMs: 5_000,
});

const SAFE_THINKING_LEVELS = new Set(["off", "minimal", "low", "medium", "high", "xhigh", "max"]);
const SECRET_ENV_NAME = /(api[_-]?key|token|secret|password|credential|deepseek)/i;

export class DelegateRuntimeError extends Error {
	constructor(code) {
		super(code);
		this.name = "DelegateRuntimeError";
		this.code = code;
	}
}

function byteLength(value) {
	return Buffer.byteLength(value, "utf8");
}

function requireBoundedString(name, value, maximumBytes) {
	if (typeof value !== "string" || value.trim() === "" || byteLength(value) > maximumBytes) {
		throw new DelegateRuntimeError(`${name}_invalid`);
	}
}

function shellQuote(value) {
	return `'${value.replaceAll("'", `'\\''`)}'`;
}

function cleanChildEnvironment(stateDir) {
	const environment = {};
	for (const name of ["PATH", "LANG", "LC_ALL", "TZ"]) {
		const value = process.env[name];
		if (value !== undefined && !SECRET_ENV_NAME.test(name)) environment[name] = value;
	}
	environment.HOME = stateDir;
	environment.TMPDIR = stateDir;
	environment.PI_CODING_AGENT_DIR = stateDir;
	environment.PI_SKIP_VERSION_CHECK = "1";
	environment.PI_TELEMETRY = "0";
	return environment;
}

function defaultPiInvocation() {
	const currentScript = process.argv[1];
	const isBundledScript = currentScript?.startsWith("/$bunfs/root/");
	if (currentScript && !isBundledScript) {
		return { command: process.execPath, args: [currentScript] };
	}
	return { command: "pi", args: [] };
}

function observeProcess(child) {
	return new Promise((resolve) => {
		let settled = false;
		const finish = (result) => {
			if (settled) return;
			settled = true;
			resolve(result);
		};
		child.once("error", () => finish({ code: null, signal: null, spawnError: true }));
		child.once("close", (code, signal) => finish({ code, signal, spawnError: false }));
	});
}

function delay(milliseconds) {
	return new Promise((resolve) => {
		const timer = setTimeout(resolve, milliseconds);
		timer.unref?.();
	});
}

function signalOwnedProcess(child, signal, processGroup) {
	if (processGroup && Number.isInteger(child.pid)) {
		try {
			process.kill(-child.pid, signal);
			return;
		} catch (error) {
			if (error?.code !== "ESRCH") throw error;
		}
	}
	if (child.exitCode === null && child.signalCode === null) child.kill(signal);
}

async function stopAndJoin(child, completion, graceMs, processGroup) {
	signalOwnedProcess(child, "SIGTERM", processGroup);
	let joined = await Promise.race([completion.then((result) => ({ result })), delay(graceMs).then(() => null)]);
	if (joined === null) {
		signalOwnedProcess(child, "SIGKILL", processGroup);
		joined = await Promise.race([
			completion.then((result) => ({ result })),
			delay(graceMs).then(() => null),
		]);
	}
	return joined?.result ?? { code: null, signal: null, spawnError: false, unreaped: true };
}

function boundedOutput(value, maximumBytes) {
	const bytes = Buffer.from(value, "utf8");
	if (bytes.length <= maximumBytes) return { text: value, truncated: false, originalBytes: bytes.length };
	const marker = Buffer.from("\n[delegate output truncated]", "utf8");
	const prefix = bytes.subarray(0, Math.max(0, maximumBytes - marker.length));
	let text = Buffer.concat([prefix, marker]).toString("utf8");
	while (byteLength(text) > maximumBytes) text = text.slice(0, -1);
	return { text, truncated: true, originalBytes: bytes.length };
}

function numberOrZero(value) {
	return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0;
}

function assistantText(message) {
	if (!Array.isArray(message?.content)) return "";
	return message.content
		.filter((part) => part?.type === "text" && typeof part.text === "string")
		.map((part) => part.text)
		.join("\n");
}

export async function runDelegate(options) {
	return runDelegateWithInvocation(options, defaultPiInvocation(), DELEGATE_LIMITS);
}

// Exported for a deterministic process-boundary oracle. Production callers use
// runDelegate(), which freezes both the Pi invocation and the limits above.
export async function runDelegateWithInvocation(options, invocation, limits = DELEGATE_LIMITS) {
	requireBoundedString("task", options?.task, limits.taskBytes);
	requireBoundedString("provider", options?.provider, 256);
	requireBoundedString("model", options?.model, 512);
	requireBoundedString("api_key", options?.apiKey, 16 * 1024);
	if (!invocation || typeof invocation.command !== "string" || !Array.isArray(invocation.args)) {
		throw new DelegateRuntimeError("invocation_invalid");
	}

	const tempParent = options.tempParent ?? tmpdir();
	const stateDir = await mkdtemp(path.join(tempParent, "memory-pi-delegate-"));
	await chmod(stateDir, 0o700);
	const fifoPath = path.join(stateDir, "provider-key.pipe");
	const authPath = path.join(stateDir, "auth.json");
	const taskPath = path.join(stateDir, "task.md");
	let child;
	let writer;
	let childCompletion;
	let writerCompletion;
	let timer;
	let abortHandler;

	try {
		const fifo = spawnSync("mkfifo", [fifoPath], { stdio: "ignore", shell: false });
		if (fifo.status !== 0) throw new DelegateRuntimeError("credential_channel_failed");
		await chmod(fifoPath, 0o600);
		const keyCommand = `!cat ${shellQuote(fifoPath)}`;
		await writeFile(
			authPath,
			`${JSON.stringify({ [options.provider]: { type: "api_key", key: keyCommand } })}\n`,
			{ encoding: "utf8", mode: 0o600 },
		);
		await chmod(authPath, 0o600);
		await writeFile(taskPath, options.task, { encoding: "utf8", mode: 0o600 });
		await chmod(taskPath, 0o600);

		const childArgs = [
			...invocation.args,
			"--mode",
			"json",
			"--print",
			"--no-session",
			"--no-extensions",
			"--no-skills",
			"--no-prompt-templates",
			"--no-themes",
			"--no-context-files",
			"--no-tools",
			"--no-approve",
			"--provider",
			options.provider,
			"--model",
			options.model,
		];
		if (SAFE_THINKING_LEVELS.has(options.thinkingLevel)) {
			childArgs.push("--thinking", options.thinkingLevel);
		}
		childArgs.push(`@${taskPath}`);

		child = spawn(invocation.command, childArgs, {
			cwd: stateDir,
			env: cleanChildEnvironment(stateDir),
			detached: process.platform !== "win32",
			shell: false,
			stdio: ["ignore", "pipe", "pipe"],
		});
		childCompletion = observeProcess(child);

		// The helper is replaced by cat, so the process we own is the only
		// credential writer. The API key crosses stdin -> FIFO once and is never
		// placed in argv, env, auth.json, task content, or returned details.
		writer = spawn("sh", ["-c", 'exec cat > "$1"', "pi-delegate-writer", fifoPath], {
			detached: process.platform !== "win32",
			shell: false,
			stdio: ["pipe", "ignore", "ignore"],
		});
		writerCompletion = observeProcess(writer);
		writer.stdin.on("error", () => {});
		writer.stdin.end(options.apiKey);

		let rawBytes = 0;
		let stderrBytes = 0;
		let pending = "";
		let invalidJSON = false;
		let agentEnded = false;
		let finalText = "";
		let stopReason = "";
		const usage = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: 0, totalTokens: 0 };
		let trigger;
		const interruption = new Promise((resolve) => {
			let fired = false;
			trigger = (reason) => {
				if (fired) return;
				fired = true;
				resolve(reason);
			};
		});
		void writerCompletion.then((result) => {
			if (result.spawnError || result.code !== 0) trigger("credential_channel_failed");
		});

		const processLine = (line) => {
			if (line.trim() === "") return;
			let event;
			try {
				event = JSON.parse(line);
			} catch {
				invalidJSON = true;
				return;
			}
			if (event.type === "message_end" && event.message?.role === "assistant") {
				finalText = assistantText(event.message);
				stopReason = typeof event.message.stopReason === "string" ? event.message.stopReason : "";
				const current = event.message.usage ?? {};
				usage.input += numberOrZero(current.input);
				usage.output += numberOrZero(current.output);
				usage.cacheRead += numberOrZero(current.cacheRead);
				usage.cacheWrite += numberOrZero(current.cacheWrite);
				usage.cost += numberOrZero(current.cost?.total);
				usage.totalTokens = numberOrZero(current.totalTokens);
			}
			if (event.type === "agent_end") agentEnded = true;
		};

		child.stdout.on("data", (chunk) => {
			rawBytes += chunk.length;
			if (rawBytes > limits.rawStreamBytes) {
				trigger("raw_stream_limit");
				return;
			}
			pending += chunk.toString("utf8");
			const lines = pending.split("\n");
			pending = lines.pop() ?? "";
			for (const line of lines) processLine(line);
		});
		child.stderr.on("data", (chunk) => {
			stderrBytes += chunk.length;
			if (stderrBytes > limits.stderrBytes) trigger("stderr_limit");
		});

		timer = setTimeout(() => trigger("timeout"), limits.timeoutMs);
		timer.unref?.();
		if (options.signal) {
			abortHandler = () => trigger("aborted");
			if (options.signal.aborted) abortHandler();
			else options.signal.addEventListener("abort", abortHandler, { once: true });
		}

		const first = await Promise.race([
			childCompletion.then((result) => ({ type: "child", result })),
			interruption.then((reason) => ({ type: "interrupt", reason })),
		]);
		if (first.type === "interrupt") {
			const results = await Promise.all([
				stopAndJoin(child, childCompletion, limits.shutdownGraceMs, process.platform !== "win32"),
				stopAndJoin(writer, writerCompletion, limits.shutdownGraceMs, process.platform !== "win32"),
			]);
			if (results.some((result) => result.unreaped)) {
				throw new DelegateRuntimeError("process_unreaped");
			}
			throw new DelegateRuntimeError(first.reason);
		}

		const writerResult = await Promise.race([
			writerCompletion.then((result) => ({ result })),
			delay(limits.shutdownGraceMs).then(() => null),
		]);
		if (writerResult === null) {
			await stopAndJoin(writer, writerCompletion, limits.shutdownGraceMs,
				process.platform !== "win32");
			throw new DelegateRuntimeError("credential_unconsumed");
		}
		if (writerResult.result.spawnError || writerResult.result.code !== 0) {
			throw new DelegateRuntimeError("credential_channel_failed");
		}
		if (pending.trim() !== "") processLine(pending);
		if (first.result.spawnError) throw new DelegateRuntimeError("child_spawn_failed");
		if (first.result.code !== 0) throw new DelegateRuntimeError("child_failed");
		if (invalidJSON || !agentEnded) throw new DelegateRuntimeError("child_protocol_invalid");
		if (stopReason === "error" || stopReason === "aborted") {
			throw new DelegateRuntimeError("child_model_failed");
		}
		if (finalText.includes(options.apiKey)) {
			throw new DelegateRuntimeError("secret_exposure");
		}

		const output = boundedOutput(finalText || "(no delegate output)", limits.outputBytes);
		return {
			text: output.text,
			details: {
				provider: options.provider,
				model: options.model,
				outputBytes: byteLength(output.text),
				outputTruncated: output.truncated,
				rawBytes,
				stopReason: stopReason || "unknown",
				usage,
			},
		};
	} finally {
		if (timer !== undefined) clearTimeout(timer);
		if (options?.signal && abortHandler) options.signal.removeEventListener("abort", abortHandler);
		if (child && childCompletion && child.exitCode === null && child.signalCode === null) {
			await stopAndJoin(child, childCompletion, limits.shutdownGraceMs,
				process.platform !== "win32").catch(() => {});
		}
		if (writer && writerCompletion && writer.exitCode === null && writer.signalCode === null) {
			await stopAndJoin(writer, writerCompletion, limits.shutdownGraceMs,
				process.platform !== "win32").catch(() => {});
		}
		await rm(stateDir, { recursive: true, force: true });
	}
}
