package agencyclient

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/JayWanX/memory/internal/agency"
	"golang.org/x/sys/unix"
)

func (directory *lockedJournalDirectory) load() (clientJournal, error) {
	names, err := directory.entryNames()
	if err != nil {
		return clientJournal{}, err
	}
	fileName := ""
	for _, name := range names {
		if name == journalLockName {
			continue
		}
		if name == journalActiveName || validTerminalName(name) {
			if fileName != "" {
				return clientJournal{}, errors.New("multiple R7 client journals exist")
			}
			fileName = name
			continue
		}
		return clientJournal{}, errors.New("R7 client journal directory contains an unknown entry")
	}
	if fileName == "" {
		return clientJournal{}, errJournalAbsent
	}
	raw, err := directory.readOwnerFile(fileName, maxJournalBytes)
	if err != nil {
		return clientJournal{}, err
	}
	journal, err := parseClientJournal(raw)
	clear(raw)
	if err != nil {
		return clientJournal{}, err
	}
	journal.fileName = fileName
	if validTerminalName(fileName) {
		operation, err := terminalOperation(fileName)
		if err != nil || operation.IsZero() {
			journal.clear()
			return clientJournal{}, errors.New("R7 terminal journal name is invalid")
		}
	}
	return journal, nil
}

func (directory *lockedJournalDirectory) entryNames() ([]string, error) {
	fd, err := unix.Openat(int(directory.dir.Fd()), ".",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	copyDir := os.NewFile(uintptr(fd), directory.path)
	if copyDir == nil {
		_ = unix.Close(fd)
		return nil, errors.New("duplicate R7 client journal directory returned no file")
	}
	defer copyDir.Close()
	entries, err := copyDir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("scan R7 client journal directory: %w", err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names, nil
}

func (directory *lockedJournalDirectory) readOwnerFile(name string, maximum int) ([]byte, error) {
	if filepath.Base(name) != name || maximum <= 0 {
		return nil, errors.New("R7 client journal file name is invalid")
	}
	fd, err := unix.Openat(int(directory.dir.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open R7 client journal: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open R7 client journal returned no file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	uid, err := validateOwnerInfo(info, ownerFileMode, false)
	stat, ok := info.Sys().(*syscall.Stat_t)
	if err != nil || !ok || uid != directory.ownerUID || stat.Nlink != 1 {
		return nil, errors.New("R7 client journal file identity is unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(raw) > maximum {
		clear(raw)
		return nil, errors.New("R7 client journal exceeds its closed byte bound")
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(directory.dir.Fd()), name, &current,
		unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameUnixIdentity(info.Sys(), &current) {
		clear(raw)
		return nil, errors.New("R7 client journal identity changed while reading")
	}
	return raw, nil
}

func (directory *lockedJournalDirectory) write(journal clientJournal) error {
	if journal.fileName != "" && journal.fileName != journalActiveName {
		return errors.New("terminal R7 client journal is immutable")
	}
	return directory.publishJournal(journal, journalActiveName)
}

func (directory *lockedJournalDirectory) publishJournal(journal clientJournal,
	target string,
) error {
	if target != journalActiveName && !validTerminalName(target) {
		return errors.New("R7 client journal target is invalid")
	}
	payload, err := journal.canonical()
	if err != nil {
		return err
	}
	defer clear(payload)
	if err := directory.recoverStage(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(directory.dir.Fd()), journalStageName,
		unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL,
		uint32(ownerFileMode))
	if err != nil {
		return fmt.Errorf("create R7 client journal stage: %w", err)
	}
	stage := os.NewFile(uintptr(fd), journalStageName)
	if stage == nil {
		_ = unix.Close(fd)
		return errors.New("create R7 client journal stage returned no file")
	}
	cleanup := true
	defer func() {
		_ = stage.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.dir.Fd()), journalStageName, 0)
		}
	}()
	if _, err := stage.Write(payload); err != nil {
		return fmt.Errorf("write R7 client journal stage: %w", err)
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("persist R7 client journal stage: %w", err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("close R7 client journal stage: %w", err)
	}
	if err := unix.Renameat(int(directory.dir.Fd()), journalStageName,
		int(directory.dir.Fd()), target); err != nil {
		return fmt.Errorf("publish R7 client journal: %w", err)
	}
	cleanup = false
	return directory.dir.Sync()
}

func (directory *lockedJournalDirectory) markTerminal(expected clientJournal,
	operation agency.OperationKey,
) (clientJournal, error) {
	if expected.fileName != journalActiveName || !strings.HasPrefix(operation.String(), admissionOperationPrefix) {
		return clientJournal{}, errors.New("R7 terminal journal transition is invalid")
	}
	current, err := directory.load()
	if err != nil {
		return clientJournal{}, err
	}
	defer current.clear()
	if current.fileName != journalActiveName || current.fileDigest != expected.fileDigest {
		return clientJournal{}, errors.New("R7 client journal changed before terminal transition")
	}
	name := terminalName(operation)
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.dir.Fd()), name, &stat,
		unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return clientJournal{}, errors.New("R7 terminal journal already exists")
	} else if !errors.Is(err, syscall.ENOENT) {
		return clientJournal{}, err
	}
	if err := unix.Renameat(int(directory.dir.Fd()), journalActiveName,
		int(directory.dir.Fd()), name); err != nil {
		return clientJournal{}, fmt.Errorf("publish R7 terminal journal: %w", err)
	}
	if err := directory.dir.Sync(); err != nil {
		return clientJournal{}, fmt.Errorf("persist R7 terminal journal: %w", err)
	}
	terminal, err := directory.load()
	if err != nil || terminal.fileName != name || terminal.fileDigest != expected.fileDigest {
		terminal.clear()
		return clientJournal{}, errors.New("published R7 terminal journal differs")
	}
	return terminal, nil
}

// markPresented consumes the replay-only Current and captured candidates only
// after an accepted Receipt has reached stdout. Replacing the terminal file is
// atomic: before the rename the exact accepted operation remains replayable;
// after it, the same Attachment is durably ready for a fresh Current.
func (directory *lockedJournalDirectory) markPresented(expected clientJournal) (
	clientJournal, error,
) {
	if !validTerminalName(expected.fileName) || expected.CurrentOperation.IsZero() {
		return clientJournal{}, errors.New("R7 presented journal transition is invalid")
	}
	current, err := directory.load()
	if err != nil {
		return clientJournal{}, err
	}
	defer current.clear()
	if current.fileName != expected.fileName || current.fileDigest != expected.fileDigest ||
		current.CurrentOperation.IsZero() {
		return clientJournal{}, errors.New("R7 terminal journal changed before presentation")
	}
	reset := current
	reset.Attachment.Credential = append([]byte(nil), current.Attachment.Credential...)
	reset.CurrentOperation = agency.OperationKey{}
	reset.CurrentProjection = ""
	reset.Candidates = nil
	defer reset.clear()
	if err := directory.publishJournal(reset, current.fileName); err != nil {
		return clientJournal{}, err
	}
	presented, err := directory.load()
	if err != nil || presented.fileName != current.fileName ||
		!presented.CurrentOperation.IsZero() || presented.CurrentProjection != "" ||
		len(presented.Candidates) != 0 {
		presented.clear()
		return clientJournal{}, errors.New("presented R7 client journal differs")
	}
	return presented, nil
}

func (directory *lockedJournalDirectory) remove(expected clientJournal) error {
	if expected.fileName == "" {
		return errors.New("R7 client journal removal lacks identity")
	}
	current, err := directory.load()
	if err != nil {
		return err
	}
	defer current.clear()
	if current.fileName != expected.fileName || current.fileDigest != expected.fileDigest {
		return errors.New("R7 client journal changed before removal")
	}
	if err := unix.Unlinkat(int(directory.dir.Fd()), current.fileName, 0); err != nil {
		return fmt.Errorf("remove R7 client journal: %w", err)
	}
	return directory.dir.Sync()
}

func terminalName(operation agency.OperationKey) string {
	return journalTerminalHead + strings.TrimPrefix(operation.String(), admissionOperationPrefix) +
		journalTerminalTail
}

func validTerminalName(name string) bool {
	if !strings.HasPrefix(name, journalTerminalHead) || !strings.HasSuffix(name, journalTerminalTail) {
		return false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, journalTerminalHead), journalTerminalTail)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	valid := err == nil && len(decoded) == sha256.Size && base64.RawURLEncoding.EncodeToString(decoded) == value
	clear(decoded)
	return valid
}

func terminalOperation(name string) (agency.OperationKey, error) {
	if !validTerminalName(name) {
		return agency.OperationKey{}, errors.New("R7 terminal journal name is invalid")
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, journalTerminalHead), journalTerminalTail)
	return agency.NewOperationKey(admissionOperationPrefix + value)
}
