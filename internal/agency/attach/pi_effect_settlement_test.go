package attach

import (
	"strings"
	"testing"
)

func TestPiSubmitUsesOneBoundedProtocolToolWithoutRuntimeOrchestration(t *testing.T) {
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	source := string(projection.PiExtension())
	for _, required := range []string{
		`const SUBMIT_TOOL = "memoryd_submit";`,
		`const SUBMIT_TIMEOUT_MS = 5000;`,
		`const SUBMIT_SHUTDOWN_GRACE_MS = 100;`,
		`const MAX_INTENT_BYTES = 12 * 1024;`,
		`const MAX_RECEIPT_OUTPUT_BYTES = (4 << 10) + 1;`,
		`pi.registerTool({`, `name: SUBMIT_TOOL`,
		`execFile("memory", ["agency", "agent", "submit", "--json"]`,
		`shell: false`, `setTimeout(interrupt, SUBMIT_TIMEOUT_MS)`,
		`setTimeout(() => signalOwnedChild(child, "SIGKILL")`,
		`signalOwnedChild(child, "SIGTERM")`,
		`signal.removeEventListener("abort", interrupt)`,
		`child.stdin.end(encoded);`,
		`value = JSON.parse(raw)`,
		`receipt.schema !== "memory.agent.receipt"`,
		`receipt.outcome === "accepted"`, `receipt.outcome !== "rejected"`,
		`typeof receipt.replayed !== "boolean"`,
		`Buffer.byteLength(receipt.diagnostic, "utf8") > MAX_DIAGNOSTIC_BYTES`,
		`const INPUT_CODE = /^(invalid_argument|content_required|content_too_large|artifact_invalid|artifact_too_large)$/;`,
		`exitStatus !== 2 || keys !== 7`, `value = JSON.parse(raw)`,
		`parseOutput(stdout, error?.code)`, `status: "input_invalid"`,
		`details?.schema !== "memory.pi.effect"`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Pi Submit boundary lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		`exec("`, `execSync(`, `spawn(`, `.includes("memory`,
		`.includes("submit`, `.match(`, `pi.on("tool_call"`,
		`pi.on("turn_start"`, `pi.setActiveTools(`, `pi.getActiveTools(`,
		`ctx.abort(`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("Pi Submit boundary owns unsafe Runtime behavior %q", forbidden)
		}
	}
}
