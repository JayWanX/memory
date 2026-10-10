package artifact

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	directoryMode = 0o700
	objectMode    = 0o600
)

func (store *Store) objectPath(digest agency.Digest, create bool) (string, error) {
	hexDigest, err := digestHex(digest)
	if err != nil {
		return "", err
	}
	shard := filepath.Join(store.root, hexDigest[:2])
	if create {
		created, err := ensurePrivateDirectory(shard)
		if err != nil {
			return "", err
		}
		if created {
			if err := syncDirectory(store.root); err != nil {
				return "", err
			}
		}
	} else if _, present, err := privateDirectory(shard); err != nil {
		return "", err
	} else if !present {
		return filepath.Join(shard, hexDigest), nil
	}
	return filepath.Join(shard, hexDigest), nil
}

func (store *Store) promotionPath(digest agency.Digest) (string, error) {
	hexDigest, err := digestHex(digest)
	if err != nil {
		return "", err
	}
	return filepath.Join(store.temp, hexDigest+".promote"), nil
}

func (store *Store) stageMarker(ctx context.Context, digest agency.Digest,
	content []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tempPath, file, err := store.newTemp()
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := writeFull(file, content); err != nil {
		return fmt.Errorf("write CAS temporary object: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("fsync CAS temporary object: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close CAS temporary object: %w", err)
	}
	verified, err := readVerifiedFile(ctx, tempPath, digest, int64(len(content)), 1)
	if err != nil {
		return err
	}
	if len(verified) != len(content) {
		return fmt.Errorf("%w: temporary object size changed", ErrCorruption)
	}
	if err := syncDirectory(store.temp); err != nil {
		return err
	}
	marker, err := store.promotionPath(digest)
	if err != nil {
		return err
	}
	if err := os.Rename(tempPath, marker); err != nil {
		return fmt.Errorf("publish CAS promotion marker: %w", err)
	}
	removeTemp = false
	if err := syncDirectory(store.temp); err != nil {
		return err
	}
	return ctx.Err()
}

// settlePromotion closes every durable crash point after a complete marker.
// The final link is authoritative and never overwritten. The marker is
// replaceable scratch: concurrent writers for one digest have already proved
// that their bytes hash to that digest, and a losing writer may remove it once
// a verified final exists. created reports whether this call installed the
// final link.
func (store *Store) settlePromotion(ctx context.Context, digest agency.Digest,
	final string,
) (created, present bool, err error) {
	marker, err := store.promotionPath(digest)
	if err != nil {
		return false, false, err
	}
	installed := false
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, false, err
		}
		_, finalPresent, err := privateRegular(final)
		if err != nil {
			return false, false, err
		}
		if finalPresent {
			created, present, err := store.settleFinal(ctx, digest, marker, final, installed)
			if retryablePromotionRace(err) {
				continue
			}
			return created, present, err
		}
		linked, markerPresent, err := store.linkPromotion(ctx, digest, marker, final)
		if retryablePromotionRace(err) {
			continue
		}
		if err != nil {
			return false, false, err
		}
		if !markerPresent {
			return false, false, nil
		}
		if linked {
			installed = true
		}
	}
	return false, false, fmt.Errorf("%w: promotion did not settle", ErrCorruption)
}

func (store *Store) linkPromotion(ctx context.Context, digest agency.Digest,
	marker, final string,
) (linked, present bool, err error) {
	_, present, err = privateRegular(marker)
	if err != nil || !present {
		return false, present, err
	}
	if _, err := readVerifiedFile(ctx, marker, digest, MaxObjectBytes, 1); err != nil {
		return false, true, err
	}
	if err := os.Link(marker, final); err != nil {
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
			return false, true, ErrCorruption
		}
		return false, true, fmt.Errorf("promote CAS object: %w", err)
	}
	return true, true, nil
}

func retryablePromotionRace(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrCorruption)
}

func (store *Store) settleFinal(ctx context.Context, digest agency.Digest,
	marker, final string, created bool,
) (bool, bool, error) {
	if err := syncDirectory(filepath.Dir(final)); err != nil {
		return false, false, err
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, false, fmt.Errorf("remove CAS promotion marker: %w", err)
	}
	if err := syncDirectory(store.temp); err != nil {
		return false, false, err
	}
	if _, err := readVerifiedFile(ctx, final, digest, MaxObjectBytes, 1); err != nil {
		return false, false, err
	}
	return created, true, ctx.Err()
}

func (store *Store) newTemp() (string, *os.File, error) {
	if err := requirePrivateDirectory(store.temp); err != nil {
		return "", nil, err
	}
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", nil, fmt.Errorf("allocate CAS temporary object: %w", err)
		}
		path := filepath.Join(store.temp, "put-"+hex.EncodeToString(random)+".tmp")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, objectMode)
		if err == nil {
			return path, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("create CAS temporary object: %w", err)
		}
	}
	return "", nil, errors.New("allocate CAS temporary object: collision budget exhausted")
}

func readVerifiedFile(ctx context.Context, path string, digest agency.Digest,
	maximum int64, expectedLinks uint64,
) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read CAS object: %w", err)
	}
	if err := validatePrivateRegular(before, maximum, expectedLinks); err != nil {
		return nil, fmt.Errorf("verify CAS object %s: %w", path, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CAS object: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSnapshot(before, opened) {
		return nil, fmt.Errorf("%w: object changed while opening", ErrCorruption)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum {
		return nil, fmt.Errorf("%w: object exceeded its read bound", ErrCorruption)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	afterFD, fdErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if fdErr != nil || pathErr != nil || !sameSnapshot(before, afterFD) ||
		!sameSnapshot(before, afterPath) || int64(len(content)) != before.Size() {
		return nil, fmt.Errorf("%w: object identity changed during read", ErrCorruption)
	}
	if agency.Sum(content) != digest {
		return nil, fmt.Errorf("%w: object bytes do not match path digest", ErrCorruption)
	}
	return content, nil
}

func privateRegular(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect CAS object: %w", err)
	}
	if err := validatePrivateRegular(info, MaxObjectBytes, linkCount(info)); err != nil {
		return nil, true, err
	}
	return info, true, nil
}

func validatePrivateRegular(info os.FileInfo, maximum int64, expectedLinks uint64) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != objectMode || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: object is not an owner-only regular file", ErrCorruption)
	}
	if info.Size() < 0 || info.Size() > maximum {
		return fmt.Errorf("%w: object size is outside its bound", ErrCorruption)
	}
	if links := linkCount(info); links == 0 || links != expectedLinks {
		return fmt.Errorf("%w: object has an unexpected hard-link count", ErrCorruption)
	}
	return nil
}

func sameSnapshot(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime()) && linkCount(left) == linkCount(right) &&
		ownedByCurrentUser(left) && ownedByCurrentUser(right)
}

func linkCount(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Nlink)
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func privateDirectory(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect CAS directory: %w", err)
	}
	if err := validatePrivateDirectory(info); err != nil {
		return nil, true, err
	}
	return info, true, nil
}

func ensurePrivateDirectory(path string) (bool, error) {
	if _, present, err := privateDirectory(path); err != nil || present {
		return false, err
	}
	if err := os.MkdirAll(path, directoryMode); err != nil {
		return false, fmt.Errorf("create CAS directory: %w", err)
	}
	if err := os.Chmod(path, directoryMode); err != nil {
		return false, fmt.Errorf("protect CAS directory: %w", err)
	}
	if err := requirePrivateDirectory(path); err != nil {
		return false, err
	}
	return true, nil
}

func requirePrivateDirectory(path string) error {
	info, present, err := privateDirectory(path)
	if err != nil {
		return err
	}
	if !present || info == nil {
		return fmt.Errorf("%w: CAS directory is unavailable", ErrCorruption)
	}
	return nil
}

func validatePrivateDirectory(info os.FileInfo) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != directoryMode || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: path is not an owner-only real directory", ErrCorruption)
	}
	return nil
}

func writeFull(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open CAS directory for fsync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("fsync CAS directory: %w", err)
	}
	return nil
}
