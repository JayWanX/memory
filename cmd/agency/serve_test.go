//go:build !windows

package agency

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveStateDirectoryReturnsThePhysicalDirectory(t *testing.T) {
	state := canonicalDirectory(t)
	linkRoot := canonicalDirectory(t)
	link := filepath.Join(linkRoot, "state-link")
	if err := os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveStateDirectory(link)
	if err != nil || resolved != state {
		t.Fatalf("resolveStateDirectory(%q) = %q, %v", link, resolved, err)
	}
}

func TestServeRejectsMalformedInputBeforeOpeningAuthority(t *testing.T) {
	state := canonicalDirectory(t)
	for _, args := range [][]string{
		{"serve"},
		{"serve", "--state-dir", state, "--state-dir", state},
	} {
		stdout, stderr, exit := executeAgency(args, "", "dev")
		if exit != 2 || stdout != "" || stderr == "" {
			t.Fatalf("invalid serve %q = exit %d stdout %q stderr %q", args, exit, stdout, stderr)
		}
	}

	missing := filepath.Join(t.TempDir(), "missing")
	stdout, stderr, exit := executeAgency([]string{"serve", "--state-dir", missing}, "", "dev")
	if exit != 1 || stdout != "" || stderr == "" {
		t.Fatalf("missing directory = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
}

func TestServeHonorsCancellationBeforeOpeningAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveDaemon(ctx, canonicalDirectory(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled serve = %v", err)
	}
}

func canonicalDirectory(t *testing.T) string {
	t.Helper()
	temporary, err := os.MkdirTemp("/tmp", "memoryd-command-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	directory, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(directory)
}
