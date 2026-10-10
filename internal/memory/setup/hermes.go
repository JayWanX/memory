package setup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
	"go.yaml.in/yaml/v3"
)

// HermesWriteSkill writes the memory skill to the Hermes skills directory.
func HermesWriteSkill(configDir string) (string, error) {
	skillDir := filepath.Join(configDir, "skills", "memory")
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(skillPath, assets.HermesSkill, 0644); err != nil {
		return "", err
	}
	return skillPath, nil
}

// HermesWriteHook writes a shell hook script to the Hermes agent-hooks directory.
func HermesWriteHook(configDir, filename string, content []byte) (string, error) {
	hooksDir := filepath.Join(configDir, "agent-hooks", "memory")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return "", err
	}
	hookPath := filepath.Join(hooksDir, filename)
	if err := writeExecutableFile(hookPath, content); err != nil {
		return "", err
	}
	return hookPath, nil
}

// HermesRegisterHooks registers selected Memory lifecycle hooks in config.yaml.
func HermesRegisterHooks(configDir string, sel HookSelection) (string, error) {
	hooksDir := filepath.Join(configDir, "agent-hooks", "memory")
	absHooksDir, err := filepath.Abs(hooksDir)
	if err != nil {
		return "", err
	}

	configPath := filepath.Join(configDir, "config.yaml")
	data, err := readYAMLFile(configPath)
	if err != nil {
		return "", err
	}
	addHermesHooks(data, absHooksDir, sel)
	if err := writeYAMLFile(configPath, data); err != nil {
		return "", err
	}
	return configPath, nil
}

// HermesEject removes memory integration from the given Hermes config dir.
func HermesEject(configDir string) []error {
	var errs []error

	fmt.Printf("\nRemoving Hermes Agent integration (%s)...\n", configDir)

	hooksDir := filepath.Join(configDir, "agent-hooks", "memory")
	if err := os.RemoveAll(hooksDir); err != nil {
		StatusError(1, 4, "Hooks", err)
		errs = append(errs, err)
	} else {
		StatusOK(1, 4, "Hooks", hooksDir+" removed")
	}
	removeIfEmpty(filepath.Join(configDir, "agent-hooks"))

	configPath := filepath.Join(configDir, "config.yaml")
	data, err := readYAMLFile(configPath)
	if err != nil {
		StatusError(2, 4, "Config", err)
		errs = append(errs, err)
	} else {
		removeHermesHooks(data)
		if err := writeOrRemoveYAMLFile(configPath, data); err != nil {
			StatusError(2, 4, "Config", err)
			errs = append(errs, err)
		} else {
			StatusOK(2, 4, "Config", configPath+" cleaned")
		}
	}

	skillDir := filepath.Join(configDir, "skills", "memory")
	if err := os.RemoveAll(skillDir); err != nil {
		StatusError(3, 4, "Skill", err)
		errs = append(errs, err)
	} else {
		StatusOK(3, 4, "Skill", skillDir+" removed")
	}
	removeIfEmpty(filepath.Join(configDir, "skills"))

	stateDir := filepath.Join(configDir, "memory")
	if err := os.RemoveAll(stateDir); err != nil {
		StatusError(4, 4, "State", err)
		errs = append(errs, err)
	} else {
		StatusOK(4, 4, "State", stateDir+" removed")
	}
	removeIfEmpty(configDir)

	return errs
}

func readYAMLFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return make(map[string]any), nil
	}

	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = make(map[string]any)
	}
	return out, nil
}

func writeYAMLFile(path string, data map[string]any) error {
	out, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeOrRemoveYAMLFile(path string, data map[string]any) error {
	if len(data) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeYAMLFile(path, data)
}

func addHermesHooks(data map[string]any, hooksDir string, sel HookSelection) {
	removeHermesHooks(data)
	hooks := ensureAnyMap(data, "hooks")

	appendHermesHook(hooks, "on_session_start", filepath.Join(hooksDir, "prime.sh"), 10)
	if sel.Remind {
		appendHermesHook(hooks, "pre_llm_call", filepath.Join(hooksDir, "remind.sh"), 10)
	}
	if sel.Nudge {
		appendHermesHook(hooks, "post_llm_call", filepath.Join(hooksDir, "nudge.sh"), 10)
	}
	if sel.Compact {
		appendHermesHook(hooks, "on_session_finalize", filepath.Join(hooksDir, "compact.sh"), 30)
	}
}

func removeHermesHooks(data map[string]any) {
	hooks, ok := data["hooks"].(map[string]any)
	if !ok {
		return
	}
	for _, event := range []string{"on_session_start", "pre_llm_call", "post_llm_call", "on_session_finalize"} {
		raw, ok := hooks[event]
		if !ok {
			continue
		}
		entries, ok := raw.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(entries))
		for _, entry := range entries {
			if !containsMemory(entry) {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(data, "hooks")
	}
}

func ensureAnyMap(data map[string]any, key string) map[string]any {
	existing, ok := data[key].(map[string]any)
	if ok {
		return existing
	}
	next := make(map[string]any)
	data[key] = next
	return next
}

func appendHermesHook(hooks map[string]any, event, command string, timeout int) {
	entry := map[string]any{
		"command": command,
		"timeout": timeout,
	}
	arr, _ := hooks[event].([]any)
	hooks[event] = append(arr, entry)
}
