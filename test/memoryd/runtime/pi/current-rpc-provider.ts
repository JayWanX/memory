import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const PROVIDER = "memory-current-oracle";
const MODEL = "current-oracle";
const TOOL = "memoryd_current";

const usage = {
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 0,
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
};

export default function (pi: ExtensionAPI) {
  pi.registerProvider(PROVIDER, {
    api: "openai-completions",
    apiKey: "runtime-oracle",
    baseUrl: "http://127.0.0.1",
    models: [{
      id: MODEL,
      name: "Current Runtime Oracle",
      reasoning: false,
      input: ["text"],
      cost: usage.cost,
      contextWindow: 4096,
      maxTokens: 256,
    }],
    streamSimple(model, context) {
      const stream = createAssistantMessageEventStream();
      queueMicrotask(() => {
        const hasResult = context.messages.some((message) =>
          message.role === "toolResult" && message.toolName === TOOL);
        const content = hasResult ?
          [{ type: "text" as const, text: "Current observed." }] :
          [{ type: "toolCall" as const, id: "current-call", name: TOOL, arguments: {} }];
        const stopReason = hasResult ? "stop" as const : "toolUse" as const;
        const message = {
          role: "assistant" as const,
          content,
          api: model.api,
          provider: model.provider,
          model: model.id,
          usage,
          stopReason,
          timestamp: Date.now(),
        };
        stream.push({ type: "start", partial: message });
        stream.push({ type: "done", reason: stopReason, message });
      });
      return stream;
    },
  });
}
