package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	exchangeConfigFile    = "exchange.json"
	exchangeConfigPending = ".exchange.pending"
	exchangeConfigSchema  = "memory.r7.exchange"
)

type exchangeConfigWire struct {
	Schema            string `json:"schema"`
	Version           int    `json:"version"`
	ListenAddress     string `json:"listen_address"`
	AdvertisedAddress string `json:"advertised_address"`
}

type exchangeConfig struct {
	listenAddress     string
	advertisedAddress string
	canonical         []byte
}

// ConfigureExchange persists one immutable, owner-only deployment endpoint.
// It stores no Peer identity, route, alias, or policy.
func ConfigureExchange(ctx context.Context, stateDirectory, listenAddress,
	advertisedAddress string,
) (result PeerCard, err error) {
	if ctx == nil {
		return PeerCard{}, fmt.Errorf("%w: context is required", ErrPeerSetup)
	}
	if err := ctx.Err(); err != nil {
		return PeerCard{}, err
	}
	config, err := newExchangeConfig(listenAddress, advertisedAddress)
	if err != nil {
		return PeerCard{}, err
	}
	stateIdentity, err := snapshotOwnerDirectory(stateDirectory)
	if err != nil {
		return PeerCard{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	lock, err := acquireExistingProvisionLock(ctx, stateDirectory)
	if err != nil {
		return PeerCard{}, err
	}
	defer func() { err = errors.Join(err, releaseProvisionLock(lock)) }()
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return PeerCard{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	if err := requireProvisionedLayout(stateDirectory); err != nil {
		return PeerCard{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	identity, err := loadTransportIdentity(stateDirectory)
	if err != nil {
		return PeerCard{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	if _, err := provisionExchangeConfigLocked(stateDirectory, config); err != nil {
		return PeerCard{}, err
	}
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return PeerCard{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	return newPeerCard(identity.projection, config.advertisedAddress)
}

func newExchangeConfig(listenAddress, advertisedAddress string) (exchangeConfig, error) {
	if err := validateTCPAddress(listenAddress, true); err != nil {
		return exchangeConfig{}, fmt.Errorf("%w: listen address: %v", ErrPeerSetup, err)
	}
	if err := validateTCPAddress(advertisedAddress, false); err != nil {
		return exchangeConfig{}, fmt.Errorf("%w: advertised address: %v", ErrPeerSetup, err)
	}
	wire := exchangeConfigWire{Schema: exchangeConfigSchema, Version: peerSetupVersion,
		ListenAddress: listenAddress, AdvertisedAddress: advertisedAddress}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > maxPeerSetupDocumentSize {
		return exchangeConfig{}, fmt.Errorf("%w: exchange configuration is not representable", ErrPeerSetup)
	}
	return exchangeConfig{listenAddress: listenAddress, advertisedAddress: advertisedAddress,
		canonical: canonical}, nil
}

func parseExchangeConfig(raw []byte) (exchangeConfig, error) {
	if len(raw) == 0 || len(raw) > maxPeerSetupDocumentSize {
		return exchangeConfig{}, fmt.Errorf("%w: exchange configuration exceeds its byte bound", ErrPeerSetup)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire exchangeConfigWire
	if err := decoder.Decode(&wire); err != nil || requireJSONEOF(decoder) != nil {
		return exchangeConfig{}, fmt.Errorf("%w: invalid exchange configuration", ErrPeerSetup)
	}
	rebuilt, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(rebuilt, raw) || wire.Schema != exchangeConfigSchema ||
		wire.Version != peerSetupVersion {
		return exchangeConfig{}, fmt.Errorf("%w: non-canonical exchange configuration", ErrPeerSetup)
	}
	return newExchangeConfig(wire.ListenAddress, wire.AdvertisedAddress)
}

func provisionExchangeConfigLocked(stateDirectory string,
	requested exchangeConfig,
) (bool, error) {
	current, present, err := recoverExchangeConfigLocked(stateDirectory)
	if err != nil {
		return false, err
	}
	if present {
		if !bytes.Equal(current.canonical, requested.canonical) {
			return false, fmt.Errorf("%w: exchange configuration is immutable", ErrPeerSetup)
		}
		return true, nil
	}
	pending := filepath.Join(stateDirectory, exchangeConfigPending)
	if err := createPrivateFile(pending, requested.canonical); err != nil {
		return false, fmt.Errorf("%w: persist pending exchange configuration: %v", ErrPeerSetup, err)
	}
	if err := syncOwnerDirectory(stateDirectory); err != nil {
		return false, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	final := filepath.Join(stateDirectory, exchangeConfigFile)
	if err := os.Link(pending, final); err != nil {
		return false, fmt.Errorf("%w: publish exchange configuration: %v", ErrPeerSetup, err)
	}
	if err := os.Remove(pending); err != nil {
		return false, fmt.Errorf("%w: settle exchange configuration: %v", ErrPeerSetup, err)
	}
	if err := syncOwnerDirectory(stateDirectory); err != nil {
		return false, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	return false, nil
}

func readExchangeConfigLocked(stateDirectory string) (exchangeConfig, bool, error) {
	final := filepath.Join(stateDirectory, exchangeConfigFile)
	pending := filepath.Join(stateDirectory, exchangeConfigPending)
	finalRaw, finalPresent, err := readOwnerDocument(final)
	if err != nil {
		return exchangeConfig{}, false, err
	}
	_, pendingPresent, err := readOwnerDocument(pending)
	if err != nil {
		return exchangeConfig{}, false, err
	}
	if pendingPresent {
		return exchangeConfig{}, false,
			fmt.Errorf("%w: pending exchange configuration requires peer prepare replay", ErrPeerSetup)
	}
	if !finalPresent {
		return exchangeConfig{}, false, nil
	}
	config, err := parseExchangeConfig(finalRaw)
	if err != nil {
		return exchangeConfig{}, false, err
	}
	return config, true, nil
}

func recoverExchangeConfigLocked(stateDirectory string) (exchangeConfig, bool, error) {
	final := filepath.Join(stateDirectory, exchangeConfigFile)
	pending := filepath.Join(stateDirectory, exchangeConfigPending)
	if err := recoverLinkedExchangeConfig(stateDirectory, pending, final); err != nil {
		return exchangeConfig{}, false, err
	}
	finalRaw, finalPresent, err := readOwnerDocument(final)
	if err != nil {
		return exchangeConfig{}, false, err
	}
	pendingRaw, pendingPresent, pendingErr := readOwnerDocument(pending)
	if pendingErr != nil {
		return exchangeConfig{}, false, pendingErr
	}
	if finalPresent {
		return settleCommittedExchangeConfig(stateDirectory, pending, finalRaw,
			pendingRaw, pendingPresent)
	}
	if !pendingPresent {
		return exchangeConfig{}, false, nil
	}
	return publishPendingExchangeConfig(stateDirectory, pending, final, pendingRaw)
}

func recoverLinkedExchangeConfig(stateDirectory, pending, final string) error {
	if !sameIdentityLink(pending, final) {
		return nil
	}
	if err := os.Remove(pending); err != nil {
		return fmt.Errorf("%w: recover linked exchange configuration: %v", ErrPeerSetup, err)
	}
	return syncOwnerDirectory(stateDirectory)
}

func settleCommittedExchangeConfig(stateDirectory, pending string, finalRaw,
	pendingRaw []byte, pendingPresent bool,
) (exchangeConfig, bool, error) {
	config, err := parseExchangeConfig(finalRaw)
	if err != nil {
		return exchangeConfig{}, false, err
	}
	if !pendingPresent {
		return config, true, nil
	}
	if !bytes.Equal(finalRaw, pendingRaw) {
		return exchangeConfig{}, false,
			fmt.Errorf("%w: pending exchange configuration conflicts", ErrPeerSetup)
	}
	if err := os.Remove(pending); err != nil {
		return exchangeConfig{}, false, err
	}
	if err := syncOwnerDirectory(stateDirectory); err != nil {
		return exchangeConfig{}, false, err
	}
	return config, true, nil
}

func publishPendingExchangeConfig(stateDirectory, pending, final string,
	pendingRaw []byte,
) (exchangeConfig, bool, error) {
	config, err := parseExchangeConfig(pendingRaw)
	if err != nil {
		return exchangeConfig{}, false, err
	}
	if err := os.Link(pending, final); err != nil {
		return exchangeConfig{}, false, fmt.Errorf("%w: recover exchange configuration: %v", ErrPeerSetup, err)
	}
	if err := os.Remove(pending); err != nil {
		return exchangeConfig{}, false, err
	}
	if err := syncOwnerDirectory(stateDirectory); err != nil {
		return exchangeConfig{}, false, err
	}
	return config, true, nil
}
