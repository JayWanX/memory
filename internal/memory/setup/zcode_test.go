package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

func TestZCodePowerShellHookEncoding(t *testing.T) {
	bom := []byte{0xef, 0xbb, 0xbf}
	for _, hook := range []struct {
		name string
		body []byte
	}{
		{"prime.ps1", assets.ZCodePrimeHookPowerShell},
		{"user_prompt.ps1", assets.ZCodeUserPromptHookPowerShell},
		{"stop.ps1", assets.ZCodeStopHookPowerShell},
	} {
		t.Run(hook.name, func(t *testing.T) {
			if !utf8.Valid(hook.body) {
				t.Fatal("PowerShell asset is not valid UTF-8")
			}
			// Windows PowerShell 5.1 reads BOM-less script source as ANSI.
			// Any asset with non-ASCII literals must carry its own UTF-8 BOM.
			for _, b := range bytes.TrimPrefix(hook.body, bom) {
				if b >= utf8.RuneSelf && !bytes.HasPrefix(hook.body, bom) {
					t.Fatal("non-ASCII PowerShell source requires a UTF-8 BOM")
				}
			}
			path, err := ZCodeWriteHook(t.TempDir(), hook.name, hook.body)
			if err != nil {
				t.Fatal(err)
			}
			installed, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(installed, hook.body) {
				t.Fatal("setup changed the embedded PowerShell bytes")
			}
		})
	}
}

func TestZCodeRegisterHooksPreservesUnrelatedConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "theme": "dark",
  "hooks": {
    "enabled": false,
    "timeoutMs": 15000,
    "events": {
      "SessionStart": [
        {"hooks": [{"type": "process", "command": "bash", "args": ["/old/memory/prime.sh"]}]},
        {"hooks": [{"type": "process", "command": "node", "args": ["/keep/custom.mjs"]}]}
      ],
      "PreToolUse": [
        {"matcher": "Write", "hooks": [{"type": "process", "command": "node", "args": ["/keep/check.mjs"]}]}
      ]
    }
  }
}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := zcodeRegisterHooks(dir, "linux"); err != nil {
		t.Fatalf("register hooks: %v", err)
	}

	data, err := ReadJSONFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if data["theme"] != "dark" {
		t.Fatalf("unrelated root config should be preserved: %#v", data)
	}
	hooks := data["hooks"].(map[string]interface{})
	if hooks["enabled"] != true || hooks["timeoutMs"].(float64) != 15000 {
		t.Fatalf("hooks settings not preserved/enabled: %#v", hooks)
	}
	events := hooks["events"].(map[string]interface{})
	if _, ok := events["PreToolUse"]; !ok {
		t.Fatalf("custom event should be preserved: %#v", events)
	}
	sessionStart := events["SessionStart"].([]interface{})
	if len(sessionStart) != 2 {
		t.Fatalf("expected custom hook plus new prime hook: %#v", sessionStart)
	}
	newHook := sessionStart[1].(map[string]interface{})["hooks"].([]interface{})[0].(map[string]interface{})
	args := newHook["args"].([]interface{})
	if newHook["type"] != "process" || newHook["command"] != "bash" || len(args) != 1 || !strings.Contains(args[0].(string), "hooks/memory/prime.sh") {
		t.Fatalf("unexpected ZCode process hook: %#v", newHook)
	}
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		if _, ok := events[event]; !ok {
			t.Fatalf("missing %s hook: %#v", event, events)
		}
	}
}

func TestZCodeEjectRemovesOnlyMemoryFilesAndHooks(t *testing.T) {
	dir := t.TempDir()
	if _, err := ZCodeWriteSkill(dir); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if _, err := ZCodeWriteHook(dir, "prime.sh", []byte("#!/bin/bash\n")); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if _, err := zcodeRegisterHooks(dir, "linux"); err != nil {
		t.Fatalf("register hooks: %v", err)
	}

	customSkillDir := filepath.Join(dir, "skills", "custom")
	if err := os.MkdirAll(customSkillDir, 0o755); err != nil {
		t.Fatalf("create custom skill: %v", err)
	}
	configPath := filepath.Join(dir, "cli", "config.json")
	data, err := ReadJSONFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	hooks := data["hooks"].(map[string]interface{})
	events := hooks["events"].(map[string]interface{})
	events["SessionStart"] = append(events["SessionStart"].([]interface{}), map[string]interface{}{
		"hooks": []interface{}{map[string]interface{}{
			"type": "process", "command": "node", "args": []interface{}{`/keep/custom.mjs`},
		}},
	})
	if err := WriteJSONFile(configPath, data); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if errs := ZCodeEject(dir); len(errs) > 0 {
		t.Fatalf("eject errors: %v", errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "memory")); !os.IsNotExist(err) {
		t.Fatalf("memory skill should be removed, err=%v", err)
	}
	if _, err := os.Stat(customSkillDir); err != nil {
		t.Fatalf("custom skill should be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks", "memory")); !os.IsNotExist(err) {
		t.Fatalf("memory hooks should be removed, err=%v", err)
	}

	data, err = ReadJSONFile(configPath)
	if err != nil {
		t.Fatalf("read config after eject: %v", err)
	}
	hooks = data["hooks"].(map[string]interface{})
	events = hooks["events"].(map[string]interface{})
	sessionStart := events["SessionStart"].([]interface{})
	if len(sessionStart) != 1 || containsMemory(sessionStart[0]) {
		t.Fatalf("custom hook should be preserved and memory removed: %#v", sessionStart)
	}
	if _, ok := events["UserPromptSubmit"]; ok {
		t.Fatalf("user prompt hook should be removed: %#v", events)
	}
	if _, ok := events["Stop"]; ok {
		t.Fatalf("stop hook should be removed: %#v", events)
	}
}

func TestZCodeRegisterHooksUsesNativeWindowsPowerShell(t *testing.T) {
	dir := t.TempDir()
	if _, err := zcodeRegisterHooks(dir, "windows"); err != nil {
		t.Fatalf("register hooks: %v", err)
	}

	data, err := ReadJSONFile(filepath.Join(dir, "cli", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	hooks := data["hooks"].(map[string]interface{})
	events := hooks["events"].(map[string]interface{})
	for event, filename := range map[string]string{
		"SessionStart":     "prime.ps1",
		"UserPromptSubmit": "user_prompt.ps1",
		"Stop":             "stop.ps1",
	} {
		entry := events[event].([]interface{})[0].(map[string]interface{})
		process := entry["hooks"].([]interface{})[0].(map[string]interface{})
		args := process["args"].([]interface{})
		if process["command"] != "powershell.exe" || len(args) != 6 {
			t.Fatalf("unexpected %s Windows process hook: %#v", event, process)
		}
		if args[0] != "-NoProfile" || args[1] != "-NonInteractive" || args[2] != "-ExecutionPolicy" || args[3] != "Bypass" || args[4] != "-File" {
			t.Fatalf("unexpected %s PowerShell arguments: %#v", event, args)
		}
		if path, ok := args[5].(string); !ok || !strings.HasSuffix(path, filepath.Join("hooks", "memory", filename)) {
			t.Fatalf("unexpected %s PowerShell script path: %#v", event, args[5])
		}
	}
}
