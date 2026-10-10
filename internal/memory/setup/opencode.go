package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

// OpenCodeWriteSkill writes the memory skill to the OpenCode skills directory.
func OpenCodeWriteSkill(configDir string) (string, error) {
	skillDir := filepath.Join(configDir, "skills", "memory")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return "", err
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, assets.OpenCodeSkill, 0644); err != nil {
		return "", err
	}
	return skillPath, nil
}

// OpenCodeWritePlugin writes the memory plugin to the OpenCode plugins directory.
func OpenCodeWritePlugin(configDir string) (string, error) {
	pluginDir := filepath.Join(configDir, "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return "", err
	}
	pluginPath := filepath.Join(pluginDir, "memory.js")
	if err := os.WriteFile(pluginPath, assets.OpenCodePlugin, 0644); err != nil {
		return "", err
	}
	return pluginPath, nil
}

// OpenCodeRegisterInstructions registers the generated memory guide in opencode.json.
func OpenCodeRegisterInstructions(configDir, promptDir string) (string, error) {
	configPath := OpenCodeConfigPath(configDir)
	data, err := ReadJSONFile(configPath)
	if err != nil {
		return "", err
	}
	if _, ok := data["$schema"]; !ok {
		data["$schema"] = "https://opencode.ai/config.json"
	}
	guidePath, err := filepath.Abs(filepath.Join(promptDir, "guide.md"))
	if err != nil {
		return "", err
	}
	addOpenCodeInstruction(data, guidePath)
	if err := WriteJSONFile(configPath, data); err != nil {
		return "", err
	}
	return configPath, nil
}

// OpenCodeConfigPath returns the OpenCode config file for a config directory.
func OpenCodeConfigPath(configDir string) string {
	if filepath.Base(filepath.Clean(configDir)) == ".opencode" {
		parent := filepath.Dir(configDir)
		if parent == "." {
			return "opencode.json"
		}
		return filepath.Join(parent, "opencode.json")
	}
	return filepath.Join(configDir, "opencode.json")
}

// OpenCodeEject removes memory skill, plugin, and instruction registration.
func OpenCodeEject(configDir string) []error {
	var errs []error

	fmt.Printf("\nRemoving OpenCode integration (%s)...\n", configDir)

	targets := []struct {
		label string
		path  string
	}{
		{"Skill", filepath.Join(configDir, "skills", "memory")},
		{"Plugin", filepath.Join(configDir, "plugins", "memory.js")},
	}

	for i, target := range targets {
		if err := os.RemoveAll(target.path); err != nil {
			StatusError(i+1, 3, target.label, err)
			errs = append(errs, err)
		} else {
			StatusOK(i+1, 3, target.label, target.path+" removed")
		}
	}
	removeIfEmpty(filepath.Join(configDir, "skills"))
	removeIfEmpty(filepath.Join(configDir, "plugins"))

	configPath := OpenCodeConfigPath(configDir)
	data, err := ReadJSONFile(configPath)
	if err != nil {
		StatusError(3, 3, "Config", err)
		errs = append(errs, err)
	} else {
		removeOpenCodeInstructions(data)
		removeOpenCodeEmptySchema(data)
		if err := WriteOrRemoveJSONFile(configPath, data); err != nil {
			StatusError(3, 3, "Config", err)
			errs = append(errs, err)
		} else {
			StatusOK(3, 3, "Config", configPath+" cleaned")
		}
	}

	removeIfEmpty(configDir)
	return errs
}

func addOpenCodeInstruction(data map[string]any, guidePath string) {
	removeOpenCodeInstructions(data)
	var instructions []any
	if existing, ok := data["instructions"].([]any); ok {
		instructions = append(instructions, existing...)
	}
	instructions = append(instructions, guidePath)
	data["instructions"] = instructions
}

func removeOpenCodeInstructions(data map[string]any) {
	existing, ok := data["instructions"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(existing))
	for _, value := range existing {
		s, ok := value.(string)
		if ok && isOpenCodeMemoryInstruction(s) {
			continue
		}
		filtered = append(filtered, value)
	}
	if len(filtered) == 0 {
		delete(data, "instructions")
	} else {
		data["instructions"] = filtered
	}
}

func removeOpenCodeEmptySchema(data map[string]any) {
	if len(data) != 1 {
		return
	}
	if data["$schema"] == "https://opencode.ai/config.json" {
		delete(data, "$schema")
	}
}

func isOpenCodeMemoryInstruction(s string) bool {
	normalized := filepath.ToSlash(s)
	return strings.Contains(normalized, "memory/prompt/guide.md") ||
		strings.Contains(normalized, ".memory/prompt/guide.md")
}
