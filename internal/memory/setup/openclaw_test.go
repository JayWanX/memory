package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

func TestOpenClawRegisterPluginWritesSelection(t *testing.T) {
	dir := t.TempDir()
	path, err := OpenClawRegisterPlugin(dir, HookSelection{Remind: true, Nudge: false, Compact: true})
	if err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	plugins := cfg["plugins"].(map[string]any)
	entries := plugins["entries"].(map[string]any)
	memory := entries["memory"].(map[string]any)
	config := memory["config"].(map[string]any)

	if memory["enabled"] != true {
		t.Fatalf("memory should be enabled: %#v", memory["enabled"])
	}
	if config["remind"] != true || config["nudge"] != false || config["compact"] != true {
		t.Fatalf("unexpected hook selection: %#v", config)
	}
}

func TestRemoveOpenClawPluginEntryPreservesOtherEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openclaw.json")
	input := `{
  "plugins": {
    "entries": {
      "memory": {"enabled": true},
      "other": {"enabled": true}
    }
  },
  "theme": "dark"
}
`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := removeOpenClawPluginEntry(path); err != nil {
		t.Fatalf("remove plugin entry: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	entries := cfg["plugins"].(map[string]any)["entries"].(map[string]any)
	if _, ok := entries["memory"]; ok {
		t.Fatal("memory entry should be removed")
	}
	if _, ok := entries["other"]; !ok {
		t.Fatal("other entry should be preserved")
	}
	if cfg["theme"] != "dark" {
		t.Fatalf("top-level fields should be preserved: %#v", cfg)
	}
}

func TestRemoveOpenClawPluginEntryRemovesEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openclaw.json")
	if err := os.WriteFile(path, []byte(`{"plugins":{"entries":{"memory":{"enabled":true}}}}`), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := removeOpenClawPluginEntry(path); err != nil {
		t.Fatalf("remove plugin entry: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected empty config file removal, got err=%v", err)
	}
}

func TestOpenClawPrimeHookResolvesMemoryDataDir(t *testing.T) {
	handler := string(assets.OpenClawHookHandler)
	for _, want := range []string{
		"process.env.MEMORY_DATA_DIR",
		"LEGACY_GUIDE_PATH",
		"existsSync(scopedPath)",
		"readFileSync(guidePath()",
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("OpenClaw prime hook missing %q", want)
		}
	}
}
