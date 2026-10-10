//go:build !windows

package agency

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRejectsInvalidOptionsBeforeCreatingState(t *testing.T) {
	project := physicalTempRoot(t)
	for _, args := range [][]string{
		{"setup", "--runtime", "codex", "--project-root", project},
		{"setup", "--runtime", "pi", "--runtime", "pi", "--project-root", project},
		{"setup", "--project-root", ""},
		{"setup", "--project-root", project, "--project-root", project},
		{"setup", "--project-root", project, "extra"},
		{"setup", "--project-root", project, "--unknown", "value"},
	} {
		stdout, stderr, exit := executeAgency(args, "", "dev")
		if exit != 2 || stdout != "" || stderr == "" {
			t.Fatalf("invalid setup %q = exit %d stdout %q stderr %q",
				args, exit, stdout, stderr)
		}
	}
	if _, err := os.Lstat(filepath.Join(project, ".memory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid setup created project state: %v", err)
	}
}

func TestSetupProjectHonorsCancellationBeforeProvision(t *testing.T) {
	project := physicalTempRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := setupProject(ctx, project)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled setup = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(project, ".memory")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled setup created project state: %v", statErr)
	}
}

func TestSetupHelpNamesOnlySupportedRuntime(t *testing.T) {
	stdout, stderr, exit := executeAgency([]string{"setup", "--help"}, "", "dev")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, "Agent Runtime to integrate (pi)") {
		t.Fatalf("setup help = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
}
