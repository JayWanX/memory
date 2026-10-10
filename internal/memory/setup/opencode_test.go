package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeWriteSkillAndPlugin(t *testing.T) {
	dir := t.TempDir()

	skillPath, err := OpenCodeWriteSkill(dir)
	if err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if skillPath != filepath.Join(dir, "skills", "memory", "SKILL.md") {
		t.Fatalf("skill path = %q", skillPath)
	}
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if !strings.Contains(string(skill), "OpenCode") {
		t.Fatalf("skill should mention OpenCode: %s", string(skill))
	}

	pluginPath, err := OpenCodeWritePlugin(dir)
	if err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	if pluginPath != filepath.Join(dir, "plugins", "memory.js") {
		t.Fatalf("plugin path = %q", pluginPath)
	}
	plugin, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	for _, want := range []string{
		`export default { id: "memory", setup, server }`,
		`ctx.session.hook("context"`, `ctx.session.hook("compaction"`, `ctx.shell.hook("create.before"`,
		"experimental.chat.messages.transform", "experimental.session.compacting", "shell.env",
	} {
		if !strings.Contains(string(plugin), want) {
			t.Fatalf("plugin missing %q: %s", want, string(plugin))
		}
	}
}

func TestOpenCodeConfigPath(t *testing.T) {
	if got := OpenCodeConfigPath(".opencode"); got != "opencode.json" {
		t.Fatalf("local config path = %q", got)
	}
	root := t.TempDir()
	if got := OpenCodeConfigPath(filepath.Join(root, ".opencode")); got != filepath.Join(root, "opencode.json") {
		t.Fatalf("temp local config path = %q", got)
	}
	if got := OpenCodeConfigPath(filepath.Join(root, ".config", "opencode")); got != filepath.Join(root, ".config", "opencode", "opencode.json") {
		t.Fatalf("global config path = %q", got)
	}
}

func TestOpenCodeRegisterInstructionsPreservesUnrelatedConfig(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".opencode")
	configPath := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(configPath, []byte(`{
  "$schema": "https://opencode.ai/config.json",
  "instructions": [
    "CONTRIBUTING.md",
    "/old/.memory/prompt/guide.md"
  ],
  "theme": "dark"
}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	promptDir := filepath.Join(root, "memory", "prompt")

	if _, err := OpenCodeRegisterInstructions(configDir, promptDir); err != nil {
		t.Fatalf("register instructions: %v", err)
	}

	data, err := ReadJSONFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if data["theme"] != "dark" {
		t.Fatalf("unrelated config should be preserved: %#v", data)
	}
	instructions := data["instructions"].([]any)
	if len(instructions) != 2 {
		t.Fatalf("expected existing instruction plus new guide: %#v", instructions)
	}
	if instructions[0] != "CONTRIBUTING.md" {
		t.Fatalf("existing instruction should be preserved: %#v", instructions)
	}
	gotGuide, _ := instructions[1].(string)
	if !strings.HasSuffix(filepath.ToSlash(gotGuide), "/memory/prompt/guide.md") {
		t.Fatalf("new guide instruction not registered: %#v", instructions)
	}

	if _, err := OpenCodeRegisterInstructions(configDir, promptDir); err != nil {
		t.Fatalf("register instructions again: %v", err)
	}
	data, err = ReadJSONFile(configPath)
	if err != nil {
		t.Fatalf("read config after second register: %v", err)
	}
	if got := len(data["instructions"].([]any)); got != 2 {
		t.Fatalf("register should be idempotent, got %d instructions: %#v", got, data["instructions"])
	}
}

func TestOpenCodeEjectRemovesOnlyMemoryFilesAndInstructions(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".opencode")
	if _, err := OpenCodeWriteSkill(configDir); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if _, err := OpenCodeWritePlugin(configDir); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	promptDir := filepath.Join(root, "memory", "prompt")
	if _, err := OpenCodeRegisterInstructions(configDir, promptDir); err != nil {
		t.Fatalf("register instructions: %v", err)
	}
	customSkillDir := filepath.Join(configDir, "skills", "custom")
	if err := os.MkdirAll(customSkillDir, 0755); err != nil {
		t.Fatalf("create custom skill: %v", err)
	}
	customPlugin := filepath.Join(configDir, "plugins", "custom.js")
	if err := os.WriteFile(customPlugin, []byte("export const Custom = async () => ({})\n"), 0644); err != nil {
		t.Fatalf("write custom plugin: %v", err)
	}
	configPath := filepath.Join(root, "opencode.json")
	data, err := ReadJSONFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	data["instructions"] = append(data["instructions"].([]any), "docs/rules.md")
	if err := WriteJSONFile(configPath, data); err != nil {
		t.Fatalf("write config: %v", err)
	}

	errs := OpenCodeEject(configDir)
	if len(errs) > 0 {
		t.Fatalf("eject errors: %v", errs)
	}
	if _, err := os.Stat(filepath.Join(configDir, "skills", "memory")); !os.IsNotExist(err) {
		t.Fatalf("memory skill should be removed, err=%v", err)
	}
	if _, err := os.Stat(customSkillDir); err != nil {
		t.Fatalf("custom skill should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "plugins", "memory.js")); !os.IsNotExist(err) {
		t.Fatalf("memory plugin should be removed, err=%v", err)
	}
	if _, err := os.Stat(customPlugin); err != nil {
		t.Fatalf("custom plugin should remain: %v", err)
	}
	data, err = ReadJSONFile(configPath)
	if err != nil {
		t.Fatalf("read config after eject: %v", err)
	}
	instructions := data["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != "docs/rules.md" {
		t.Fatalf("only custom instruction should remain: %#v", instructions)
	}
}

func TestOpenCodeEjectRemovesEmptyGeneratedConfig(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".opencode")
	promptDir := filepath.Join(root, "memory", "prompt")
	if _, err := OpenCodeRegisterInstructions(configDir, promptDir); err != nil {
		t.Fatalf("register instructions: %v", err)
	}

	errs := OpenCodeEject(configDir)
	if len(errs) > 0 {
		t.Fatalf("eject errors: %v", errs)
	}
	if _, err := os.Stat(filepath.Join(root, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("generated config should be removed, err=%v", err)
	}
}
