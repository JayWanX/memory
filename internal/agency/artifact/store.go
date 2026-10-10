package artifact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	// MaxObjectBytes is the T0 Artifact byte bound shared by local capture and
	// peer verification. The store never accepts semantic input that raises it.
	MaxObjectBytes = 4 << 20
	digestShards   = 256
)

var (
	ErrInput      = errors.New("artifact: invalid input")
	ErrCorruption = errors.New("artifact: corruption")
)

// Store owns one owner-only sha256 object tree. It stores bytes only; pins,
// provenance, references, delivery state, and domain authority live elsewhere.
type Store struct {
	root    string
	temp    string
	digests [digestShards]sync.Mutex
}

type PutResult struct {
	Digest   agency.Digest
	Size     int64
	Replayed bool
}

// Open creates or validates an owner-only Artifact root. root is the
// objects/sha256 directory, not an object or workspace path.
func Open(root string) (*Store, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("%w: root must be an absolute canonical path", ErrInput)
	}
	rootCreated, err := ensurePrivateDirectory(root)
	if err != nil {
		return nil, err
	}
	if rootCreated {
		if err := syncDirectory(filepath.Dir(root)); err != nil {
			return nil, err
		}
	}
	temp := filepath.Join(root, ".tmp")
	created, err := ensurePrivateDirectory(temp)
	if err != nil {
		return nil, err
	}
	if created {
		if err := syncDirectory(root); err != nil {
			return nil, err
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve CAS root: %w", err)
	}
	if realRoot != root {
		return nil, fmt.Errorf("%w: CAS root has a symlinked ancestor", ErrCorruption)
	}
	return &Store{root: root, temp: temp}, nil
}

// OpenExisting adopts an exact provisioned Artifact layout. It never creates or
// repairs root, .tmp, shard, marker, or object state.
func OpenExisting(root string) (*Store, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("%w: root must be an absolute canonical path", ErrInput)
	}
	if err := requirePrivateDirectory(root); err != nil {
		return nil, err
	}
	temp := filepath.Join(root, ".tmp")
	if err := requirePrivateDirectory(temp); err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve existing CAS root: %w", err)
	}
	if realRoot != root {
		return nil, fmt.Errorf("%w: existing CAS root has a symlinked ancestor", ErrCorruption)
	}
	return &Store{root: root, temp: temp}, nil
}

func (store *Store) Root() string {
	if store == nil {
		return ""
	}
	return store.root
}

// Put verifies the claimed digest before touching the store and publishes the
// object with no-overwrite hard-link semantics. Same digest and bytes replay;
// any existing mismatch fails closed.
func (store *Store) Put(ctx context.Context, digest agency.Digest,
	content []byte,
) (PutResult, error) {
	if ctx == nil || digest.IsZero() || len(content) > MaxObjectBytes {
		return PutResult{}, fmt.Errorf("%w: context, digest, or object size", ErrInput)
	}
	if err := ctx.Err(); err != nil {
		return PutResult{}, err
	}
	if agency.Sum(content) != digest {
		return PutResult{}, fmt.Errorf("%w: bytes do not match the claimed digest", ErrCorruption)
	}
	guard, err := store.digestGuard(digest)
	if err != nil {
		return PutResult{}, err
	}
	guard.Lock()
	defer guard.Unlock()

	final, err := store.objectPath(digest, true)
	if err != nil {
		return PutResult{}, err
	}
	// The first settle can install the final link itself: another writer may
	// have staged a complete marker and not yet promoted it. Dropping created
	// here reported the writer that published the object as a replay, and when
	// the staging writer then lost the link race no caller was told it had
	// created anything.
	created, present, err := store.settlePromotion(ctx, digest, final)
	if err != nil {
		return PutResult{}, err
	}
	if present {
		result, err := store.inspectReplay(ctx, digest, final, content)
		if err != nil {
			return PutResult{}, err
		}
		result.Replayed = !created
		return result, nil
	}
	if err := store.stageMarker(ctx, digest, content); err != nil {
		return PutResult{}, err
	}
	created, present, err = store.settlePromotion(ctx, digest, final)
	if err != nil {
		return PutResult{}, err
	}
	if !present {
		return PutResult{}, fmt.Errorf("%w: promotion produced no final object", ErrCorruption)
	}
	result, err := store.inspectReplay(ctx, digest, final, content)
	if err != nil {
		return PutResult{}, err
	}
	result.Replayed = !created
	return result, nil
}

// Read returns verified bytes only. maximum is both a caller budget and a hard
// pre-allocation bound; paths and temporary files are never exposed.
func (store *Store) Read(ctx context.Context, digest agency.Digest,
	maximum int64,
) ([]byte, error) {
	if ctx == nil || digest.IsZero() || maximum < 0 || maximum > MaxObjectBytes {
		return nil, fmt.Errorf("%w: context, digest, or read bound", ErrInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	guard, err := store.digestGuard(digest)
	if err != nil {
		return nil, err
	}
	guard.Lock()
	defer guard.Unlock()
	final, err := store.objectPath(digest, false)
	if err != nil {
		return nil, err
	}
	_, present, err := store.settlePromotion(ctx, digest, final)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("read CAS object: %w", os.ErrNotExist)
	}
	content, err := readVerifiedFile(ctx, final, digest, maximum, 1)
	if err != nil {
		return nil, err
	}
	return content, ctx.Err()
}

// VerifyArtifact implements authority's narrow verification seam without
// importing authority. It re-reads and re-hashes the complete exact-size object.
func (store *Store) VerifyArtifact(ctx context.Context, digest agency.Digest,
	byteSize int64,
) error {
	content, err := store.Read(ctx, digest, byteSize)
	if err != nil {
		return err
	}
	if int64(len(content)) != byteSize || agency.Sum(content) != digest {
		return fmt.Errorf("%w: object does not match verified metadata", ErrCorruption)
	}
	return ctx.Err()
}

func (store *Store) inspectReplay(ctx context.Context, digest agency.Digest,
	path string, expected []byte,
) (PutResult, error) {
	content, err := readVerifiedFile(ctx, path, digest, int64(len(expected)), 1)
	if err != nil {
		return PutResult{}, err
	}
	if !bytes.Equal(content, expected) {
		return PutResult{}, fmt.Errorf("%w: same digest has different bytes", ErrCorruption)
	}
	return PutResult{Digest: digest, Size: int64(len(content)), Replayed: true}, nil
}

func (store *Store) digestGuard(digest agency.Digest) (*sync.Mutex, error) {
	if err := store.validate(); err != nil {
		return nil, err
	}
	if digest.IsZero() {
		return nil, fmt.Errorf("%w: zero digest", ErrInput)
	}
	return &store.digests[int(digest[0])], nil
}

func (store *Store) validate() error {
	if store == nil || store.root == "" || store.temp == "" {
		return fmt.Errorf("%w: nil or incomplete store", ErrInput)
	}
	for _, directory := range []string{store.root, store.temp} {
		if err := requirePrivateDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func digestHex(digest agency.Digest) (string, error) {
	if digest.IsZero() {
		return "", fmt.Errorf("%w: zero digest", ErrInput)
	}
	value := strings.TrimPrefix(digest.String(), "sha256:")
	if len(value) != 64 {
		return "", fmt.Errorf("%w: malformed digest", ErrInput)
	}
	return value, nil
}
