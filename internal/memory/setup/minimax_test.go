package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

func TestMiniMaxCodeWriteSkill(t *testing.T) {
	dir := t.TempDir()

	skillPath, err := MiniMaxCodeWriteSkill(dir)
	if err != nil {
		t.Fatalf("write skill: %v", err)
	}
	wantPath := filepath.Join(dir, "skills", "memory", "SKILL.md")
	if skillPath != wantPath {
		t.Fatalf("skill path = %q, want %q", skillPath, wantPath)
	}
	written, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if !bytes.Equal(written, assets.MiniMaxCodeSkill) {
		t.Fatal("skill content differs from embedded asset")
	}
}

func TestMiniMaxCodeEjectPreservesOtherSkills(t *testing.T) {
	dir := t.TempDir()
	if _, err := MiniMaxCodeWriteSkill(dir); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	customSkill := filepath.Join(dir, "skills", "custom", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(customSkill), 0o755); err != nil {
		t.Fatalf("create custom skill dir: %v", err)
	}
	if err := os.WriteFile(customSkill, []byte("custom"), 0o644); err != nil {
		t.Fatalf("write custom skill: %v", err)
	}

	if errs := MiniMaxCodeEject(dir); len(errs) > 0 {
		t.Fatalf("eject errors: %v", errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "memory")); !os.IsNotExist(err) {
		t.Fatalf("memory skill should be removed, err=%v", err)
	}
	if _, err := os.Stat(customSkill); err != nil {
		t.Fatalf("custom skill should be preserved: %v", err)
	}
}

func TestDetectMiniMaxCodeRecognizesCurrentAndLegacyDataDirs(t *testing.T) {
	for _, dataDir := range []string{".minimax", ".mavis"} {
		t.Run(dataDir, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := os.Mkdir(filepath.Join(home, dataDir), 0o755); err != nil {
				t.Fatalf("create data dir: %v", err)
			}

			env := detectMiniMaxCode(true)
			if !env.Detected {
				t.Fatalf("MiniMax Code should be detected from %s", dataDir)
			}
			if want := filepath.Join(home, ".minimax"); env.ConfigDir != want {
				t.Fatalf("config dir = %q, want current path %q", env.ConfigDir, want)
			}
		})
	}
}
