package attach

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxProjectedFileBytes = 8 << 10

func journalDirectoryRelative() string {
	return filepath.Join(".memory", "agency", "attach", "pi")
}

func inspectFreshTargets(plan installPlan) error {
	paths := []string{filepath.Dir(plan.journalPath)}
	for _, file := range plan.files {
		paths = append(paths, filepath.Dir(file.path))
	}
	for _, path := range paths {
		relative, err := filepath.Rel(plan.workspace, path)
		if err != nil {
			return fmt.Errorf("%w: resolve projection parent", ErrUnsafe)
		}
		if err := requireExistingDirectoryChain(plan.workspace, relative); err != nil {
			return err
		}
	}
	if info, err := os.Lstat(filepath.Dir(plan.journalPath)); err == nil {
		if err := validateSafeDirectory(info, true); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect journal directory", ErrUnsafe)
	}
	for _, file := range plan.files {
		if _, err := os.Lstat(file.path); err == nil {
			return fmt.Errorf("%w: projected file exists without ownership", ErrDrift)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect projected file: %v", ErrUnsafe, err)
		}
	}
	return nil
}

func ensureJournalDirectory(plan installPlan) error {
	if err := ensureDirectoryChain(plan.workspace, journalDirectoryRelative(), 0o700); err != nil {
		return err
	}
	return requireSafeDirectory(filepath.Dir(plan.journalPath), true)
}

func ensureProjectionDirectories(plan installPlan) error {
	for _, file := range plan.files {
		relative, err := filepath.Rel(plan.workspace, filepath.Dir(file.path))
		if err != nil {
			return fmt.Errorf("%w: resolve Pi directory", ErrUnsafe)
		}
		if err := ensureDirectoryChain(plan.workspace, relative, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func convergeFile(plan installPlan, file projectedFile) (bool, error) {
	current, _, err := readExactFile(file.path, projectedMode, maxProjectedFileBytes)
	if err == nil {
		if !bytes.Equal(current, file.content) {
			return false, fmt.Errorf("%w: projected file bytes changed", ErrDrift)
		}
		return false, cleanupStage(filepath.Dir(plan.journalPath), file.path,
			file.content, projectedMode)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("%w: inspect projected file: %v", ErrDrift, err)
	}
	if err := publishExclusive(filepath.Dir(plan.journalPath), file.path,
		file.content, projectedMode); err != nil {
		current, _, readErr := readExactFile(file.path, projectedMode, maxProjectedFileBytes)
		if readErr != nil || !bytes.Equal(current, file.content) {
			return false, fmt.Errorf("%w: publish projected file: %v", ErrDrift, err)
		}
		return false, cleanupStage(filepath.Dir(plan.journalPath), file.path,
			file.content, projectedMode)
	}
	return true, nil
}

func verifyFiles(plan installPlan) error {
	for _, file := range plan.files {
		relative, err := filepath.Rel(plan.workspace, filepath.Dir(file.path))
		if err != nil {
			return fmt.Errorf("%w: resolve projected file", ErrUnsafe)
		}
		if err := requireDirectoryChain(plan.workspace, relative); err != nil {
			return err
		}
		content, _, err := readExactFile(file.path, projectedMode, maxProjectedFileBytes)
		if err != nil || !bytes.Equal(content, file.content) || digest(content) != file.record.Digest {
			return fmt.Errorf("%w: projected file is not exact: %v", ErrDrift, err)
		}
	}
	return nil
}

func ensureDirectoryChain(root, relative string, mode os.FileMode) error {
	components, err := relativeComponents(relative)
	if err != nil {
		return err
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create projection directory: %w", err)
		}
		if err := requireSafeDirectory(current, false); err != nil {
			return err
		}
	}
	return nil
}

func requireDirectoryChain(root, relative string) error {
	components, err := relativeComponents(relative)
	if err != nil {
		return err
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		if err := requireSafeDirectory(current, false); err != nil {
			return err
		}
	}
	return nil
}

func requireExistingDirectoryChain(root, relative string) error {
	components, err := relativeComponents(relative)
	if err != nil {
		return err
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		if _, err := os.Lstat(current); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("%w: inspect projection directory", ErrUnsafe)
		}
		if err := requireSafeDirectory(current, false); err != nil {
			return err
		}
	}
	return nil
}

func relativeComponents(relative string) ([]string, error) {
	if relative == "" || relative == "." || filepath.IsAbs(relative) ||
		filepath.Clean(relative) != relative || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: path escapes workspace", ErrUnsafe)
	}
	return strings.Split(relative, string(filepath.Separator)), nil
}

func requireSafeDirectory(path string, private bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect directory: %v", ErrUnsafe, err)
	}
	return validateSafeDirectory(info, private)
}

func validateSafeDirectory(info os.FileInfo, private bool) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!ownedByCurrentUser(info) || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: directory is not a current-owner real path", ErrUnsafe)
	}
	if private && info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: journal directory is not owner-private", ErrUnsafe)
	}
	return nil
}

func publishExclusive(stagingDirectory, target string, content []byte,
	mode os.FileMode,
) error {
	stage, err := prepareStage(stagingDirectory, target, content, mode)
	if err != nil {
		return err
	}
	if err := os.Link(stage, target); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	return cleanupStage(stagingDirectory, target, content, mode)
}

func prepareStage(directory, target string, content []byte, mode os.FileMode) (string, error) {
	path := stagePath(directory, target)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		current, _, readErr := readExactFile(path, mode, int64(len(content)))
		if readErr == nil && bytes.Equal(current, content) {
			return path, nil
		}
		if discardErr := discardIncompleteStage(path, mode); discardErr != nil {
			return "", discardErr
		}
		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return "", err
	}
	fail := func(cause error) (string, error) {
		_ = file.Close()
		return "", cause
	}
	if err := writeFull(file, content); err != nil {
		return fail(err)
	}
	if err := file.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := syncDirectory(directory); err != nil {
		return "", err
	}
	return path, nil
}

func stagePath(directory, target string) string {
	identity := strings.TrimPrefix(digest([]byte(target)), "sha256:")
	return filepath.Join(directory, ".stage-"+identity)
}

func discardIncompleteStage(path string, expectedMode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!ownedByCurrentUser(info) ||
		(info.Mode().Perm() != 0o600 && info.Mode().Perm() != expectedMode) {
		return fmt.Errorf("%w: stage is not an owner-only regular file", ErrUnsafe)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func cleanupStage(directory, target string, content []byte, mode os.FileMode) error {
	path := stagePath(directory, target)
	current, _, err := readExactFile(path, mode, int64(len(content)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !bytes.Equal(current, content) {
		return discardIncompleteStage(path, mode)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func readExactFile(path string, mode os.FileMode, maximum int64) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if err := validateSafeFile(before, mode, maximum); err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameFileSnapshot(before, opened) {
		return nil, nil, errors.New("file changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum {
		return nil, nil, errors.New("file exceeds its bound")
	}
	afterFD, fdErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if fdErr != nil || pathErr != nil || !sameFileSnapshot(before, afterFD) ||
		!sameFileSnapshot(before, afterPath) || int64(len(content)) != before.Size() {
		return nil, nil, errors.New("file changed during read")
	}
	return content, afterFD, nil
}

func validateSafeFile(info os.FileInfo, mode os.FileMode, maximum int64) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!ownedByCurrentUser(info) || info.Mode().Perm() != mode || info.Size() < 0 ||
		info.Size() > maximum {
		return errors.New("file has unsafe type, owner, mode, or size")
	}
	return nil
}

func sameFileSnapshot(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime()) && ownedByCurrentUser(left) &&
		ownedByCurrentUser(right)
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
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
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
