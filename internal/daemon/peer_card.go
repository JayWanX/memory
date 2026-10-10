package daemon

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

const peerCardSchema = "memory.r7.peer-card"

// PeerCard is the bounded public projection of one node's transport endpoint.
// It is an owner-exchanged candidate, not enrollment authority.
type PeerCard struct {
	peerID    agency.OpaqueHandle
	publicKey ed25519.PublicKey
	address   string
	canonical []byte
}

type peerCardWire struct {
	Schema           string `json:"schema"`
	Version          int    `json:"version"`
	PeerID           string `json:"peer_id"`
	PublicKey        string `json:"public_key"`
	TransportAddress string `json:"transport_address"`
}

func (card PeerCard) PeerID() agency.OpaqueHandle { return card.peerID }
func (card PeerCard) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), card.publicKey...)
}
func (card PeerCard) TransportAddress() string { return card.address }
func (card PeerCard) CanonicalJSON() []byte    { return append([]byte(nil), card.canonical...) }

// ParsePeerCardCanonicalJSON validates a public card before any local setup
// mutation. Peer identity is recomputed from the pinned Ed25519 key.
func ParsePeerCardCanonicalJSON(raw []byte) (PeerCard, error) {
	if len(raw) == 0 || len(raw) > maxPeerSetupDocumentSize {
		return PeerCard{}, fmt.Errorf("%w: Peer Card exceeds its byte bound", ErrPeerSetup)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire peerCardWire
	if err := decoder.Decode(&wire); err != nil || requireJSONEOF(decoder) != nil {
		return PeerCard{}, fmt.Errorf("%w: invalid Peer Card", ErrPeerSetup)
	}
	rebuilt, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(rebuilt, raw) || wire.Schema != peerCardSchema ||
		wire.Version != peerSetupVersion {
		return PeerCard{}, fmt.Errorf("%w: non-canonical Peer Card", ErrPeerSetup)
	}
	peerID, err := agency.NewOpaqueHandle(wire.PeerID)
	if err != nil {
		return PeerCard{}, fmt.Errorf("%w: invalid Peer Card identity", ErrPeerSetup)
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(wire.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize ||
		base64.RawStdEncoding.EncodeToString(publicKey) != wire.PublicKey {
		return PeerCard{}, fmt.Errorf("%w: invalid Peer Card public key", ErrPeerSetup)
	}
	derived, err := derivePeerIdentity(ed25519.PublicKey(publicKey))
	if err != nil || derived != peerID {
		return PeerCard{}, fmt.Errorf("%w: Peer Card identity is not derived from its key", ErrPeerSetup)
	}
	if err := validateTCPAddress(wire.TransportAddress, false); err != nil {
		return PeerCard{}, fmt.Errorf("%w: advertised address: %v", ErrPeerSetup, err)
	}
	return PeerCard{peerID: peerID, publicKey: append(ed25519.PublicKey(nil), publicKey...),
		address: wire.TransportAddress, canonical: append([]byte(nil), raw...)}, nil
}

func newPeerCard(identity TransportIdentity, address string) (PeerCard, error) {
	if identity.PeerID().IsZero() || len(identity.PublicKey()) != ed25519.PublicKeySize {
		return PeerCard{}, fmt.Errorf("%w: transport identity is incomplete", ErrPeerSetup)
	}
	if err := validateTCPAddress(address, false); err != nil {
		return PeerCard{}, fmt.Errorf("%w: advertised address: %v", ErrPeerSetup, err)
	}
	wire := peerCardWire{Schema: peerCardSchema, Version: peerSetupVersion,
		PeerID:           identity.PeerID().String(),
		PublicKey:        base64.RawStdEncoding.EncodeToString(identity.PublicKey()),
		TransportAddress: address}
	canonical, err := json.Marshal(wire)
	if err != nil || len(canonical) > maxPeerSetupDocumentSize {
		return PeerCard{}, fmt.Errorf("%w: Peer Card is not representable", ErrPeerSetup)
	}
	return PeerCard{peerID: identity.PeerID(), publicKey: identity.PublicKey(),
		address: address, canonical: canonical}, nil
}
