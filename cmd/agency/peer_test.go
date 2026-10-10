//go:build !windows

package agency

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/daemon"
)

func TestPeerPrepareAndEnrollUseTheDeclaredCommandPath(t *testing.T) {
	localRoot := physicalTempRoot(t)
	remoteRoot := physicalTempRoot(t)

	localCard, stderr, exit := executeAgency([]string{"peer", "prepare",
		"--listen", "127.0.0.1:41001", "--advertise", "peer-a.invalid:41001",
		"--project-root", localRoot}, "", "dev")
	if exit != 0 || stderr != "" {
		t.Fatalf("local prepare = exit %d stdout %q stderr %q", exit, localCard, stderr)
	}
	if _, err := daemon.ParsePeerCardCanonicalJSON(bytes.TrimSuffix([]byte(localCard), []byte{'\n'})); err != nil {
		t.Fatalf("local Peer Card: %v", err)
	}

	remoteCard, stderr, exit := executeAgency([]string{"peer", "prepare",
		"--listen", "127.0.0.1:41002", "--advertise", "peer-b.invalid:41002",
		"--project-root", remoteRoot}, "", "dev")
	if exit != 0 || stderr != "" {
		t.Fatalf("remote prepare = exit %d stdout %q stderr %q", exit, remoteCard, stderr)
	}

	receipt, stderr, exit := executeAgency([]string{"peer", "enroll",
		"--alias", "target:peer-b", "--project-root", localRoot}, remoteCard, "dev")
	if exit != 0 || stderr != "" {
		t.Fatalf("enroll = exit %d stdout %q stderr %q", exit, receipt, stderr)
	}
	var projection struct {
		Status string `json:"status"`
		Alias  string `json:"alias"`
	}
	if err := json.Unmarshal([]byte(receipt), &projection); err != nil ||
		projection.Status != "enrolled" || projection.Alias != "target:peer-b" {
		t.Fatalf("enrollment projection = %#v error %v", projection, err)
	}
}

func TestPeerRejectsMalformedOptionsBeforeCreatingState(t *testing.T) {
	project := physicalTempRoot(t)
	for _, args := range [][]string{
		{"peer", "prepare", "--listen", "127.0.0.1:1", "--project-root", project},
		{"peer", "prepare", "--listen", "a:1", "--listen", "a:1", "--advertise", "b:2", "--project-root", project},
		{"peer", "prepare", "--listen", "a:1", "--advertise", "b:2", "--advertise", "b:2", "--project-root", project},
		{"peer", "prepare", "--listen", "a:1", "--advertise", "b:2", "--project-root", ""},
		{"peer", "enroll", "--project-root", project},
		{"peer", "enroll", "--alias", "target:a", "--alias", "target:a", "--project-root", project},
		{"peer", "enroll", "--alias", "target:a", "--project-root", project},
		{"peer", "enroll", "--alias", "target:a", "--project-root", ""},
		{"peer", "enroll", "--alias", "target:a", "--project-root", project, "--project-root", project},
		{"peer", "connect", "--project-root", project},
	} {
		stdout, stderr, exit := executeAgency(args, "", "dev")
		if exit != 2 || stdout != "" || stderr == "" {
			t.Fatalf("invalid peer %q = exit %d stdout %q stderr %q",
				args, exit, stdout, stderr)
		}
	}
	if _, err := os.Lstat(filepath.Join(project, ".memory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid peer command created project state: %v", err)
	}
}

func TestReadPeerCardAcceptsOnlyCanonicalBodyWithOneOptionalLF(t *testing.T) {
	root := physicalTempRoot(t)
	stdout, stderr, exit := executeAgency([]string{"peer", "prepare",
		"--listen", "127.0.0.1:42001", "--advertise", "peer.invalid:42001",
		"--project-root", root}, "", "dev")
	if exit != 0 || stderr != "" {
		t.Fatalf("prepare = exit %d stderr %q", exit, stderr)
	}
	canonical := bytes.TrimSuffix([]byte(stdout), []byte{'\n'})
	for _, input := range [][]byte{canonical, append(append([]byte(nil), canonical...), '\n')} {
		card, err := readPeerCard(bytes.NewReader(input))
		if err != nil || card.PeerID().IsZero() {
			t.Fatalf("readPeerCard(valid) = peer %s error %v", card.PeerID(), err)
		}
	}
	for _, suffix := range []string{"\r\n", "\n\n", " "} {
		input := append(append([]byte(nil), canonical...), suffix...)
		if _, err := readPeerCard(bytes.NewReader(input)); err == nil {
			t.Fatalf("readPeerCard accepted non-canonical suffix %q", suffix)
		}
	}
	if _, err := readPeerCard(strings.NewReader("")); err == nil {
		t.Fatal("readPeerCard accepted empty input")
	}
	maxCanonical := append(bytes.Repeat([]byte{'x'}, maxPeerCardInputBytes-1), '\n')
	if _, err := readPeerCard(bytes.NewReader(maxCanonical)); err == nil ||
		!strings.Contains(err.Error(), "parse Peer Card") {
		t.Fatalf("maximum malformed input = %v", err)
	}
	oversized := append(bytes.Repeat([]byte{'x'}, maxPeerCardInputBytes), '\n')
	if _, err := readPeerCard(bytes.NewReader(oversized)); err == nil ||
		!strings.Contains(err.Error(), "canonical Peer Card") {
		t.Fatalf("oversized input = %v", err)
	}
}

func physicalTempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
