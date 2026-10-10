package peerlink

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	protocolALPN      = "memory-peerlink/1"
	certificatePeriod = 10 * 365 * 24 * time.Hour
	maxAddressBytes   = 512
)

// Identity is one node's owner-provided transport identity. ID is local
// configuration metadata: it is never encoded in the certificate or a frame.
type Identity struct {
	ID         agency.OpaqueHandle
	PrivateKey ed25519.PrivateKey
}

// Peer is one owner-enrolled public-key pin. Address is required only when the
// value is used as a client destination.
type Peer struct {
	ID        agency.OpaqueHandle
	PublicKey ed25519.PublicKey
	Address   string
}

// AuthenticatedPeer is derived only from the TLS certificate public key and
// the server's immutable enrollment map. A wire field can never construct it.
type AuthenticatedPeer struct {
	id        agency.OpaqueHandle
	publicKey ed25519.PublicKey
}

func (peer AuthenticatedPeer) ID() agency.OpaqueHandle { return peer.id }
func (peer AuthenticatedPeer) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), peer.publicKey...)
}

type localIdentity struct {
	id          agency.OpaqueHandle
	privateKey  ed25519.PrivateKey
	publicKey   ed25519.PublicKey
	certificate tls.Certificate
}

type publicKeyPin [ed25519.PublicKeySize]byte

func prepareIdentity(identity Identity) (localIdentity, error) {
	if identity.ID.IsZero() || len(identity.PrivateKey) != ed25519.PrivateKeySize {
		return localIdentity{}, fmt.Errorf("%w: complete Ed25519 identity is required", ErrInput)
	}
	privateKey := append(ed25519.PrivateKey(nil), identity.PrivateKey...)
	derived := ed25519.NewKeyFromSeed(privateKey.Seed())
	if !bytes.Equal(privateKey, derived) {
		return localIdentity{}, fmt.Errorf("%w: Ed25519 identity key is inconsistent", ErrInput)
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return localIdentity{}, fmt.Errorf("%w: identity key is not Ed25519", ErrInput)
	}
	certificate, err := selfSignedCertificate(privateKey, publicKey, time.Now().UTC())
	if err != nil {
		return localIdentity{}, err
	}
	return localIdentity{id: identity.ID, privateKey: privateKey,
		publicKey: append(ed25519.PublicKey(nil), publicKey...), certificate: certificate}, nil
}

func selfSignedCertificate(privateKey ed25519.PrivateKey, publicKey ed25519.PublicKey,
	now time.Time,
) (tls.Certificate, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create peer certificate serial: %w", err)
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(certificatePeriod),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	encoded, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create self-signed peer certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(encoded)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse self-signed peer certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{encoded}, PrivateKey: privateKey, Leaf: leaf}, nil
}

func preparePeer(peer Peer, requireAddress bool) (Peer, error) {
	if peer.ID.IsZero() || len(peer.PublicKey) != ed25519.PublicKeySize {
		return Peer{}, fmt.Errorf("%w: complete peer ID and Ed25519 key are required", ErrInput)
	}
	if requireAddress && !validAddress(peer.Address) {
		return Peer{}, fmt.Errorf("%w: bounded peer address is required", ErrInput)
	}
	return Peer{ID: peer.ID, PublicKey: append(ed25519.PublicKey(nil), peer.PublicKey...),
		Address: peer.Address}, nil
}

func validAddress(value string) bool {
	return value != "" && len(value) <= maxAddressBytes && !strings.ContainsRune(value, '\x00')
}

func peerPins(local localIdentity, peers []Peer) (map[publicKeyPin]Peer, error) {
	if len(peers) == 0 {
		return nil, fmt.Errorf("%w: at least one enrolled peer is required", ErrInput)
	}
	result := make(map[publicKeyPin]Peer, len(peers))
	ids := make(map[string]struct{}, len(peers))
	for _, input := range peers {
		peer, err := preparePeer(input, false)
		if err != nil {
			return nil, err
		}
		if peer.ID == local.id || bytes.Equal(peer.PublicKey, local.publicKey) {
			return nil, fmt.Errorf("%w: a peer cannot be the local identity", ErrInput)
		}
		pin := pinFor(peer.PublicKey)
		if _, duplicate := result[pin]; duplicate {
			return nil, fmt.Errorf("%w: duplicate enrolled public key", ErrInput)
		}
		if _, duplicate := ids[peer.ID.String()]; duplicate {
			return nil, fmt.Errorf("%w: duplicate enrolled peer ID", ErrInput)
		}
		ids[peer.ID.String()] = struct{}{}
		peer.Address = ""
		result[pin] = peer
	}
	return result, nil
}

func pinFor(publicKey ed25519.PublicKey) publicKeyPin {
	var pin publicKeyPin
	copy(pin[:], publicKey)
	return pin
}

func authenticateConnection(state tls.ConnectionState, pins map[publicKeyPin]Peer,
	now time.Time,
) (AuthenticatedPeer, error) {
	if state.NegotiatedProtocol != protocolALPN || len(state.PeerCertificates) != 1 {
		return AuthenticatedPeer{}, fmt.Errorf("%w: exact peer certificate and protocol are required",
			ErrAuthentication)
	}
	certificate := state.PeerCertificates[0]
	publicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize ||
		certificate.PublicKeyAlgorithm != x509.Ed25519 ||
		certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) ||
		certificate.CheckSignature(certificate.SignatureAlgorithm,
			certificate.RawTBSCertificate, certificate.Signature) != nil {
		return AuthenticatedPeer{}, fmt.Errorf("%w: invalid self-signed Ed25519 certificate",
			ErrAuthentication)
	}
	peer, enrolled := pins[pinFor(publicKey)]
	if !enrolled {
		return AuthenticatedPeer{}, fmt.Errorf("%w: public key is not enrolled", ErrAuthentication)
	}
	return AuthenticatedPeer{id: peer.ID,
		publicKey: append(ed25519.PublicKey(nil), peer.PublicKey...)}, nil
}

func serverTLSConfig(identity localIdentity, pins map[publicKeyPin]Peer) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{identity.certificate},
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{protocolALPN},
		VerifyConnection: func(state tls.ConnectionState) error {
			_, err := authenticateConnection(state, pins, time.Now().UTC())
			return err
		},
	}
}

func clientTLSConfig(identity localIdentity, peer Peer) *tls.Config {
	pins := map[publicKeyPin]Peer{pinFor(peer.PublicKey): peer}
	return &tls.Config{
		Certificates: []tls.Certificate{identity.certificate},
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		NextProtos:   []string{protocolALPN},
		// Verification is replaced by the exact Ed25519 public-key pin below;
		// no DNS name or CA authority participates in peer identity.
		InsecureSkipVerify: true, //nolint:gosec -- exact key pin is the trust root
		VerifyConnection: func(state tls.ConnectionState) error {
			_, err := authenticateConnection(state, pins, time.Now().UTC())
			return err
		},
	}
}
