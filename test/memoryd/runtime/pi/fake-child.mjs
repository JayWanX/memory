import { execFileSync, spawn } from "node:child_process";
import { readFileSync } from "node:fs";

const mode = process.argv[2];
const stateDir = process.env.PI_CODING_AGENT_DIR;
const auth = JSON.parse(readFileSync(`${stateDir}/auth.json`, "utf8"));
const credential = Object.values(auth)[0];
if (typeof credential?.key !== "string" || !credential.key.startsWith("!")) process.exit(31);
const secret = execFileSync("sh", ["-c", credential.key.slice(1)], { encoding: "utf8" }).trim();
if (!secret) process.exit(32);
if (process.argv.some((value) => value.includes(secret))) process.exit(33);
if (Object.values(process.env).some((value) => value?.includes(secret))) process.exit(34);
for (const required of [
	"--no-session",
	"--no-extensions",
	"--no-skills",
	"--no-prompt-templates",
	"--no-themes",
	"--no-context-files",
	"--no-tools",
	"--no-approve",
]) {
	if (!process.argv.includes(required)) process.exit(35);
}
if (mode === "require-high") {
	const thinking = process.argv.indexOf("--thinking");
	if (thinking < 0 || process.argv[thinking + 1] !== "high") process.exit(36);
}

if (mode === "hang") {
	setInterval(() => {}, 10_000);
} else if (mode === "descendant-hang") {
	const descendant = spawn(process.execPath, ["-e", "setInterval(() => {}, 10000)"], {
		detached: false,
		stdio: "inherit",
	});
	descendant.unref();
	process.exit(0);
} else if (mode === "secret-output") {
	process.stdout.write(
		`${JSON.stringify({
			type: "message_end",
			message: {
				role: "assistant",
				content: [{ type: "text", text: secret }],
				stopReason: "stop",
			},
		})}\n`,
	);
	process.stdout.write(`${JSON.stringify({ type: "agent_end" })}\n`);
} else if (mode === "raw-overflow") {
	process.stdout.write("x".repeat(300 * 1024));
} else {
	const text = mode === "long-output" ? "o".repeat(12 * 1024) : "independent bounded finding";
	process.stdout.write(
		`${JSON.stringify({
			type: "message_end",
			message: {
				role: "assistant",
				content: [{ type: "text", text }],
				stopReason: "stop",
				usage: {
					input: 11,
					output: 7,
					cacheRead: 0,
					cacheWrite: 0,
					totalTokens: 18,
					cost: { total: 0.001 },
				},
			},
		})}\n`,
	);
	process.stdout.write(`${JSON.stringify({ type: "agent_end" })}\n`);
}
