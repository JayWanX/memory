package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestDefaultAgentPrincipalUsesIndependentFullDigestDomain(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	principal, err := DefaultAgentPrincipal(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	want := "principal:r7:" + agency.Sum(append(
		[]byte("memory.r7.default-agent-principal.v1\x00"), publicKey...)).String()[len("sha256:"):]
	if principal.String() != want || len(principal.String()) != len("principal:r7:")+64 {
		t.Fatalf("Principal = %q, want %q", principal.String(), want)
	}
	peer, err := derivePeerIdentity(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if principal.String()[len("principal:r7:"):] == peer.String()[len("peer:"):] {
		t.Fatal("Principal and PeerID reused one derivation domain")
	}
	if _, err := DefaultAgentPrincipal(publicKey[:8]); err == nil {
		t.Fatal("short public key derived a Principal")
	}
}

func TestTransportIdentityProvisionIsDurableAndReplayable(t *testing.T) {
	state, _ := provisionDaemonState(t)
	first, err := ProvisionTransportIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProvisionTransportIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	if first.PeerID() != second.PeerID() || !bytes.Equal(first.PublicKey(), second.PublicKey()) {
		t.Fatal("identity replay changed the node identity")
	}
	path := filepath.Join(state, transportIdentityFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != ownerFileMode {
		t.Fatalf("identity file = (%v, %v)", info, err)
	}
	loaded, err := loadTransportIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.projection.PeerID() != first.PeerID() ||
		!bytes.Equal(loaded.projection.PublicKey(), first.PublicKey()) {
		t.Fatal("loaded identity diverged from setup projection")
	}
}

func TestTransportIdentityRecoversCommittedPendingFile(t *testing.T) {
	state, _ := provisionDaemonState(t)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, expected, err := encodeTransportIdentity(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(state, transportIdentityPending)
	if err := createPrivateFile(pending, raw); err != nil {
		t.Fatal(err)
	}
	actual, err := ProvisionTransportIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	if actual.PeerID() != expected.projection.PeerID() ||
		!bytes.Equal(actual.PublicKey(), expected.projection.PublicKey()) {
		t.Fatal("pending identity recovery changed the identity")
	}
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending identity survived recovery: %v", err)
	}
}

func TestTransportIdentityDiscardsInterruptedPartialPending(t *testing.T) {
	state := canonicalTempDir(t)
	pending := filepath.Join(state, transportIdentityPending)
	if err := os.WriteFile(pending, []byte(`{"schema":"memory.r7`), ownerFileMode); err != nil {
		t.Fatal(err)
	}
	identity, err := ProvisionTransportIdentity(state)
	if err != nil || identity.PeerID().IsZero() {
		t.Fatalf("recover partial pending = (%#v, %v)", identity, err)
	}
	if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial pending survived recovery: %v", err)
	}
	loaded, err := loadTransportIdentity(state)
	if err != nil || loaded.projection.PeerID() != identity.PeerID() {
		t.Fatalf("recovered committed identity = (%#v, %v)", loaded, err)
	}
}

func TestTransportIdentityRecoversInterruptedHardLinkPublication(t *testing.T) {
	state, _ := provisionDaemonState(t)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, expected, err := encodeTransportIdentity(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(state, transportIdentityPending)
	final := filepath.Join(state, transportIdentityFile)
	if err := createPrivateFile(pending, raw); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(pending, final); err != nil {
		t.Fatal(err)
	}
	actual, err := ProvisionTransportIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	if actual.PeerID() != expected.projection.PeerID() {
		t.Fatal("hard-link recovery changed the identity")
	}
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatalf("linked pending identity survived recovery: %v", err)
	}
}

func TestTransportIdentityConcurrentProvisionConverges(t *testing.T) {
	state, _ := provisionDaemonState(t)
	const workers = 32
	start := make(chan struct{})
	identities := make(chan TransportIdentity, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			identity, err := ProvisionTransportIdentity(state)
			if err != nil {
				errorsChannel <- err
				return
			}
			identities <- identity
		}()
	}
	close(start)
	group.Wait()
	close(errorsChannel)
	close(identities)
	for err := range errorsChannel {
		t.Fatalf("concurrent provision: %v", err)
	}
	var expected TransportIdentity
	for identity := range identities {
		if expected.PeerID().IsZero() {
			expected = identity
			continue
		}
		if identity.PeerID() != expected.PeerID() ||
			!bytes.Equal(identity.PublicKey(), expected.PublicKey()) {
			t.Fatal("concurrent provision published more than one identity")
		}
	}
	if expected.PeerID().IsZero() {
		t.Fatal("concurrent provision returned no identity")
	}
}

func TestProvisionLockObservesCancellation(t *testing.T) {
	state, _ := provisionDaemonState(t)
	if _, err := ProvisionTransportIdentity(state); err != nil {
		t.Fatal(err)
	}
	held, err := acquireExistingProvisionLock(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProvisionLock(held)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if contender, err := acquireExistingProvisionLock(cancelled, state); contender != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock acquisition = (%v, %v)", contender, err)
	}
}

func TestPublishTransportIdentityAcceptsMissingPendingOnlyForExactFinal(t *testing.T) {
	state, _ := provisionDaemonState(t)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := encodeTransportIdentity(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(state, transportIdentityPending)
	final := filepath.Join(state, transportIdentityFile)
	if err := createPrivateFile(pending, raw); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(pending, final); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if err := publishTransportIdentity(state, pending, final, raw); err != nil {
		t.Fatalf("exact concurrently published identity was rejected: %v", err)
	}
	if err := publishTransportIdentity(state, pending, final, append(raw, '\n')); err == nil {
		t.Fatal("missing pending accepted a different committed identity")
	}
}

func TestTransportIdentityFailsClosedOnUnsafeOrCorruptFile(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "corrupt", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(`{"schema":"wrong"}`), ownerFileMode); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "public mode", mutate: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _ := provisionDaemonState(t)
			if _, err := ProvisionTransportIdentity(state); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, filepath.Join(state, transportIdentityFile))
			if _, err := loadTransportIdentity(state); err == nil {
				t.Fatal("load accepted unsafe identity")
			}
		})
	}
}
