package attach

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	piRuntime     = "pi"
	journalSchema = 1
	projectedMode = 0o644
	journalMode   = 0o600
)

var (
	ErrUnsafe = errors.New("attach: unsafe path")
	ErrDrift  = errors.New("attach: owned projection drift")
)

// InstallReceipt identifies only the exact files owned by this projection.
type InstallReceipt struct {
	CurrentExtensionPath string
	ExtensionPath        string
	GuidePath            string
	JournalPath          string
	Revision             string
	Replayed             bool
}

type fileRecord struct {
	Digest string `json:"digest"`
	Mode   string `json:"mode"`
	Path   string `json:"path"`
}

type journalRevision struct {
	Files   []fileRecord `json:"files"`
	Runtime string       `json:"runtime"`
	Schema  int          `json:"schema"`
}

type ownershipJournal struct {
	Files    []fileRecord `json:"files"`
	Revision string       `json:"revision"`
	Runtime  string       `json:"runtime"`
	Schema   int          `json:"schema"`
}

type projectedFile struct {
	content []byte
	path    string
	record  fileRecord
}

type installPlan struct {
	files        []projectedFile
	journal      ownershipJournal
	journalBytes []byte
	journalPath  string
	workspace    string
}

type installBoundary func(string) error

// InstallPi projects the embedded guide and extension into one physical,
// project-local workspace. It never discovers or mutates another runtime.
func InstallPi(workspace string) (InstallReceipt, error) {
	return installPi(workspace, nil)
}

func installPi(workspace string, boundary installBoundary) (InstallReceipt, error) {
	plan, err := prepareInstall(workspace)
	if err != nil {
		return InstallReceipt{}, err
	}
	receipt := receiptFor(plan)
	_, statErr := os.Lstat(plan.journalPath)
	journalExisted := statErr == nil
	switch {
	case statErr == nil:
		err = readJournal(plan)
		if err != nil {
			return InstallReceipt{}, err
		}
		err = cleanupStage(filepath.Dir(plan.journalPath), plan.journalPath,
			plan.journalBytes, journalMode)
	case errors.Is(statErr, os.ErrNotExist):
		err = beginInstall(plan)
	default:
		err = fmt.Errorf("%w: inspect ownership journal: %v", ErrUnsafe, statErr)
	}
	if err != nil {
		return InstallReceipt{}, err
	}
	changed, err := convergeInstall(plan, boundary)
	if err != nil {
		return InstallReceipt{}, err
	}
	receipt.Replayed = journalExisted && !changed
	return receipt, nil
}

// VerifyPi proves that the exact embedded revision owns the exact projected
// bytes at the fixed project-local paths.
func VerifyPi(workspace string) error {
	plan, err := prepareInstall(workspace)
	if err != nil {
		return err
	}
	if err := readJournal(plan); err != nil {
		return err
	}
	return verifyFiles(plan)
}

func prepareInstall(workspace string) (installPlan, error) {
	if workspace == "" || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return installPlan{}, fmt.Errorf("%w: workspace must be an absolute clean path", ErrUnsafe)
	}
	physical, err := filepath.EvalSymlinks(workspace)
	if err != nil || physical != workspace {
		return installPlan{}, fmt.Errorf("%w: workspace must be a physical path", ErrUnsafe)
	}
	if err := requireSafeDirectory(workspace, false); err != nil {
		return installPlan{}, err
	}
	projection, err := Load()
	if err != nil {
		return installPlan{}, err
	}
	files := projectedFiles(workspace, projection)
	records := make([]fileRecord, len(files))
	for index := range files {
		records[index] = files[index].record
	}
	revisionBytes, err := json.Marshal(journalRevision{Files: records, Runtime: piRuntime,
		Schema: journalSchema})
	if err != nil {
		return installPlan{}, fmt.Errorf("encode Pi projection revision: %w", err)
	}
	revision := digest(revisionBytes)
	journal := ownershipJournal{Files: records, Revision: revision, Runtime: piRuntime,
		Schema: journalSchema}
	journalBytes, err := canonicalJournal(journal)
	if err != nil {
		return installPlan{}, err
	}
	return installPlan{files: files, journal: journal, journalBytes: journalBytes,
		journalPath: filepath.Join(workspace, journalDirectoryRelative(), "ownership.json"),
		workspace:   workspace}, nil
}

func projectedFiles(workspace string, projection Projection) []projectedFile {
	sources := []struct {
		content  []byte
		relative string
	}{
		{projection.Guide(), filepath.Join(".pi", "skills", "memoryd", "SKILL.md")},
		{projection.PiExtension(), filepath.Join(".pi", "extensions", "memoryd.ts")},
		{projection.PiCurrentExtension(), filepath.Join(".pi", "extensions", "memoryd-current.ts")},
	}
	files := make([]projectedFile, 0, len(sources))
	for _, source := range sources {
		record := fileRecord{Digest: digest(source.content), Mode: "0644",
			Path: filepath.ToSlash(source.relative)}
		files = append(files, projectedFile{content: clone(source.content),
			path: filepath.Join(workspace, source.relative), record: record})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].record.Path < files[j].record.Path })
	return files
}

func canonicalJournal(journal ownershipJournal) ([]byte, error) {
	raw, err := json.Marshal(journal)
	if err != nil {
		return nil, fmt.Errorf("encode Pi ownership journal: %w", err)
	}
	return append(raw, '\n'), nil
}

func receiptFor(plan installPlan) InstallReceipt {
	receipt := InstallReceipt{JournalPath: plan.journalPath, Revision: plan.journal.Revision}
	for _, file := range plan.files {
		switch file.record.Path {
		case ".pi/extensions/memoryd-current.ts":
			receipt.CurrentExtensionPath = file.path
		case ".pi/extensions/memoryd.ts":
			receipt.ExtensionPath = file.path
		case ".pi/skills/memoryd/SKILL.md":
			receipt.GuidePath = file.path
		}
	}
	return receipt
}

func beginInstall(plan installPlan) error {
	if err := inspectFreshTargets(plan); err != nil {
		return err
	}
	if err := ensureJournalDirectory(plan); err != nil {
		return err
	}
	if err := publishExclusive(filepath.Dir(plan.journalPath), plan.journalPath,
		plan.journalBytes, journalMode); err != nil {
		if readErr := readJournal(plan); readErr != nil {
			return fmt.Errorf("%w: publish ownership journal: %v", ErrDrift, err)
		}
	}
	return nil
}

func convergeInstall(plan installPlan, boundary installBoundary) (bool, error) {
	if err := runBoundary(boundary, "after_journal"); err != nil {
		return false, err
	}
	if err := ensureProjectionDirectories(plan); err != nil {
		return false, err
	}
	changed := false
	for _, file := range plan.files {
		created, err := convergeFile(plan, file)
		if err != nil {
			return false, err
		}
		changed = changed || created
		if err := runBoundary(boundary, "after_file:"+file.record.Path); err != nil {
			return false, err
		}
	}
	if err := verifyFiles(plan); err != nil {
		return false, err
	}
	if err := cleanupStage(filepath.Dir(plan.journalPath), plan.journalPath,
		plan.journalBytes, journalMode); err != nil {
		return false, err
	}
	return changed, nil
}

func readJournal(plan installPlan) error {
	raw, err := readJournalBytes(plan)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, plan.journalBytes) {
		return fmt.Errorf("%w: journal does not own this revision", ErrDrift)
	}
	return nil
}

func readJournalBytes(plan installPlan) ([]byte, error) {
	if err := requireDirectoryChain(plan.workspace, journalDirectoryRelative()); err != nil {
		return nil, err
	}
	if err := requireSafeDirectory(filepath.Dir(plan.journalPath), true); err != nil {
		return nil, err
	}
	raw, _, err := readExactFile(plan.journalPath, journalMode, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("%w: read ownership journal: %v", ErrDrift, err)
	}
	return raw, nil
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func runBoundary(boundary installBoundary, stage string) error {
	if boundary == nil {
		return nil
	}
	return boundary(stage)
}
