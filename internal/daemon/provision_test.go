package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

func TestResolveProjectStateIsPureAndPhysical(t *testing.T) {
	root := canonicalTempDir(t)
	resolved, state, err := ResolveProjectState(root)
	if err != nil || resolved != root || state != filepath.Join(root, ".memory", "agency") {
		t.Fatalf("ResolveProjectState = (%q, %q, %v)", resolved, state, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("ResolveProjectState mutated project: entries=%v error=%v", entries, err)
	}
}

func TestProvisionCreatesOneReplayableNodeIdentityAndPrincipal(t *testing.T) {
	root := canonicalTempDir(t)
	first, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	wantState := filepath.Join(root, ".memory", "agency")
	if first.StateDirectory() != wantState || second.StateDirectory() != wantState ||
		first.PeerID().IsZero() || first.Principal().IsZero() || first.Replayed() ||
		!second.Replayed() || first.PeerID() != second.PeerID() ||
		first.Principal() != second.Principal() {
		t.Fatalf("provision results = first %#v second %#v", first, second)
	}
	for _, directory := range []string{wantState, filepath.Join(wantState, "objects"),
		filepath.Join(wantState, "objects", "sha256"),
		filepath.Join(wantState, "objects", "sha256", ".tmp")} {
		assertOwnerMode(t, directory, ownerDirectoryMode)
	}
	for _, file := range []string{filepath.Join(wantState, provisionLockFile),
		filepath.Join(wantState, ensureLockFile),
		filepath.Join(wantState, transportIdentityFile),
		filepath.Join(wantState, authorityFileName),
		filepath.Join(wantState, authorityFileName+".writer.lock")} {
		assertOwnerMode(t, file, ownerFileMode)
	}
	identity, err := loadTransportIdentity(wantState)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := DefaultAgentPrincipal(identity.projection.PublicKey())
	if err != nil || derived != first.Principal() {
		t.Fatalf("derived Principal = (%s, %v), want %s", derived.String(), err,
			first.Principal().String())
	}
	for _, forbidden := range []string{"principal", "config", "registry"} {
		if _, err := os.Lstat(filepath.Join(wantState, forbidden)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected second identity source %q: %v", forbidden, err)
		}
	}
}

func TestProvisionConcurrentCallsConvergeUnderOneLock(t *testing.T) {
	root := canonicalTempDir(t)
	const workers = 12
	start := make(chan struct{})
	results := make(chan ProvisionResult, workers)
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := Provision(context.Background(), root)
			results <- result
			errorsSeen <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Provision: %v", err)
		}
	}
	var peer agency.OpaqueHandle
	var principal agency.AgentPrincipalID
	fresh := 0
	for result := range results {
		if peer.IsZero() {
			peer, principal = result.PeerID(), result.Principal()
		}
		if result.PeerID() != peer || result.Principal() != principal {
			t.Fatal("concurrent Provision produced divergent identity")
		}
		if !result.Replayed() {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh Provision results = %d, want 1", fresh)
	}
}

func TestProvisionConvergesFromSchemaOnlyPartialState(t *testing.T) {
	root := canonicalTempDir(t)
	state, err := ensureProvisionDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionTransportIdentity(state); err != nil {
		t.Fatal(err)
	}
	if err := provisionEnsureLock(state); err != nil {
		t.Fatal(err)
	}
	objects := provisionTestCAS(t, state)
	store, err := authority.OpenWithArtifactVerifier(context.Background(),
		filepath.Join(state, authorityFileName), objects)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := Provision(context.Background(), root)
	if err != nil || result.Replayed() {
		t.Fatalf("partial-state Provision = (%#v, %v)", result, err)
	}
	opened, err := OpenProvisioned(context.Background(), result.StateDirectory())
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionReplaysAfterPeerRouteEnrollsItsSurrogatePrincipal(t *testing.T) {
	root := canonicalTempDir(t)
	first, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifact.Open(filepath.Join(first.StateDirectory(), "objects", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := authority.OpenExistingWithArtifactVerifier(context.Background(),
		filepath.Join(first.StateDirectory(), authorityFileName), objects)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := agency.NewRouteID("route:provision-replay")
	publicAlias, _ := agency.NewOpaqueHandle("peer:provision-replay")
	remotePeer, _ := agency.NewOpaqueHandle("transport-peer:provision-replay")
	remoteTarget, _ := agency.NewOpaqueHandle("remote-target:provision-replay")
	inboundTarget, _ := agency.NewOpaqueHandle("inbound-target:provision-replay")
	_, err = store.EnrollPeerRoute(context.Background(), authority.PeerRouteSpec{
		RouteID:              route,
		PublicAlias:          publicAlias,
		RemotePeerID:         remotePeer,
		RemotePublicKey:      bytes.Repeat([]byte{7}, authority.MaxPeerRoutePublicKeyBytes),
		TransportAddress:     "127.0.0.1:17071",
		RemoteTargetAlias:    remoteTarget,
		InboundTargetAlias:   inboundTarget,
		LocalTargetPrincipal: first.Principal(),
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := Provision(context.Background(), root)
	if err != nil || !replayed.Replayed() || replayed.Principal() != first.Principal() {
		t.Fatalf("Provision after route enrollment = (%#v, %v)", replayed, err)
	}
	opened, err := OpenProvisioned(context.Background(), first.StateDirectory())
	if err != nil {
		t.Fatalf("OpenProvisioned after route enrollment: %v", err)
	}
	if err := opened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionRejectsAuthorityWithoutIdentityBeforeCreatingIdentity(t *testing.T) {
	root := canonicalTempDir(t)
	state, err := ensureProvisionDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	objects := provisionTestCAS(t, state)
	database := filepath.Join(state, authorityFileName)
	store, err := authority.OpenWithArtifactVerifier(context.Background(), database, objects)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := Provision(context.Background(), root); result != (ProvisionResult{}) ||
		!errors.Is(err, ErrProvision) {
		t.Fatalf("authority-without-identity Provision = (%#v, %v)", result, err)
	}
	if _, err := os.Lstat(filepath.Join(state, transportIdentityFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected Provision created identity: %v", err)
	}
	after, err := os.ReadFile(database)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected Provision changed authority bytes: equal=%t error=%v",
			bytes.Equal(before, after), err)
	}
}

func TestProvisionNeverRepairsMissingCASAfterAuthorityExists(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(result.StateDirectory(), "objects", "sha256", ".tmp")
	if err := os.Remove(temporary); err != nil {
		t.Fatal(err)
	}
	if replay, err := Provision(context.Background(), root); replay != (ProvisionResult{}) ||
		!errors.Is(err, ErrProvision) {
		t.Fatalf("Provision with missing durable CAS = (%#v, %v)", replay, err)
	}
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected replay repaired missing CAS state: %v", err)
	}
}

func TestProvisionNeverRepairsMissingEnsureLockAfterAuthorityExists(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(result.StateDirectory(), ensureLockFile)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if replay, err := Provision(context.Background(), root); replay != (ProvisionResult{}) ||
		!errors.Is(err, ErrProvision) {
		t.Fatalf("Provision with missing ensure lock = (%#v, %v)", replay, err)
	}
	if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected replay repaired missing ensure lock: %v", err)
	}
}

func TestProvisionFailsClosedOnDifferentExistingPrincipal(t *testing.T) {
	root := canonicalTempDir(t)
	state, err := ensureProvisionDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionTransportIdentity(state); err != nil {
		t.Fatal(err)
	}
	if err := provisionEnsureLock(state); err != nil {
		t.Fatal(err)
	}
	objects := provisionTestCAS(t, state)
	store, err := authority.OpenWithArtifactVerifier(context.Background(),
		filepath.Join(state, authorityFileName), objects)
	if err != nil {
		t.Fatal(err)
	}
	wrong, _ := agency.NewAgentPrincipalID("principal:wrong")
	if err := store.EnrollPrincipal(context.Background(), wrong); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := Provision(context.Background(), root); result != (ProvisionResult{}) ||
		!errors.Is(err, ErrProvision) || !errors.Is(err, authority.ErrPrincipalConflict) {
		t.Fatalf("conflicting Provision = (%#v, %v)", result, err)
	}
	verify, err := authority.OpenExistingWithArtifactVerifier(context.Background(),
		filepath.Join(state, authorityFileName), objects)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	if err := verify.RequirePrincipal(context.Background(), wrong); err != nil {
		t.Fatalf("conflict changed existing Principal: %v", err)
	}
}

func TestProvisionRejectsSymlinkAndUnsafeDirectoryWithoutRepair(t *testing.T) {
	realRoot := canonicalTempDir(t)
	linkParent := canonicalTempDir(t)
	rootLink := filepath.Join(linkParent, "project")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(context.Background(), rootLink); !errors.Is(err, ErrProvision) {
		t.Fatalf("symlink root Provision = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(realRoot, ".memory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected symlink root was mutated: %v", err)
	}

	unsafeRoot := canonicalTempDir(t)
	node := filepath.Join(unsafeRoot, ".memory", "agency")
	if err := os.MkdirAll(node, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(unsafeRoot, ".memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(node, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(context.Background(), unsafeRoot); !errors.Is(err, ErrProvision) {
		t.Fatalf("unsafe state Provision = %v", err)
	}
	assertOwnerMode(t, node, 0o755)

	lockRoot := canonicalTempDir(t)
	provisioned, err := Provision(context.Background(), lockRoot)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(provisioned.StateDirectory(), provisionLockFile)
	if err := os.Chmod(lockPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(context.Background(), lockRoot); !errors.Is(err, ErrProvision) {
		t.Fatalf("unsafe lock Provision = %v", err)
	}
	assertOwnerMode(t, lockPath, 0o644)
}

func TestOpenProvisionedIsStrictAndDerivesPrincipal(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenProvisioned(context.Background(), result.StateDirectory())
	if err != nil || runtime == nil {
		t.Fatalf("OpenProvisioned = (%v, %v)", runtime, err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenProvisionedDoesNotInitializeEmptyState(t *testing.T) {
	empty := canonicalTempDir(t)
	before, err := os.ReadDir(empty)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenProvisioned(context.Background(), empty); err == nil || opened != nil {
		t.Fatalf("OpenProvisioned(empty) = (%v, %v)", opened, err)
	}
	after, err := os.ReadDir(empty)
	if err != nil || len(before) != 0 || len(after) != 0 {
		t.Fatalf("strict open initialized empty state: before=%d after=%d error=%v",
			len(before), len(after), err)
	}
}

func TestOpenProvisionedDoesNotRepairMissingProvisionLock(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(result.StateDirectory(), provisionLockFile)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenProvisioned(context.Background(), result.StateDirectory()); err == nil || opened != nil {
		t.Fatalf("OpenProvisioned(missing lock) = (%v, %v)", opened, err)
	}
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict open recreated missing lock: %v", err)
	}
}

func TestOpenProvisionedDoesNotRepairMissingEnsureLock(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ensurePath := filepath.Join(result.StateDirectory(), ensureLockFile)
	if err := os.Remove(ensurePath); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenProvisioned(context.Background(), result.StateDirectory()); err == nil || opened != nil {
		t.Fatalf("OpenProvisioned(missing ensure lock) = (%v, %v)", opened, err)
	}
	if _, err := os.Lstat(ensurePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict open recreated missing ensure lock: %v", err)
	}
}

func TestOpenProvisionedDoesNotRepairMissingCAS(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	tempDirectory := filepath.Join(result.StateDirectory(), "objects", "sha256", ".tmp")
	if err := os.Remove(tempDirectory); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenProvisioned(context.Background(), result.StateDirectory()); err == nil || opened != nil {
		t.Fatalf("OpenProvisioned(missing CAS state) = (%v, %v)", opened, err)
	}
	if _, err := os.Lstat(tempDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict open repaired missing CAS state: %v", err)
	}
}

func TestOpenProvisionedRejectsOrphanPrincipalAuthority(t *testing.T) {
	root := canonicalTempDir(t)
	result, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifact.OpenExisting(filepath.Join(result.StateDirectory(), "objects", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := authority.OpenExistingWithArtifactVerifier(context.Background(),
		filepath.Join(result.StateDirectory(), authorityFileName), objects)
	if err != nil {
		t.Fatal(err)
	}
	orphan, _ := agency.NewAgentPrincipalID("principal:orphan")
	if err := store.EnrollPrincipal(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenProvisioned(context.Background(), result.StateDirectory()); opened != nil || !errors.Is(err, authority.ErrPrincipalConflict) {
		t.Fatalf("OpenProvisioned(orphan Principal) = (%v, %v)", opened, err)
	}
	verify, err := authority.OpenExistingWithArtifactVerifier(context.Background(),
		filepath.Join(result.StateDirectory(), authorityFileName), objects)
	if err != nil {
		t.Fatalf("failed strict open leaked authority writer: %v", err)
	}
	_ = verify.Close()
}

func provisionTestCAS(t *testing.T, state string) *artifact.Store {
	t.Helper()
	parent := filepath.Join(state, "objects")
	if err := ensureOwnedDirectory(parent, true); err != nil {
		t.Fatal(err)
	}
	objects, err := artifact.Open(filepath.Join(parent, "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func assertOwnerMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("%s mode = (%v, %v), want %04o", path, info, err, mode)
	}
}
