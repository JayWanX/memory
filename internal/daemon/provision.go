package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

var ErrProvision = errors.New("daemon: provision R7 node")

// ResolveProjectState returns the physical project root and its fixed R7 node
// directory without creating, opening, or repairing either path.
func ResolveProjectState(projectRoot string) (string, string, error) {
	root, err := resolvePhysicalProjectRoot(projectRoot)
	if err != nil {
		return "", "", err
	}
	return root, filepath.Join(root, ".memory", "agency"), nil
}

// ProvisionResult is the bounded setup receipt for one local R7 node. The
// Principal is derived, never configured or persisted as a second identity.
type ProvisionResult struct {
	stateDirectory string
	peerID         agency.OpaqueHandle
	principal      agency.AgentPrincipalID
	replayed       bool
}

func (result ProvisionResult) StateDirectory() string             { return result.stateDirectory }
func (result ProvisionResult) PeerID() agency.OpaqueHandle        { return result.peerID }
func (result ProvisionResult) Principal() agency.AgentPrincipalID { return result.principal }
func (result ProvisionResult) Replayed() bool                     { return result.replayed }

// Provision creates or verifies exactly one node below a physical project
// root. One owner-only lock covers identity, CAS, authority schema, and the
// default Principal so interrupted setup can converge forward on replay.
func Provision(ctx context.Context, projectRoot string) (result ProvisionResult, err error) {
	if ctx == nil {
		return ProvisionResult{}, fmt.Errorf("%w: context is required", ErrProvision)
	}
	if err := ctx.Err(); err != nil {
		return ProvisionResult{}, err
	}
	root, _, err := ResolveProjectState(projectRoot)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	stateDirectory, err := ensureProvisionDirectories(root)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	stateIdentity, err := snapshotOwnerDirectory(stateDirectory)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	lock, err := acquireProvisionLock(ctx, stateDirectory)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	defer func() {
		if releaseErr := releaseProvisionLock(lock); releaseErr != nil {
			result = ProvisionResult{}
			err = errors.Join(err, releaseErr)
		}
	}()
	return provisionLocked(ctx, stateDirectory, stateIdentity)
}

func provisionLocked(ctx context.Context, stateDirectory string,
	stateIdentity os.FileInfo,
) (result ProvisionResult, err error) {
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	if err := rejectAuthorityWithoutIdentity(stateDirectory); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	authorityPresent, err := authorityStatePresent(stateDirectory)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: inspect authority: %w", ErrProvision, err)
	}
	if authorityPresent {
		if _, present, err := inspectProvisionLock(filepath.Join(stateDirectory, ensureLockFile)); err != nil || !present {
			return ProvisionResult{}, fmt.Errorf("%w: required ensure lock: %w",
				ErrProvision, errors.Join(errors.New("lock is unavailable"), err))
		}
	} else if err := provisionEnsureLock(stateDirectory); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: provision ensure lock: %w", ErrProvision, err)
	}

	identity, identityReplayed, err := provisionTransportIdentityLocked(stateDirectory)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	principal, err := DefaultAgentPrincipal(identity.PublicKey())
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	objects, err := openProvisionCAS(stateDirectory, authorityPresent)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: open Artifact store: %w", ErrProvision, err)
	}
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	store, err := authority.OpenWithArtifactVerifier(ctx,
		filepath.Join(stateDirectory, authorityFileName), objects)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: open authority: %w", ErrProvision, err)
	}
	storeClosed := false
	defer func() {
		if storeClosed {
			return
		}
		if closeErr := store.Close(); closeErr != nil {
			result = ProvisionResult{}
			err = errors.Join(err, fmt.Errorf("%w: close authority: %v", ErrProvision, closeErr))
		}
	}()
	principalReplayed, err := store.ProvisionInitialPrincipal(ctx, principal)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	if err := store.RequirePrincipal(ctx, principal); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: verify Principal: %w", ErrProvision, err)
	}
	if err := store.Close(); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: close authority: %w", ErrProvision, err)
	}
	storeClosed = true
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return ProvisionResult{}, fmt.Errorf("%w: %w", ErrProvision, err)
	}
	return ProvisionResult{stateDirectory: stateDirectory, peerID: identity.PeerID(),
		principal: principal, replayed: identityReplayed && principalReplayed}, nil
}

func openProvisionCAS(stateDirectory string, authorityPresent bool) (*artifact.Store, error) {
	objectsParent := filepath.Join(stateDirectory, "objects")
	objectsRoot := filepath.Join(objectsParent, "sha256")
	if authorityPresent {
		if err := requireOwnerDirectory(objectsParent); err != nil {
			return nil, err
		}
		return artifact.OpenExisting(objectsRoot)
	}
	if err := ensureOwnedDirectory(objectsParent, true); err != nil {
		return nil, fmt.Errorf("prepare CAS parent: %w", err)
	}
	return artifact.Open(objectsRoot)
}

func requireProvisionedLayout(stateDirectory string) error {
	if err := requireOwnerDirectory(stateDirectory); err != nil {
		return err
	}
	for _, directory := range []string{
		filepath.Join(stateDirectory, "objects"),
		filepath.Join(stateDirectory, "objects", "sha256"),
		filepath.Join(stateDirectory, "objects", "sha256", ".tmp"),
	} {
		if err := requireOwnerDirectory(directory); err != nil {
			return fmt.Errorf("required private directory %q: %w", filepath.Base(directory), err)
		}
	}
	if _, present, err := inspectProvisionLock(filepath.Join(stateDirectory, ensureLockFile)); err != nil || !present {
		return errors.Join(errors.New("required ensure lock is unavailable"), err)
	}
	return nil
}

func rejectAuthorityWithoutIdentity(stateDirectory string) error {
	identityPath := filepath.Join(stateDirectory, transportIdentityFile)
	if _, err := os.Lstat(identityPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect transport identity: %w", err)
	}
	present, err := authorityStatePresent(stateDirectory)
	if err != nil {
		return err
	}
	if present {
		return errors.New("authority exists without its transport identity")
	}
	return nil
}

func authorityStatePresent(stateDirectory string) (bool, error) {
	for _, path := range authorityFilePaths(stateDirectory) {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect authority state: %w", err)
		}
	}
	return false, nil
}

func authorityFilePaths(stateDirectory string) []string {
	database := filepath.Join(stateDirectory, authorityFileName)
	return []string{database, database + ".writer.lock", database + "-wal", database + "-shm"}
}

func snapshotOwnerDirectory(path string) (os.FileInfo, error) {
	if err := requireOwnerDirectory(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect owner directory: %w", err)
	}
	return info, nil
}

// R7 T0 does not claim protection from arbitrary hostile code running as the
// same OS user. These inode checks still fail closed on accidental replacement
// across each durable provisioning stage without introducing an alternate
// dirfd-based filesystem architecture.
func verifyOwnerDirectoryIdentity(path string, expected os.FileInfo) error {
	if err := requireOwnerDirectory(path); err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || expected == nil || !os.SameFile(current, expected) {
		return errors.New("owner directory identity changed during provisioning")
	}
	return nil
}

func resolvePhysicalProjectRoot(requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", errors.New("project root is empty")
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	absolute = filepath.Clean(absolute)
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve physical project root: %w", err)
	}
	if physical != absolute {
		return "", errors.New("project root must already name its physical path")
	}
	info, err := os.Lstat(physical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("project root must be an existing real directory")
	}
	owner, ownerErr := fileOwnerUID(info)
	if ownerErr != nil || owner != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("project root must be owned by the current user and not writable by others")
	}
	return physical, nil
}

func ensureProvisionDirectories(root string) (string, error) {
	memoryDirectory := filepath.Join(root, ".memory")
	if err := ensureOwnedDirectory(memoryDirectory, false); err != nil {
		return "", err
	}
	stateDirectory := filepath.Join(memoryDirectory, "agency")
	if err := ensureOwnedDirectory(stateDirectory, true); err != nil {
		return "", err
	}
	return stateDirectory, nil
}

func ensureOwnedDirectory(path string, exactPrivate bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		mkdirErr := os.Mkdir(path, ownerDirectoryMode)
		created := mkdirErr == nil
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("create private directory %q: %w", filepath.Base(path), mkdirErr)
		}
		if created {
			if err := os.Chmod(path, ownerDirectoryMode); err != nil {
				return fmt.Errorf("protect private directory %q: %w", filepath.Base(path), err)
			}
			if err := syncProvisionDirectory(filepath.Dir(path)); err != nil {
				return err
			}
		}
		info, err = os.Lstat(path)
		if err != nil {
			return fmt.Errorf("protect private directory %q: %w", filepath.Base(path), err)
		}
	}
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("provision path is not a real directory")
	}
	owner, ownerErr := fileOwnerUID(info)
	if ownerErr != nil || owner != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
		return errors.New("provision directory is not safely owned")
	}
	if exactPrivate && info.Mode().Perm() != ownerDirectoryMode {
		return fmt.Errorf("provision directory mode is %04o, want %04o",
			info.Mode().Perm(), ownerDirectoryMode)
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return errors.New("provision directory has a symlinked ancestor")
	}
	return nil
}

func syncProvisionDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sync provision directory: %w", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
}
