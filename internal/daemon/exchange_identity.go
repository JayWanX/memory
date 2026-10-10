package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
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

const (
	transportIdentityFile    = "peer-identity.json"
	transportIdentityPending = ".peer-identity.pending"
	transportIdentitySchema  = "memory.r7.peer-identity"
	transportIdentityVersion = 1
	ownerFileMode            = 0o600
	peerIdentityDomain       = "memory.r7.peer-identity.v1"
)

type transportIdentityWire struct {
	Schema     string `json:"schema"`
	Version    int    `json:"version"`
	PrivateKey string `json:"private_key"`
}

// TransportIdentity is the public setup projection of one durable node key.
// The private key never leaves the owner-only identity file or daemon memory.
type TransportIdentity struct {
	id        agency.OpaqueHandle
	publicKey ed25519.PublicKey
}

func (identity TransportIdentity) PeerID() agency.OpaqueHandle { return identity.id }
func (identity TransportIdentity) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), identity.publicKey...)
}

type loadedTransportIdentity struct {
	projection TransportIdentity
	privateKey ed25519.PrivateKey
}

// ProvisionTransportIdentity creates one durable Ed25519 identity or replays
// the already committed identity. Setup must call it before exchange is
// enabled; ordinary daemon Open never creates setup state.
func ProvisionTransportIdentity(stateDirectory string) (_ TransportIdentity, err error) {
	if err := requireOwnerDirectory(stateDirectory); err != nil {
		return TransportIdentity{}, fmt.Errorf("provision transport identity: %w", err)
	}
	lockContext, cancel := context.WithTimeout(context.Background(), provisionLockWait)
	defer cancel()
	lock, err := acquireProvisionLock(lockContext, stateDirectory)
	if err != nil {
		return TransportIdentity{}, err
	}
	defer func() { err = errors.Join(err, releaseProvisionLock(lock)) }()
	identity, _, err := provisionTransportIdentityLocked(stateDirectory)
	return identity, err
}

func provisionTransportIdentityLocked(stateDirectory string) (TransportIdentity, bool, error) {
	final := filepath.Join(stateDirectory, transportIdentityFile)
	pending := filepath.Join(stateDirectory, transportIdentityPending)
	if err := recoverLinkedTransportIdentity(stateDirectory, pending, final); err != nil {
		return TransportIdentity{}, false, err
	}
	if identity, found, err := replayTransportIdentity(final, pending); err != nil || found {
		return identity, found && err == nil, err
	}
	if identity, found, err := recoverPendingTransportIdentity(stateDirectory, pending, final); err != nil || found {
		return identity, false, err
	}
	identity, err := createTransportIdentity(stateDirectory, pending, final)
	return identity, false, err
}

func createTransportIdentity(directory, pending, final string) (TransportIdentity, error) {
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		return TransportIdentity{}, fmt.Errorf("provision transport identity: generate key: %w", err)
	}
	raw, identity, err := encodeTransportIdentity(privateKey)
	if err != nil {
		return TransportIdentity{}, err
	}
	if err := createPrivateFile(pending, raw); err != nil {
		if replay, pendingRaw, found, readErr := readTransportIdentityFile(pending); readErr == nil && found {
			if promoteErr := publishTransportIdentity(directory, pending, final, pendingRaw); promoteErr != nil {
				return TransportIdentity{}, promoteErr
			}
			return replay.projection, nil
		}
		return TransportIdentity{}, err
	}
	if err := syncOwnerDirectory(directory); err != nil {
		return TransportIdentity{}, err
	}
	if err := publishTransportIdentity(directory, pending, final, raw); err != nil {
		return TransportIdentity{}, err
	}
	return identity.projection, nil
}

func loadTransportIdentity(stateDirectory string) (loadedTransportIdentity, error) {
	if err := requireOwnerDirectory(stateDirectory); err != nil {
		return loadedTransportIdentity{}, fmt.Errorf("load transport identity: %w", err)
	}
	identity, _, found, err := readTransportIdentityFile(
		filepath.Join(stateDirectory, transportIdentityFile))
	if err != nil {
		return loadedTransportIdentity{}, err
	}
	if !found {
		return loadedTransportIdentity{}, errors.New("load transport identity: identity is not provisioned")
	}
	return identity, nil
}

func encodeTransportIdentity(privateKey ed25519.PrivateKey) ([]byte, loadedTransportIdentity, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, loadedTransportIdentity{}, errors.New("encode transport identity: invalid private key")
	}
	privateCopy := append(ed25519.PrivateKey(nil), privateKey...)
	derived := ed25519.NewKeyFromSeed(privateCopy.Seed())
	if !bytes.Equal(privateCopy, derived) {
		return nil, loadedTransportIdentity{}, errors.New("encode transport identity: inconsistent private key")
	}
	publicKey := privateCopy.Public().(ed25519.PublicKey)
	id, err := derivePeerIdentity(publicKey)
	if err != nil {
		return nil, loadedTransportIdentity{}, err
	}
	wire := transportIdentityWire{Schema: transportIdentitySchema,
		Version:    transportIdentityVersion,
		PrivateKey: base64.RawStdEncoding.EncodeToString(privateCopy)}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, loadedTransportIdentity{}, fmt.Errorf("encode transport identity: %w", err)
	}
	return raw, loadedTransportIdentity{projection: TransportIdentity{id: id,
		publicKey: append(ed25519.PublicKey(nil), publicKey...)}, privateKey: privateCopy}, nil
}

func parseTransportIdentity(raw []byte) (loadedTransportIdentity, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire transportIdentityWire
	if err := decoder.Decode(&wire); err != nil {
		return loadedTransportIdentity{}, errors.New("load transport identity: invalid identity document")
	}
	if err := requireIdentityJSONEOF(decoder); err != nil {
		return loadedTransportIdentity{}, err
	}
	rebuilt, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(rebuilt, raw) || wire.Schema != transportIdentitySchema ||
		wire.Version != transportIdentityVersion {
		return loadedTransportIdentity{}, errors.New("load transport identity: non-canonical identity document")
	}
	privateKey, err := base64.RawStdEncoding.DecodeString(wire.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return loadedTransportIdentity{}, errors.New("load transport identity: invalid private key")
	}
	_, identity, err := encodeTransportIdentity(ed25519.PrivateKey(privateKey))
	return identity, err
}

func derivePeerIdentity(publicKey ed25519.PublicKey) (agency.OpaqueHandle, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return agency.OpaqueHandle{}, errors.New("derive peer identity: invalid Ed25519 public key")
	}
	digest := agency.Sum(append([]byte(peerIdentityDomain+"\x00"), publicKey...))
	return agency.NewOpaqueHandle("peer:" + strings.TrimPrefix(digest.String(), "sha256:"))
}

func readTransportIdentityFile(path string) (loadedTransportIdentity, []byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return loadedTransportIdentity{}, nil, false, nil
	}
	if err != nil {
		return loadedTransportIdentity{}, nil, false,
			fmt.Errorf("load transport identity: inspect file: %w", err)
	}
	if err := requireOwnerRegularFile(info); err != nil {
		return loadedTransportIdentity{}, nil, false, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return loadedTransportIdentity{}, nil, false,
			fmt.Errorf("load transport identity: open file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return loadedTransportIdentity{}, nil, false,
			errors.New("load transport identity: opened file is unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return loadedTransportIdentity{}, nil, false,
			errors.New("load transport identity: file identity changed while opening")
	}
	if err := requireOwnerRegularFile(opened); err != nil {
		return loadedTransportIdentity{}, nil, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil || len(raw) == 0 || len(raw) > 1024 {
		return loadedTransportIdentity{}, nil, false,
			errors.New("load transport identity: identity document exceeds bound")
	}
	afterFD, fdErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if fdErr != nil || pathErr != nil || !os.SameFile(opened, afterFD) ||
		!os.SameFile(opened, afterPath) {
		return loadedTransportIdentity{}, nil, false,
			errors.New("load transport identity: file identity changed during read")
	}
	if err := requireOwnerRegularFile(afterFD); err != nil {
		return loadedTransportIdentity{}, nil, false, err
	}
	identity, err := parseTransportIdentity(raw)
	if err != nil {
		return loadedTransportIdentity{}, nil, false, err
	}
	return identity, raw, true, nil
}

func requireOwnerRegularFile(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != ownerFileMode {
		return errors.New("load transport identity: identity must be an owner-only regular file")
	}
	owner, err := fileOwnerUID(info)
	stat, ok := info.Sys().(*syscall.Stat_t)
	if err != nil || owner != uint32(os.Geteuid()) || !ok || stat.Nlink != 1 {
		return errors.New("load transport identity: identity owner or link count is invalid")
	}
	return nil
}

func createPrivateFile(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, ownerFileMode)
	if err != nil {
		return fmt.Errorf("provision transport identity: create pending file: %w", err)
	}
	writeErr := error(nil)
	if err := file.Chmod(ownerFileMode); err != nil {
		writeErr = err
	} else if _, err := file.Write(raw); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	writeErr = errors.Join(writeErr, file.Close())
	if writeErr != nil {
		return fmt.Errorf("provision transport identity: persist pending file: %w", writeErr)
	}
	return nil
}

func publishTransportIdentity(directory, pending, final string, expected []byte) error {
	linkErr := os.Link(pending, final)
	if errors.Is(linkErr, os.ErrNotExist) {
		return requireExactPublishedIdentity(final, expected)
	}
	if linkErr != nil && !errors.Is(linkErr, os.ErrExist) {
		return fmt.Errorf("provision transport identity: publish: %w", linkErr)
	}
	if linkErr == nil {
		if err := os.Remove(pending); err != nil {
			return fmt.Errorf("provision transport identity: settle published link: %w", err)
		}
	} else if sameIdentityLink(pending, final) {
		if err := os.Remove(pending); err != nil {
			return fmt.Errorf("provision transport identity: recover published link: %w", err)
		}
	}
	if err := syncOwnerDirectory(directory); err != nil {
		return err
	}
	identity, raw, found, err := readTransportIdentityFile(final)
	if err != nil || !found || identity.projection.id.IsZero() {
		return errors.Join(errors.New("provision transport identity: published identity is invalid"), err)
	}
	if len(expected) > 0 && !bytes.Equal(raw, expected) {
		return errors.New("provision transport identity: concurrent identity conflicts")
	}
	if err := os.Remove(pending); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("provision transport identity: remove pending file: %w", err)
	}
	return syncOwnerDirectory(directory)
}

func requireExactPublishedIdentity(final string, expected []byte) error {
	identity, raw, found, err := readTransportIdentityFile(final)
	if err != nil || !found || identity.projection.id.IsZero() ||
		len(expected) == 0 || !bytes.Equal(raw, expected) {
		return errors.Join(errors.New("provision transport identity: pending disappeared without exact replay"), err)
	}
	return nil
}

func sameIdentityLink(left, right string) bool {
	leftInfo, leftErr := os.Lstat(left)
	rightInfo, rightErr := os.Lstat(right)
	if leftErr != nil || rightErr != nil || !os.SameFile(leftInfo, rightInfo) {
		return false
	}
	leftOwner, ownerErr := fileOwnerUID(leftInfo)
	stat, ok := leftInfo.Sys().(*syscall.Stat_t)
	return ownerErr == nil && leftOwner == uint32(os.Geteuid()) && ok && stat.Nlink == 2 &&
		leftInfo.Mode().IsRegular() && leftInfo.Mode().Perm() == ownerFileMode
}

func settleMatchingPendingIdentity(path string, expected []byte) error {
	_, raw, found, err := readTransportIdentityFile(path)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if !bytes.Equal(raw, expected) {
		return errors.New("provision transport identity: pending identity conflicts with committed identity")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("provision transport identity: remove replayed pending file: %w", err)
	}
	return syncOwnerDirectory(filepath.Dir(path))
}

func syncOwnerDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sync transport identity directory: %w", err)
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}

func requireIdentityJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("load transport identity: identity document has trailing content")
	}
	return nil
}
