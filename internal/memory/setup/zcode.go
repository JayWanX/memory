package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

// ZCodeWriteSkill writes the memory skill to the ZCode skills directory.
func ZCodeWriteSkill(configDir string) (string, error) {
	skillDir := filepath.Join(configDir, "skills", "memory")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", err
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, assets.ZCodeSkill, 0o644); err != nil {
		return "", err
	}
	return skillPath, nil
}

// ZCodeWriteHook writes a hook script to the ZCode hooks directory.
func ZCodeWriteHook(configDir, filename string, content []byte) (string, error) {
	hooksDir := filepath.Join(configDir, "hooks", "memory")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", err
	}
	hookPath := filepath.Join(hooksDir, filename)
	if err := writeExecutableFile(hookPath, content); err != nil {
		return "", err
	}
	return hookPath, nil
}

// ZCodeRegisterHooks registers user-level Memory hooks in cli/config.json.
func ZCodeRegisterHooks(configDir string) (string, error) {
	return zcodeRegisterHooks(configDir, runtime.GOOS)
}

func zcodeRegisterHooks(configDir, goos string) (string, error) {
	hooksDir := filepath.Join(configDir, "hooks", "memory")
	absHooksDir, err := filepath.Abs(hooksDir)
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(configDir, "cli", "config.json")
	data, err := ReadJSONFile(configPath)
	if err != nil {
		return "", err
	}
	addZCodeHooks(data, absHooksDir, goos)
	if err := WriteJSONFile(configPath, data); err != nil {
		return "", err
	}
	return configPath, nil
}

// ZCodeEject removes the Memory skill and user-level hooks from ZCode.
func ZCodeEject(configDir string) []error {
	var errs []error

	fmt.Printf("\nRemoving ZCode integration (%s)...\n", configDir)

	hooksDir := filepath.Join(configDir, "hooks", "memory")
	if err := os.RemoveAll(hooksDir); err != nil {
		StatusError(1, 3, "Hooks", err)
		errs = append(errs, err)
	} else {
		StatusOK(1, 3, "Hooks", hooksDir+" removed")
	}
	removeIfEmpty(filepath.Join(configDir, "hooks"))

	configPath := filepath.Join(configDir, "cli", "config.json")
	data, err := ReadJSONFile(configPath)
	if err != nil {
		StatusError(2, 3, "Hooks config", err)
		errs = append(errs, err)
	} else {
		removeZCodeHooks(data)
		if err := WriteOrRemoveJSONFile(configPath, data); err != nil {
			StatusError(2, 3, "Hooks config", err)
			errs = append(errs, err)
		} else {
			StatusOK(2, 3, "Hooks config", configPath+" cleaned")
		}
	}
	removeIfEmpty(filepath.Join(configDir, "cli"))

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

func addZCodeHooks(data map[string]interface{}, hooksDir, goos string) {
	removeZCodeHooks(data)
	hooks := ensureHooksMap(data)
	hooks["enabled"] = true

	events, ok := hooks["events"].(map[string]interface{})
	if !ok {
		events = make(map[string]interface{})
		hooks["events"] = events
	}

	events["SessionStart"] = appendZCodeHook(events["SessionStart"], map[string]interface{}{
		"matcher": "startup|clear|compact",
		"hooks": []interface{}{
			zcodeProcessHook(filepath.Join(hooksDir, zcodeHookFilename("prime", goos)), "Loading Memory context", goos),
		},
	})
	events["UserPromptSubmit"] = appendZCodeHook(events["UserPromptSubmit"], map[string]interface{}{
		"hooks": []interface{}{
			zcodeProcessHook(filepath.Join(hooksDir, zcodeHookFilename("user_prompt", goos)), "Checking Memory recall guidance", goos),
		},
	})
	events["Stop"] = appendZCodeHook(events["Stop"], map[string]interface{}{
		"hooks": []interface{}{
			zcodeProcessHook(filepath.Join(hooksDir, zcodeHookFilename("stop", goos)), "Checking Memory writeback guidance", goos),
		},
	})
}

func appendZCodeHook(current interface{}, entry map[string]interface{}) []interface{} {
	entries, _ := current.([]interface{})
	return append(entries, entry)
}

func zcodeHookFilename(base, goos string) string {
	if goos == "windows" {
		return base + ".ps1"
	}
	return base + ".sh"
}

func zcodeProcessHook(scriptPath, status, goos string) map[string]interface{} {
	command := "bash"
	args := []interface{}{scriptPath}
	if goos == "windows" {
		command = "powershell.exe"
		args = []interface{}{
			"-NoProfile",
			"-NonInteractive",
			"-ExecutionPolicy",
			"Bypass",
			"-File",
			scriptPath,
		}
	}
	return map[string]interface{}{
		"type":          "process",
		"command":       command,
		"args":          args,
		"enabled":       true,
		"timeoutMs":     30000,
		"statusMessage": status,
	}
}

func removeZCodeHooks(data map[string]interface{}) {
	hooks, ok := data["hooks"].(map[string]interface{})
	if !ok {
		return
	}
	events, ok := hooks["events"].(map[string]interface{})
	if !ok {
		return
	}
	for _, key := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		entries, ok := events[key].([]interface{})
		if !ok {
			continue
		}
		filtered := filterHookArray(entries)
		if len(filtered) == 0 {
			delete(events, key)
		} else {
			events[key] = filtered
		}
	}
	if len(events) == 0 {
		delete(hooks, "events")
	}
	if len(hooks) == 1 && hooks["enabled"] == true {
		delete(data, "hooks")
	}
}
