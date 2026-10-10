package artifact

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestStorePutReadReplayAndOwnerOnlyLayout(t *testing.T) {
	store := openTestStore(t)
	content := []byte("immutable R7 Artifact")
	digest := agency.Sum(content)
	first, err := store.Put(context.Background(), digest, content)
	if err != nil || first.Replayed || first.Digest != digest || first.Size != int64(len(content)) {
		t.Fatalf("first Put = (%#v, %v)", first, err)
	}
	replay, err := store.Put(context.Background(), digest, append([]byte(nil), content...))
	if err != nil || !replay.Replayed || replay.Digest != digest || replay.Size != int64(len(content)) {
		t.Fatalf("replayed Put = (%#v, %v)", replay, err)
	}
	read, err := store.Read(context.Background(), digest, int64(len(content)))
	if err != nil || !bytes.Equal(read, content) {
		t.Fatalf("Read = (%q, %v)", read, err)
	}
	if err := store.VerifyArtifact(context.Background(), digest, int64(len(content))); err != nil {
		t.Fatalf("VerifyArtifact() = %v", err)
	}
	object, err := store.objectPath(digest, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
		dir  bool
	}{{store.root, directoryMode, true}, {store.temp, directoryMode, true},
		{filepath.Dir(object), directoryMode, true}, {object, objectMode, false}} {
		info, err := os.Lstat(check.path)
		if err != nil {
			t.Fatalf("inspect %s: %v", check.path, err)
		}
		if info.Mode().Perm() != check.mode || info.IsDir() != check.dir || !ownedByCurrentUser(info) {
			t.Fatalf("unsafe layout %s: mode=%v dir=%t", check.path, info.Mode(), info.IsDir())
		}
	}
	if _, err := os.Lstat(filepath.Join(store.root, ".staging")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("R5 staging directory exists: %v", err)
	}
}

func TestOpenExistingNeverCreatesOrRepairsCASLayout(t *testing.T) {
	root := testRoot(t)
	if opened, err := OpenExisting(root); err == nil || opened != nil {
		t.Fatalf("OpenExisting(missing) = (%v, %v)", opened, err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict open created root: %v", err)
	}
	created, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenExisting(root); err != nil || opened.Root() != created.Root() {
		t.Fatalf("OpenExisting(provisioned) = (%v, %v)", opened, err)
	}
	temporary := filepath.Join(root, ".tmp")
	if err := os.Remove(temporary); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenExisting(root); err == nil || opened != nil {
		t.Fatalf("OpenExisting(missing .tmp) = (%v, %v)", opened, err)
	}
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict open recreated .tmp: %v", err)
	}
}

func TestStoreRejectsMismatchBoundsCancellationAndCorruption(t *testing.T) {
	store := openTestStore(t)
	content := []byte("strict bytes")
	digest := agency.Sum(content)
	if _, err := store.Put(context.Background(), agency.Sum([]byte("other")), content); !errors.Is(err, ErrCorruption) {
		t.Fatalf("wrong digest Put error = %v", err)
	}
	if _, err := store.Put(context.Background(), agency.Sum(make([]byte, MaxObjectBytes+1)),
		make([]byte, MaxObjectBytes+1)); !errors.Is(err, ErrInput) {
		t.Fatalf("oversized Put error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(cancelled, digest, content); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Put error = %v", err)
	}
	if _, err := store.Put(context.Background(), digest, content); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), digest, int64(len(content)-1)); !errors.Is(err, ErrCorruption) {
		t.Fatalf("under-budget Read error = %v", err)
	}
	if err := store.VerifyArtifact(context.Background(), digest, int64(len(content)-1)); !errors.Is(err, ErrCorruption) {
		t.Fatalf("wrong-size VerifyArtifact error = %v", err)
	}
	object, err := store.objectPath(digest, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("tampered!!!"), objectMode); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), digest, MaxObjectBytes); !errors.Is(err, ErrCorruption) {
		t.Fatalf("tampered Read error = %v", err)
	}
}

func TestStoreConcurrentPutHasOneFinalEffectAcrossInstances(t *testing.T) {
	root := testRoot(t)
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("bounded"), 4096)
	digest := agency.Sum(content)
	start := make(chan struct{})
	results := make(chan PutResult, 16)
	errorsSeen := make(chan error, 16)
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		store := first
		if index%2 == 1 {
			store = second
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, putErr := store.Put(context.Background(), digest, content)
			results <- result
			errorsSeen <- putErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Put = %v", err)
		}
	}
	created := 0
	for result := range results {
		if !result.Replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created results = %d, want 1", created)
	}
	read, err := second.Read(context.Background(), digest, int64(len(content)))
	if err != nil || !bytes.Equal(read, content) {
		t.Fatalf("concurrent final Read = (%d bytes, %v)", len(read), err)
	}
}

// Promoting another writer's complete marker is how the object gets created,
// so the promoting Put is the creator. Reporting it as a replay loses the
// only creation signal: the writer that staged the marker lost the link race
// and replays too, leaving a stored object no caller claims to have written.
func TestStorePutReportsCreationWhenItPromotesAnotherWritersMarker(t *testing.T) {
	root := testRoot(t)
	stager, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	promoter, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("staged then promoted"), 128)
	digest := agency.Sum(content)

	// The marker is complete and verified, but never promoted by its writer.
	if err := stager.stageMarker(context.Background(), digest, content); err != nil {
		t.Fatalf("stage marker: %v", err)
	}

	promoted, err := promoter.Put(context.Background(), digest, content)
	if err != nil {
		t.Fatalf("Put over a staged marker: %v", err)
	}
	if promoted.Replayed {
		t.Error("Put installed the final link, so it created the object and must not report a replay")
	}

	replay, err := stager.Put(context.Background(), digest, content)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if !replay.Replayed {
		t.Error("a Put against an existing final object must report a replay")
	}
	assertPromotionSettled(t, promoter, digest)
}

func TestStoreConcurrentProcessesPreserveImmutableFinal(t *testing.T) {
	const helper = "MEMORY_CAS_PROCESS_HELPER"
	content := bytes.Repeat([]byte("cross-process immutable bytes"), 4096)
	if os.Getenv(helper) == "1" {
		runCASProcessHelper(t, content)
		return
	}

	root := testRoot(t)
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gateWrite.Close()
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, len(commands))
	for index := range commands {
		command := exec.Command(os.Args[0], "-test.run=^TestStoreConcurrentProcessesPreserveImmutableFinal$")
		command.Env = append(os.Environ(), helper+"=1", "MEMORY_CAS_PROCESS_ROOT="+root)
		command.ExtraFiles = []*os.File{gateRead}
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			_ = gateRead.Close()
			_ = gateWrite.Close()
			t.Fatal(err)
		}
		commands[index] = command
	}
	_ = gateRead.Close()
	if err := gateWrite.Close(); err != nil {
		t.Fatal(err)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("CAS helper %d: %v\n%s", index, err, outputs[index].String())
		}
	}

	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	digest := agency.Sum(content)
	read, err := store.Read(context.Background(), digest, int64(len(content)))
	if err != nil || !bytes.Equal(read, content) {
		t.Fatalf("cross-process final Read = (%d bytes, %v)", len(read), err)
	}
	assertPromotionSettled(t, store, digest)
}

func runCASProcessHelper(t *testing.T, content []byte) {
	t.Helper()
	gate := os.NewFile(3, "cas-test-gate")
	if gate == nil {
		t.Fatal("helper gate is unavailable")
	}
	if _, err := io.Copy(io.Discard, gate); err != nil {
		t.Fatal(err)
	}
	_ = gate.Close()
	store, err := Open(os.Getenv("MEMORY_CAS_PROCESS_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), agency.Sum(content), content); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRestartRecoversOnlyCompletePromotionMarker(t *testing.T) {
	root := testRoot(t)
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("complete marker before final link")
	digest := agency.Sum(content)
	if _, err := store.objectPath(digest, true); err != nil {
		t.Fatal(err)
	}
	if err := store.stageMarker(context.Background(), digest, content); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.Read(context.Background(), digest, int64(len(content)))
	if err != nil || !bytes.Equal(read, content) {
		t.Fatalf("marker-only recovery Read = (%q, %v)", read, err)
	}
	assertPromotionSettled(t, reopened, digest)

	linkedContent := []byte("complete marker after final link")
	linkedDigest := agency.Sum(linkedContent)
	final, err := reopened.objectPath(linkedDigest, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.stageMarker(context.Background(), linkedDigest, linkedContent); err != nil {
		t.Fatal(err)
	}
	marker, err := reopened.promotionPath(linkedDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(marker, final); err != nil {
		t.Fatal(err)
	}
	reopenedAgain, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	read, err = reopenedAgain.Read(context.Background(), linkedDigest, int64(len(linkedContent)))
	if err != nil || !bytes.Equal(read, linkedContent) {
		t.Fatalf("linked-marker recovery Read = (%q, %v)", read, err)
	}
	assertPromotionSettled(t, reopenedAgain, linkedDigest)
}

func TestStoreIgnoresIncompleteTemporaryFiles(t *testing.T) {
	store := openTestStore(t)
	content := []byte("orphan is not authority")
	digest := agency.Sum(content)
	orphan := filepath.Join(store.temp, "put-11111111111111111111111111111111.tmp")
	if err := os.WriteFile(orphan, content[:5], objectMode); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), digest, int64(len(content))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan temporary file Read error = %v", err)
	}
	if _, err := store.Put(context.Background(), digest, content); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(orphan); err != nil {
		t.Fatalf("unrelated orphan was consumed: %v", err)
	}
}

func TestStoreRejectsUnsafeRootShardObjectAndHardLink(t *testing.T) {
	base := realTestDirectory(t)
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, directoryMode); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(linkRoot); !errors.Is(err, ErrCorruption) {
		t.Fatalf("symlink root Open error = %v", err)
	}

	store := openTestStore(t)
	content := []byte("owner-only object")
	digest := agency.Sum(content)
	hexDigest, err := digestHex(digest)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, directoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.root, hexDigest[:2])); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), digest, content); !errors.Is(err, ErrCorruption) {
		t.Fatalf("symlink shard Put error = %v", err)
	}

	clean := openTestStore(t)
	if _, err := clean.Put(context.Background(), digest, content); err != nil {
		t.Fatal(err)
	}
	object, err := clean.objectPath(digest, false)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign-link")
	if err := os.Link(object, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := clean.Read(context.Background(), digest, int64(len(content))); !errors.Is(err, ErrCorruption) {
		t.Fatalf("foreign hard-link Read error = %v", err)
	}
}

func TestStoreRejectsSymlinkedAncestor(t *testing.T) {
	base := realTestDirectory(t)
	realParent := filepath.Join(base, "parent")
	if err := os.Mkdir(realParent, directoryMode); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(base, "parent-link")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(linkParent, "objects", "sha256")); !errors.Is(err, ErrCorruption) {
		t.Fatalf("symlink ancestor Open error = %v", err)
	}
}

func assertPromotionSettled(t *testing.T, store *Store, digest agency.Digest) {
	t.Helper()
	marker, err := store.promotionPath(digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("promotion marker remains: %v", err)
	}
	object, err := store.objectPath(digest, false)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(object)
	if err != nil {
		t.Fatal(err)
	}
	if linkCount(info) != 1 {
		t.Fatalf("final hard-link count = %d", linkCount(info))
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(realTestDirectory(t), "objects", "sha256")
}

func realTestDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
