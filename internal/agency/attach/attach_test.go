package attach

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestLoadHasOneFixedCueOneBoundedReceiptAndNoAuthorityOrSecretSurface(t *testing.T) {
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	guide, cue, extension := projection.Guide(), projection.HookCue(), projection.PiExtension()
	if len(guide) == 0 || len(guide) > MaxGuideBytes || cue == "" || len(cue) > MaxCueBytes ||
		len(extension) == 0 || len(extension) > MaxExtensionBytes {
		t.Fatalf("asset sizes = guide %d, cue %d, extension %d",
			len(guide), len(cue), len(extension))
	}
	if !strings.Contains(cue, ".pi/skills/memoryd/SKILL.md") {
		t.Fatal("fixed cue does not name the installed, bounded guide projection")
	}
	assertGuideTerminalSurface(t, string(guide))
	source := string(extension)
	for _, required := range []string{
		`pi.on("before_agent_start"`, `execFileSync("memory", ["agency", ...args]`,
		`["hook", "attach", "--json"]`, `["hook", "end", "--json"]`,
		`pi.on("session_shutdown"`, `randomBytes(32).toString("base64url")`,
		`stdio: ["pipe", "ignore", "ignore"]`, `input: boundaryEnvelope(boundary)`,
		`timeout: ATTACH_TIMEOUT_MS`, `content: HOOK_CUE`, `display: false`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Pi extension lacks %q", required)
		}
	}
	if strings.Count(source, "content: HOOK_CUE") != 1 ||
		strings.Count(source, "function submitResult(") != 1 ||
		strings.Count(source, "function parseOutput(") != 1 ||
		strings.Count(source, "JSON.parse(raw)") != 1 || !strings.Contains(source, cue) {
		t.Fatal("extension does not expose exactly one fixed cue and one bounded Receipt surface")
	}
	for _, forbidden := range []string{`pi.on("turn_end"`, `pi.on("agent_end"`} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("Pi extension uses non-Host boundary callback %q", forbidden)
		}
	}
	all := strings.ToLower(string(guide) + "\n" + cue + "\n" + source)
	for _, forbidden := range []string{
		"review", "workflow", "case", "contract-net", "blackboard", "memory.wiki",
		"work.", "knowledge.",
		"--event-id", "--operation-id", "--principal", "--fence", "--peer-id",
		"deepseek", "api_key", "api-key", "authorization:", "bearer ", "sk-",
		"process.env", "content: output", "content: result", "text: output", "text: result",
		"model:", "provider:",
	} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("projection contains forbidden surface %q", forbidden)
		}
	}
	guide[0] ^= 0xff
	extension[0] ^= 0xff
	if bytes.Equal(guide, projection.Guide()) || bytes.Equal(extension, projection.PiExtension()) {
		t.Fatal("Load returned mutable embedded assets")
	}
}

func assertGuideTerminalSurface(t *testing.T, guide string) {
	t.Helper()
	normalized := strings.Join(strings.Fields(guide), " ")
	for _, required := range []string{
		"memoryd_current {}",
		"`View -> Intent -> Receipt -> View`",
		"Only `allowed_intents` states which structural consequences are available now",
		"memory agency artifact capture --json < PATH",
		"memory agency artifact read \"$HANDLE\"",
		"exactly one nonempty",
		"no Markdown",
		"Each `successors` element is only",
		`"successors":[{"self":true}]`,
		"next eligible Hook boundary and read a new View",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("guide lacks complete, bounded terminal surface %q", required)
		}
	}
	for _, required := range []string{
		"All handles are opaque and scoped to that exact View",
		"never guess one, reinterpret it, or carry it into a later View",
		"open semantics with one closed structural consequence",
		"References affect later Views and create no responsibility",
		"A Reference action does not implicitly advance or close `current`",
		"If no offered consequence expresses the intended effect, submit no Intent",
		"memoryd fails closed",
		"ends this governed Host opportunity: stop",
		"`input_invalid` is a control diagnostic, not a Receipt",
		"A `rejected` Receipt records an admission rejection",
		"never creates a second effect",
		"Peer delivery remains candidate input until receiver admission",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("guide lacks protocol rule %q", required)
		}
	}
	if strings.Contains(guide, "$INTENT_JSON") {
		t.Error("guide relies on an undefined cross-tool shell variable")
	}
}

func TestGuideIsCapabilityNeutralAndDoesNotPrescribeAnIntentEpisode(t *testing.T) {
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	guide := strings.ToLower(string(projection.Guide()))
	for _, forbidden := range []string{
		`"kind":"work.`, `"kind":"knowledge.`, "review.", "contract-net", "blackboard",
		"submit once", "correct once", "advance only when", "return one correlated terminal disposition",
	} {
		if strings.Contains(guide, forbidden) {
			t.Fatalf("guide prescribes capability or episode semantics %q", forbidden)
		}
	}
}

func TestGuideTracksCanonicalAgentIntentFieldsAndClosedShapes(t *testing.T) {
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	guide := string(projection.Guide())
	fields := []string{"kind", "payload", "consequence", "subject_handling", "successors",
		"reference_key", "reference_head", "artifacts", "causation_handles", "correlation_handle"}
	for _, field := range fields {
		if !strings.Contains(guide, "`"+field+"`") {
			t.Errorf("guide lacks canonical AgentIntent field %q", field)
		}
	}
	for _, consequence := range []agency.Consequence{
		agency.ConsequenceCreateHandlings,
		agency.ConsequenceAdvanceHandling,
		agency.ConsequenceResolveCompleted,
		agency.ConsequenceResolveDeclined,
		agency.ConsequenceResolveUnresolved,
		agency.ConsequencePublishReference,
		agency.ConsequenceSupersedeReference,
		agency.ConsequenceRetractReference,
	} {
		if !strings.Contains(guide, "`"+consequence.String()+"`") {
			t.Errorf("guide lacks closed consequence %q", consequence.String())
		}
	}
	for _, required := range []string{
		`{"self":true}`, `{"alias":"<View-offered target>"}`,
		`{"kind":"candidate","handle":"<captured handle>"}`,
		`{"kind":"view_handle","handle":"<View-offered handle>"}`,
	} {
		if !strings.Contains(guide, required) {
			t.Errorf("guide lacks canonical nested shape %q", required)
		}
	}
}

func TestPiHookTimeoutCoversEnsureAndCleanupWithinOneFixedBound(t *testing.T) {
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	source := string(projection.PiExtension())
	match := regexp.MustCompile(`const ATTACH_TIMEOUT_MS = ([0-9]+);`).FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("Pi extension has no single literal attach timeout: %q", source)
	}
	timeout, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	const (
		ensureMillis  = 3000
		cleanupMillis = 1000
		fixedBound    = 5000
	)
	if timeout != fixedBound || timeout <= ensureMillis+cleanupMillis {
		t.Fatalf("Pi attach timeout = %dms; want fixed %dms above %dms ensure+cleanup",
			timeout, fixedBound, ensureMillis+cleanupMillis)
	}
	if strings.Count(source, "ATTACH_TIMEOUT_MS") != 2 {
		t.Fatal("Pi extension does not use exactly one declared attach timeout")
	}
}

func TestPiHookRetriesOnePrivateBoundaryAndEmitsNoCueOnFailure(t *testing.T) {
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	source := string(projection.PiExtension())
	for _, required := range []string{
		"const ATTACH_ATTEMPTS = 2;",
		"for (let attempt = 0; attempt < ATTACH_ATTEMPTS; attempt += 1)",
		"if (runBoundary([\"hook\", \"attach\", \"--json\"], boundary)) return true;",
		"if (!attachBoundary(boundary)) return undefined;",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Pi bounded attachment retry lacks %q", required)
		}
	}
	if strings.Count(source, "const boundary = randomBytes(32)") != 1 {
		t.Fatal("Pi attachment retry can mint more than one boundary nonce")
	}
}

func TestPiHookDoesNotOrchestrateRuntimeToolsOrTurns(t *testing.T) {
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	source := string(projection.PiExtension())
	for _, required := range []string{
		`pi.on("before_agent_start"`, `pi.on("agent_settled"`,
		`pi.on("session_shutdown"`, "releaseBoundary();",
		"if (!releaseBoundary()) return undefined;",
		"if (!endBoundary(boundary)) return false;",
		"activeBoundary = undefined;",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Pi lifecycle adapter lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		`pi.on("tool_call"`, `pi.on("turn_start"`, `pi.setActiveTools(`,
		`pi.getActiveTools(`, `ctx.abort(`, "MAX_TOOL_CALL_ATTEMPTS_PER_RUN",
		"MAX_EFFECT_SETTLEMENT_ATTEMPTS", "ATTENTION_EXHAUSTED_REASON",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("Pi lifecycle adapter owns Runtime orchestration %q", forbidden)
		}
	}
}

func TestInstallPiIsProjectLocalExactAndPreservesAdjacentFiles(t *testing.T) {
	workspace := testWorkspace(t)
	legacy := filepath.Join(workspace, ".pi", "skills", "memory", "SKILL.md")
	custom := filepath.Join(workspace, ".pi", "extensions", "custom.ts")
	writeTestFile(t, legacy, []byte("legacy memory\n"), 0o644)
	writeTestFile(t, custom, []byte("custom extension\n"), 0o644)

	receipt, err := InstallPi(workspace)
	if err != nil {
		t.Fatal(err)
	}
	assertInstallPaths(t, workspace, receipt)
	if receipt.Replayed || receipt.Revision == "" {
		t.Fatalf("first InstallPi receipt = %#v", receipt)
	}
	projection, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, receipt.GuidePath, projection.Guide(), projectedMode)
	assertFile(t, receipt.ExtensionPath, projection.PiExtension(), projectedMode)
	assertFile(t, receipt.CurrentExtensionPath, projection.PiCurrentExtension(), projectedMode)
	assertFile(t, receipt.JournalPath, mustPlan(t, workspace).journalBytes, journalMode)
	assertFile(t, legacy, []byte("legacy memory\n"), 0o644)
	assertFile(t, custom, []byte("custom extension\n"), 0o644)
	assertMode(t, filepath.Dir(receipt.JournalPath), 0o700)
	if err := VerifyPi(workspace); err != nil {
		t.Fatalf("VerifyPi() = %v", err)
	}
}

func TestInstallPiExactReplayDoesNotRewriteOwnedFiles(t *testing.T) {
	workspace := testWorkspace(t)
	first, err := InstallPi(workspace)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{first.GuidePath, first.ExtensionPath, first.CurrentExtensionPath,
		first.JournalPath}
	identities := make([]os.FileInfo, len(paths))
	for index, path := range paths {
		identities[index], err = os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	second, err := InstallPi(workspace)
	if err != nil || !second.Replayed || second.Revision != first.Revision {
		t.Fatalf("replay InstallPi() = (%#v, %v)", second, err)
	}
	for index, path := range paths {
		current, statErr := os.Lstat(path)
		if statErr != nil || !os.SameFile(identities[index], current) {
			t.Fatalf("replay rewrote %s: %v", path, statErr)
		}
	}
}

func TestInstallPiRecoversEveryDurableInterruption(t *testing.T) {
	stages := []string{
		"after_journal",
		"after_file:.pi/extensions/memoryd-current.ts",
		"after_file:.pi/extensions/memoryd.ts",
		"after_file:.pi/skills/memoryd/SKILL.md",
	}
	interrupted := errors.New("test interruption")
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			workspace := testWorkspace(t)
			boundary := func(current string) error {
				if current == stage {
					return interrupted
				}
				return nil
			}
			if _, err := installPi(workspace, boundary); !errors.Is(err, interrupted) {
				t.Fatalf("interrupted install = %v", err)
			}
			receipt, err := InstallPi(workspace)
			if err != nil {
				t.Fatalf("recovered InstallPi() = (%#v, %v)", receipt, err)
			}
			if err := VerifyPi(workspace); err != nil {
				t.Fatalf("recovered VerifyPi() = %v", err)
			}
		})
	}
}

func TestInstallPiRecoversBoundedCrashStages(t *testing.T) {
	t.Run("incomplete journal stage", func(t *testing.T) {
		workspace := testWorkspace(t)
		plan := mustPlan(t, workspace)
		if err := ensureJournalDirectory(plan); err != nil {
			t.Fatal(err)
		}
		stage := stagePath(filepath.Dir(plan.journalPath), plan.journalPath)
		if err := os.WriteFile(stage, plan.journalBytes[:8], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallPi(workspace); err != nil {
			t.Fatalf("InstallPi(incomplete stage) = %v", err)
		}
		assertAbsent(t, stage)
		if err := VerifyPi(workspace); err != nil {
			t.Fatalf("VerifyPi(incomplete stage recovery) = %v", err)
		}
	})

	t.Run("complete unlinked projected stage", func(t *testing.T) {
		workspace := testWorkspace(t)
		plan := mustPlan(t, workspace)
		if err := beginInstall(plan); err != nil {
			t.Fatal(err)
		}
		if err := ensureProjectionDirectories(plan); err != nil {
			t.Fatal(err)
		}
		file := plan.files[0]
		stage, err := prepareStage(filepath.Dir(plan.journalPath), file.path,
			file.content, projectedMode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := InstallPi(workspace); err != nil {
			t.Fatalf("InstallPi(unlinked stage) = %v", err)
		}
		assertAbsent(t, stage)
		if err := VerifyPi(workspace); err != nil {
			t.Fatalf("VerifyPi(unlinked stage recovery) = %v", err)
		}
	})

	t.Run("linked projected stage", func(t *testing.T) {
		workspace := testWorkspace(t)
		plan := mustPlan(t, workspace)
		if err := beginInstall(plan); err != nil {
			t.Fatal(err)
		}
		if err := ensureProjectionDirectories(plan); err != nil {
			t.Fatal(err)
		}
		file := plan.files[0]
		stage, err := prepareStage(filepath.Dir(plan.journalPath), file.path,
			file.content, projectedMode)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Link(stage, file.path); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallPi(workspace); err != nil {
			t.Fatalf("InstallPi(linked stage) = %v", err)
		}
		assertAbsent(t, stage)
		if err := VerifyPi(workspace); err != nil {
			t.Fatalf("VerifyPi(linked stage recovery) = %v", err)
		}
	})
}

func TestInstallPiRejectsUnownedTargetAndOwnedDrift(t *testing.T) {
	t.Run("unowned target", func(t *testing.T) {
		workspace := testWorkspace(t)
		target := filepath.Join(workspace, ".pi", "extensions", "memoryd.ts")
		writeTestFile(t, target, []byte("user file\n"), projectedMode)
		if _, err := InstallPi(workspace); !errors.Is(err, ErrDrift) {
			t.Fatalf("InstallPi(unowned) = %v", err)
		}
		assertFile(t, target, []byte("user file\n"), projectedMode)
	})
	t.Run("content drift", func(t *testing.T) {
		workspace := testWorkspace(t)
		receipt, err := InstallPi(workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receipt.GuidePath, []byte("drift\n"), projectedMode); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallPi(workspace); !errors.Is(err, ErrDrift) {
			t.Fatalf("InstallPi(content drift) = %v", err)
		}
		assertFile(t, receipt.GuidePath, []byte("drift\n"), projectedMode)
	})
	t.Run("mode drift", func(t *testing.T) {
		workspace := testWorkspace(t)
		receipt, err := InstallPi(workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(receipt.ExtensionPath, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := VerifyPi(workspace); !errors.Is(err, ErrDrift) {
			t.Fatalf("VerifyPi(mode drift) = %v", err)
		}
	})
	t.Run("missing owned file recovers", func(t *testing.T) {
		workspace := testWorkspace(t)
		receipt, err := InstallPi(workspace)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(receipt.GuidePath); err != nil {
			t.Fatal(err)
		}
		recovered, err := InstallPi(workspace)
		if err != nil || recovered.Replayed {
			t.Fatalf("InstallPi(missing file) = (%#v, %v)", recovered, err)
		}
		if err := VerifyPi(workspace); err != nil {
			t.Fatalf("VerifyPi(recovered missing file) = %v", err)
		}
	})
}

func TestInstallPiRejectsSymlinkedProjectionAncestors(t *testing.T) {
	workspace := testWorkspace(t)
	outside := testWorkspace(t)
	if err := os.Symlink(outside, filepath.Join(workspace, ".pi")); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPi(workspace); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("InstallPi(symlinked .pi) = %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: entries=%v err=%v", entries, err)
	}
}

func TestOwnerDriftFailsClosed(t *testing.T) {
	path := filepath.Join(testWorkspace(t), "owned")
	if err := os.WriteFile(path, []byte("owned"), projectedMode); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := foreignOwnerInfo{FileInfo: info, uid: uint32(os.Geteuid() + 1)}
	if err := validateSafeFile(foreign, projectedMode, 64); err == nil {
		t.Fatal("foreign-owner file passed validation")
	}
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	foreignDirectory := foreignOwnerInfo{FileInfo: directory, uid: uint32(os.Geteuid() + 1)}
	if err := validateSafeDirectory(foreignDirectory, false); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("foreign-owner directory validation = %v", err)
	}
}

type foreignOwnerInfo struct {
	os.FileInfo
	uid uint32
}

func (info foreignOwnerInfo) Sys() any {
	stat := *info.FileInfo.Sys().(*syscall.Stat_t)
	stat.Uid = info.uid
	return &stat
}

func assertInstallPaths(t *testing.T, workspace string, receipt InstallReceipt) {
	t.Helper()
	if receipt.GuidePath != filepath.Join(workspace, ".pi", "skills", "memoryd", "SKILL.md") ||
		receipt.CurrentExtensionPath != filepath.Join(workspace, ".pi", "extensions", "memoryd-current.ts") ||
		receipt.ExtensionPath != filepath.Join(workspace, ".pi", "extensions", "memoryd.ts") ||
		receipt.JournalPath != filepath.Join(workspace, ".memory", "agency", "attach", "pi",
			"ownership.json") {
		t.Fatalf("InstallPi paths = %#v", receipt)
	}
}

func mustPlan(t *testing.T, workspace string) installPlan {
	t.Helper()
	plan, err := prepareInstall(workspace)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testWorkspace(t *testing.T) string {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}

func writeTestFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path string, expected []byte, mode os.FileMode) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(content, expected) {
		t.Fatalf("file %s = %q, err=%v; want %q", path, content, err, expected)
	}
	assertMode(t, path, mode)
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != mode || !ownedByCurrentUser(info) {
		t.Fatalf("path %s = (%v, %v); want owner mode %04o", path, info, err, mode)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s remains: %v", path, err)
	}
}
