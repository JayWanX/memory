package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/JayWanX/memory/internal/memory/setup/assets"
)

func TestHostSkillAndHookArtifacts(t *testing.T) {
	type writeSkill func(string) (string, error)
	type writeHook func(string, string, []byte) (string, error)

	tests := []struct {
		name       string
		skill      []byte
		writeSkill writeSkill
		writeHook  writeHook
	}{
		{name: "CodeBuddy", skill: assets.CodeBuddySkill, writeSkill: CodeBuddyWriteSkill, writeHook: CodeBuddyWriteHook},
		{name: "Cursor", skill: assets.CursorSkill, writeSkill: CursorWriteSkill, writeHook: CursorWriteHook},
		{name: "Kimi", skill: assets.KimiSkill, writeSkill: KimiWriteSkill, writeHook: KimiWriteHook},
		{name: "Trae", skill: assets.TraeSkill, writeSkill: TraeWriteSkill, writeHook: TraeWriteHook},
		{name: "WorkBuddy", skill: assets.WorkBuddySkill, writeSkill: WorkBuddyWriteSkill, writeHook: WorkBuddyWriteHook},
		{name: "ZCode", skill: assets.ZCodeSkill, writeSkill: ZCodeWriteSkill, writeHook: ZCodeWriteHook},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()

			skillPath, err := test.writeSkill(dir)
			if err != nil {
				t.Fatalf("write skill: %v", err)
			}
			if want := filepath.Join(dir, "skills", "memory", "SKILL.md"); skillPath != want {
				t.Fatalf("skill path = %q, want %q", skillPath, want)
			}
			skill, err := os.ReadFile(skillPath)
			if err != nil {
				t.Fatalf("read skill: %v", err)
			}
			if !bytes.Equal(skill, test.skill) {
				t.Fatalf("skill content differs from embedded asset")
			}

			hook := []byte("#!/bin/bash\n")
			hookPath, err := test.writeHook(dir, "prime.sh", hook)
			if err != nil {
				t.Fatalf("write hook: %v", err)
			}
			if want := filepath.Join(dir, "hooks", "memory", "prime.sh"); hookPath != want {
				t.Fatalf("hook path = %q, want %q", hookPath, want)
			}
			info, err := os.Stat(hookPath)
			if err != nil {
				t.Fatalf("stat hook: %v", err)
			}
			if info.Mode().Perm() != 0o755 {
				t.Fatalf("hook permissions = %v, want 0755", info.Mode().Perm())
			}
			writtenHook, err := os.ReadFile(hookPath)
			if err != nil {
				t.Fatalf("read hook: %v", err)
			}
			if !bytes.Equal(writtenHook, hook) {
				t.Fatalf("hook content changed while writing")
			}
		})
	}
}
