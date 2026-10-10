package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorRegisterHooksPreservesUnrelatedConfig(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(hooksPath, []byte(`{
  "version": 1,
  "hooks": {
    "sessionStart": [
      {"command": "/old/memory/prime.sh"},
      {"command": "/keep/custom.sh"}
    ],
    "stop": [
      {"command": "/old/memory/stop.sh"}
    ]
  }
}`), 0644); err != nil {
		t.Fatalf("write hooks config: %v", err)
	}

	if _, err := CursorRegisterHooks(dir, HookSelection{Nudge: true, Compact: true}); err != nil {
		t.Fatalf("register hooks: %v", err)
	}

	data, err := ReadJSONFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks config: %v", err)
	}
	hooks := data["hooks"].(map[string]any)
	sessionStart := hooks["sessionStart"].([]any)
	if len(sessionStart) != 2 {
		t.Fatalf("expected custom hook plus new prime hook: %#v", sessionStart)
	}
	if !strings.Contains(sessionStart[1].(map[string]any)["command"].(string), "hooks/memory/prime.sh") {
		t.Fatalf("expected new prime hook, got %#v", sessionStart[1])
	}
	stop := hooks["stop"].([]any)
	if len(stop) != 1 || stop[0].(map[string]any)["loop_limit"].(float64) != 1 {
		t.Fatalf("expected one nudge hook with loop limit: %#v", stop)
	}
	if _, ok := hooks["preCompact"]; !ok {
		t.Fatalf("compact hook should be registered: %#v", hooks)
	}
}

func TestCursorEjectRemovesOnlyMemoryFilesAndHooks(t *testing.T) {
	dir := t.TempDir()
	if _, err := CursorWriteSkill(dir); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if _, err := CursorWriteHook(dir, "prime.sh", []byte("#!/bin/bash\n")); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if _, err := CursorRegisterHooks(dir, HookSelection{Nudge: true, Compact: true}); err != nil {
		t.Fatalf("register hooks: %v", err)
	}
	customSkillDir := filepath.Join(dir, "skills", "custom")
	if err := os.MkdirAll(customSkillDir, 0755); err != nil {
		t.Fatalf("create custom skill: %v", err)
	}
	hooksPath := filepath.Join(dir, "hooks.json")
	data, err := ReadJSONFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	hooks := data["hooks"].(map[string]any)
	hooks["sessionStart"] = append(hooks["sessionStart"].([]any), map[string]any{"command": "/keep/custom.sh"})
	if err := WriteJSONFile(hooksPath, data); err != nil {
		t.Fatalf("write hooks: %v", err)
	}

	errs := CursorEject(dir)
	if len(errs) > 0 {
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
	data, err = ReadJSONFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks after eject: %v", err)
	}
	hooks = data["hooks"].(map[string]any)
	sessionStart := hooks["sessionStart"].([]any)
	if len(sessionStart) != 1 || containsMemory(sessionStart[0]) {
		t.Fatalf("custom hook should be preserved and memory removed: %#v", sessionStart)
	}
	if _, ok := hooks["stop"]; ok {
		t.Fatalf("stop hooks should be removed: %#v", hooks)
	}
	if _, ok := hooks["preCompact"]; ok {
		t.Fatalf("preCompact hooks should be removed: %#v", hooks)
	}
}
