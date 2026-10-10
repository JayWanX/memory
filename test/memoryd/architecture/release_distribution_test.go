package architecture_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReleaseDistributionPublishesWindowsArchives(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var config struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			GOOS   []string `yaml:"goos"`
			GOARCH []string `yaml:"goarch"`
		} `yaml:"builds"`
		Archives []struct {
			IDs             []string `yaml:"ids"`
			Formats         []string `yaml:"formats"`
			FormatOverrides []struct {
				GOOS    string   `yaml:"goos"`
				Formats []string `yaml:"formats"`
			} `yaml:"format_overrides"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatalf("parse .goreleaser.yml: %v", err)
	}

	if len(config.Builds) != 1 {
		t.Fatalf("release builds = %d, want one product build", len(config.Builds))
	}
	build := config.Builds[0]
	if build.ID != "memory" {
		t.Fatalf("release build ID = %q, want memory", build.ID)
	}
	if want := []string{"linux", "darwin", "windows"}; !slices.Equal(build.GOOS, want) {
		t.Fatalf("memory release operating systems = %v, want %v", build.GOOS, want)
	}
	if want := []string{"amd64", "arm64"}; !slices.Equal(build.GOARCH, want) {
		t.Fatalf("memory release architectures = %v, want %v", build.GOARCH, want)
	}

	if len(config.Archives) != 1 {
		t.Fatalf("release archives = %d, want one product archive", len(config.Archives))
	}
	archive := config.Archives[0]
	if !slices.Equal(archive.IDs, []string{"memory"}) {
		t.Fatalf("archive build IDs = %v, want [memory]", archive.IDs)
	}
	if !slices.Equal(archive.Formats, []string{"tar.gz"}) {
		t.Fatalf("default archive formats = %v, want [tar.gz]", archive.Formats)
	}
	if len(archive.FormatOverrides) != 1 {
		t.Fatalf("archive format overrides = %d, want one Windows override", len(archive.FormatOverrides))
	}
	windows := archive.FormatOverrides[0]
	if windows.GOOS != "windows" || !slices.Equal(windows.Formats, []string{"zip"}) {
		t.Fatalf("archive format override = (%q, %v), want (windows, [zip])",
			windows.GOOS, windows.Formats)
	}
}

func TestReleaseCaskEmitsPostflightSteps(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var config struct {
		Casks []struct {
			Name        string `yaml:"name"`
			CustomBlock string `yaml:"custom_block"`
			Hooks       struct {
				Post struct {
					Install string `yaml:"install"`
				} `yaml:"post"`
			} `yaml:"hooks"`
		} `yaml:"homebrew_casks"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatalf("parse .goreleaser.yml: %v", err)
	}

	if len(config.Casks) != 1 {
		t.Fatalf("homebrew casks = %d, want one", len(config.Casks))
	}
	cask := config.Casks[0]

	// hooks.post.install renders Homebrew's deprecated `postflight` stanza
	// (goreleaser/goreleaser#6870); the quarantine strip must live in
	// custom_block as `postflight_steps` until a steps-aware hook ships.
	if cask.Hooks.Post.Install != "" {
		t.Fatalf("homebrew cask still uses hooks.post.install, which emits the deprecated postflight stanza")
	}
	for _, want := range []string{"postflight_steps", "on_macos", "/usr/bin/xattr", `{{ "{{staged_path}}" }}`} {
		if !strings.Contains(cask.CustomBlock, want) {
			t.Fatalf("homebrew cask custom_block is missing %q; got:\n%s", want, cask.CustomBlock)
		}
	}
	if strings.Contains(cask.CustomBlock, "postflight do") {
		t.Fatalf("homebrew cask custom_block still emits deprecated postflight:\n%s", cask.CustomBlock)
	}
}
