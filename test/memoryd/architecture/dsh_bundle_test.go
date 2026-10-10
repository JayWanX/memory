package architecture_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dshGitHubInstallCommand = "dsh plugin --profile web add github:JayWanX/memory"

func TestDSHGitHubBundleContract(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
		DSH          struct {
			Bundle struct {
				Patch string `json:"patch"`
			} `json:"bundle"`
		} `json:"dsh"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if manifest.Name != "@jaywanx/dsh-memory-bundle" {
		t.Errorf("package name = %q", manifest.Name)
	}
	if manifest.Dependencies["@jaywanx/dsh-memory"] != "latest" {
		t.Errorf("@jaywanx/dsh-memory dependency = %q, want latest", manifest.Dependencies["@jaywanx/dsh-memory"])
	}
	if manifest.DSH.Bundle.Patch != "./cordis.patch.yml" {
		t.Errorf("bundle patch = %q", manifest.DSH.Bundle.Patch)
	}
	assertFileContains(t, root, "cordis.patch.yml", `name: "@jaywanx/dsh-memory"`)
	assertFileContains(t, root, "README.md", dshGitHubInstallCommand)
	assertFileContains(t, root, "docs/zh/README.md", dshGitHubInstallCommand)
}

func assertFileContains(t *testing.T, root, name, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if !strings.Contains(string(raw), want) {
		t.Errorf("%s does not contain %q", name, want)
	}
}
