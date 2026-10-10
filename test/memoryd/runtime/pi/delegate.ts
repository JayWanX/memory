import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { DELEGATE_LIMITS, DelegateRuntimeError, runDelegate } from "./delegate-runtime.mjs";

const DelegateParameters = {
	type: "object",
	properties: {
		task: {
			type: "string",
			minLength: 1,
			maxLength: DELEGATE_LIMITS.taskBytes,
			description: "A bounded evidence bundle or question for one isolated analysis context",
		},
	},
	required: ["task"],
	additionalProperties: false,
} as const;

type DelegateRunner = typeof runDelegate;

export function createDelegateExtension(delegateRunner: DelegateRunner = runDelegate) {
	return function delegateExtension(pi: ExtensionAPI) {
		let usedInAgentRun = false;

		pi.on("before_agent_start", async () => {
			usedInAgentRun = false;
		});
		// Pi treats a returned tool value as successful. Reflect the delegate's
		// closed status through the tool-result middleware, which is the host's
		// supported error channel, rather than placing an ignored isError field
		// in the execute() result.
		pi.on("tool_result", async (event) => {
			if (event.toolName !== "delegate") return;
			const details = event.details as
				| { schema?: unknown; version?: unknown; status?: unknown }
				| undefined;
			if (
				details?.schema === "memory.pi.delegate" &&
				details.version === 1 &&
				details.status !== "completed"
			) {
				return { isError: true };
			}
		});

		pi.registerTool({
			name: "delegate",
			label: "Delegate",
			description:
				"Ask one isolated, tool-free Pi context to examine a bounded evidence bundle. " +
				"The child Pi has no tools, memoryd attachment, workspace context, or session; " +
				"its returned text is advice, never an accepted effect.",
			parameters: DelegateParameters as never,

			async execute(_toolCallId, params, signal, _onUpdate, ctx) {
				if (usedInAgentRun) {
					return {
						content: [{ type: "text" as const, text: "Delegate unavailable: the bounded slot was already used." }],
						details: { schema: "memory.pi.delegate", version: 1, status: "slot_used" },
					};
				}
				usedInAgentRun = true;

				const task = typeof params?.task === "string" ? params.task : "";
				if (task.trim() === "" || Buffer.byteLength(task, "utf8") > DELEGATE_LIMITS.taskBytes) {
					return {
						content: [{ type: "text" as const, text: "Delegate rejected an invalid bounded task." }],
						details: { schema: "memory.pi.delegate", version: 1, status: "task_invalid" },
					};
				}
				if (!ctx.model) {
					return {
						content: [{ type: "text" as const, text: "Delegate unavailable: no active parent model." }],
						details: { schema: "memory.pi.delegate", version: 1, status: "model_unavailable" },
					};
				}

				const provider = ctx.model.provider;
				const model = ctx.model.id;
				const apiKey = await ctx.modelRegistry.getApiKeyForProvider(provider);
				if (!apiKey) {
					return {
						content: [{ type: "text" as const, text: "Delegate unavailable: parent authentication was not resolved." }],
						details: { schema: "memory.pi.delegate", version: 1, status: "auth_unavailable" },
					};
				}

				try {
					const result = await delegateRunner({
						task,
						provider,
						model,
						thinkingLevel: ctx.thinkingLevel,
						apiKey,
						signal,
					});
					return {
						content: [{ type: "text" as const, text: result.text }],
						details: {
							schema: "memory.pi.delegate",
							version: 1,
							status: "completed",
							...result.details,
						},
					};
				} catch (error) {
					const code = error instanceof DelegateRuntimeError ? error.code : "internal";
					return {
						content: [{ type: "text" as const, text: `Delegate failed safely: ${code}.` }],
						details: { schema: "memory.pi.delegate", version: 1, status: "failed", code },
					};
				}
			},
		});
	};
}

export default createDelegateExtension();
