package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

func TestPiWriteSkillAndExtension(t *testing.T) {
	dir := t.TempDir()

	skillPath, err := PiWriteSkill(dir)
	if err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if skillPath != filepath.Join(dir, "skills", "memory", "SKILL.md") {
		t.Fatalf("skill path = %q", skillPath)
	}
	if data, err := os.ReadFile(skillPath); err != nil {
		t.Fatalf("read skill: %v", err)
	} else if !strings.Contains(string(data), "name: memory") {
		t.Fatalf("unexpected skill content: %q", string(data))
	}

	extPath, err := PiWriteExtension(dir)
	if err != nil {
		t.Fatalf("write extension: %v", err)
	}
	if extPath != filepath.Join(dir, "extensions", "memory.ts") {
		t.Fatalf("extension path = %q", extPath)
	}
	if data, err := os.ReadFile(extPath); err != nil {
		t.Fatalf("read extension: %v", err)
	} else if !strings.Contains(string(data), `pi.on("before_agent_start"`) {
		t.Fatalf("unexpected extension content: %q", string(data))
	}
}

func TestPiExtensionMapsLifecycleEvents(t *testing.T) {
	extension := string(assets.PiExtension)
	for _, want := range []string{
		`pi.on("resources_discover"`,
		`pi.on("session_start"`,
		`pi.on("before_agent_start"`,
		`pi.on("agent_end"`,
		`pi.on("session_before_compact"`,
		"process.env.MEMORY_DATA_DIR",
		"process.env.PI_CODING_AGENT_DIR",
	} {
		if !strings.Contains(extension, want) {
			t.Fatalf("Pi extension missing %q", want)
		}
	}
}

func TestPiEjectRemovesOnlyMemoryFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := PiWriteSkill(dir); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if _, err := PiWriteExtension(dir); err != nil {
		t.Fatalf("write extension: %v", err)
	}
	other := filepath.Join(dir, "extensions", "custom.ts")
	if err := os.WriteFile(other, []byte("export default function () {}\n"), 0644); err != nil {
		t.Fatalf("write custom extension: %v", err)
	}

	errs := PiEject(dir)
	if len(errs) > 0 {
		t.Fatalf("eject errors: %v", errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "memory")); !os.IsNotExist(err) {
		t.Fatalf("memory skill should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extensions", "memory.ts")); !os.IsNotExist(err) {
		t.Fatalf("memory extension should be removed, err=%v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("custom extension should be preserved: %v", err)
	}
}
