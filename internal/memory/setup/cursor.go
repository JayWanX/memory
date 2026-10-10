package setup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

// CursorWriteSkill writes the memory skill to the Cursor skills directory.
func CursorWriteSkill(configDir string) (string, error) {
	skillDir := filepath.Join(configDir, "skills", "memory")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return "", err
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, assets.CursorSkill, 0644); err != nil {
		return "", err
	}
	return skillPath, nil
}

// CursorWriteHook writes a hook script to the Cursor hooks directory.
func CursorWriteHook(configDir, filename string, content []byte) (string, error) {
	hooksDir := filepath.Join(configDir, "hooks", "memory")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return "", err
	}
	hookPath := filepath.Join(hooksDir, filename)
	if err := writeExecutableFile(hookPath, content); err != nil {
		return "", err
	}
	return hookPath, nil
}

// CursorRegisterHooks registers selected Memory lifecycle hooks in hooks.json.
func CursorRegisterHooks(configDir string, sel HookSelection) (string, error) {
	hooksDir := filepath.Join(configDir, "hooks", "memory")
	absHooksDir, err := filepath.Abs(hooksDir)
	if err != nil {
		return "", err
	}
	hooksPath := filepath.Join(configDir, "hooks.json")
	data, err := ReadJSONFile(hooksPath)
	if err != nil {
		return "", err
	}
	addCursorHooks(data, absHooksDir, sel)
	if err := WriteJSONFile(hooksPath, data); err != nil {
		return "", err
	}
	return hooksPath, nil
}

// CursorEject removes memory skill and hooks from the given Cursor config dir.
func CursorEject(configDir string) []error {
	var errs []error

	fmt.Printf("\nRemoving Cursor integration (%s)...\n", configDir)

	hooksDir := filepath.Join(configDir, "hooks", "memory")
	if err := os.RemoveAll(hooksDir); err != nil {
		StatusError(1, 3, "Hooks", err)
		errs = append(errs, err)
	} else {
		StatusOK(1, 3, "Hooks", hooksDir+" removed")
	}
	removeIfEmpty(filepath.Join(configDir, "hooks"))

	hooksPath := filepath.Join(configDir, "hooks.json")
	data, err := ReadJSONFile(hooksPath)
	if err != nil {
		StatusError(2, 3, "Hooks config", err)
		errs = append(errs, err)
	} else {
		removeCursorHooks(data)
		if err := WriteOrRemoveJSONFile(hooksPath, data); err != nil {
			StatusError(2, 3, "Hooks config", err)
			errs = append(errs, err)
		} else {
			StatusOK(2, 3, "Hooks config", hooksPath+" cleaned")
		}
	}

	skillDir := filepath.Join(configDir, "skills", "memory")
	if err := os.RemoveAll(skillDir); err != nil {
		StatusError(3, 3, "Skill", err)
		errs = append(errs, err)
	} else {
		StatusOK(3, 3, "Skill", skillDir+" removed")
	}
	removeIfEmpty(filepath.Join(configDir, "skills"))
	removeIfEmpty(configDir)

	return errs
}

func addCursorHooks(data map[string]interface{}, hooksDir string, sel HookSelection) {
	removeCursorHooks(data)
	if _, ok := data["version"]; !ok {
		data["version"] = 1
	}
	hooks := ensureHooksMap(data)

	sessionEntry := map[string]interface{}{
		"command": filepath.Join(hooksDir, "prime.sh"),
		"timeout": 30,
	}
	sessionArr, _ := hooks["sessionStart"].([]interface{})
	hooks["sessionStart"] = append(sessionArr, sessionEntry)

	if sel.Nudge {
		stopEntry := map[string]interface{}{
			"command":    filepath.Join(hooksDir, "stop.sh"),
			"timeout":    30,
			"loop_limit": 1,
		}
		stopArr, _ := hooks["stop"].([]interface{})
		hooks["stop"] = append(stopArr, stopEntry)
	}

	if sel.Compact {
		compactEntry := map[string]interface{}{
			"command": filepath.Join(hooksDir, "compact.sh"),
			"timeout": 30,
		}
		compactArr, _ := hooks["preCompact"].([]interface{})
		hooks["preCompact"] = append(compactArr, compactEntry)
	}
}

func removeCursorHooks(data map[string]interface{}) {
	hooks, ok := data["hooks"].(map[string]interface{})
	if !ok {
		return
	}
	for _, key := range []string{"sessionStart", "stop", "preCompact"} {
		arr, ok := hooks[key].([]interface{})
		if !ok {
			continue
		}
		filtered := filterHookArray(arr)
		if len(filtered) == 0 {
			delete(hooks, key)
		} else {
			hooks[key] = filtered
		}
	}
	if len(hooks) == 0 {
		delete(data, "hooks")
		if _, ok := data["version"]; ok && len(data) == 1 {
			delete(data, "version")
		}
	}
}
