package peerlink

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
)

const (
	defaultHandshakeTimeout = 5 * time.Second
	defaultRequestTimeout   = 15 * time.Second
	defaultShutdownTimeout  = 5 * time.Second
	maximumTimeout          = time.Minute
	defaultMaxHandlers      = 8
	maximumHandlers         = 64
)

type ClientOptions struct {
	Identity         Identity
	Artifacts        *artifact.Store
	HandshakeTimeout time.Duration
	RequestTimeout   time.Duration
}

// Client opens one bounded TLS/TCP connection per operation. It owns no
// delivery queue, retry state, route policy, or admission state.
type Client struct {
	identity         localIdentity
	artifacts        *artifact.Store
	handshakeTimeout time.Duration
	requestTimeout   time.Duration
}

func NewClient(options ClientOptions) (*Client, error) {
	identity, err := prepareIdentity(options.Identity)
	if err != nil {
		return nil, err
	}
	handshakeTimeout, err := boundedTimeout(options.HandshakeTimeout, defaultHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	requestTimeout, err := boundedTimeout(options.RequestTimeout, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	return &Client{identity: identity, artifacts: options.Artifacts,
		handshakeTimeout: handshakeTimeout, requestTimeout: requestTimeout}, nil
}

// SendDelivery sends one signed delivery candidate and returns either a
// transport-only ACK or a signature-verified admission Receipt. The caller
// owns durable retry and must not settle an outbox entry from an ACK.
func (client *Client) SendDelivery(ctx context.Context, destination Peer,
	delivery agency.PeerDelivery,
) (DeliveryResponse, error) {
	if client == nil {
		return DeliveryResponse{}, fmt.Errorf("%w: client is required", ErrInput)
	}
	request, err := deliveryOfferFrame(delivery, client.identity.privateKey)
	if err != nil {
		return DeliveryResponse{}, err
	}
	peer, err := client.destination(destination)
	if err != nil {
		return DeliveryResponse{}, err
	}
	session, err := client.open(ctx, peer)
	if err != nil {
		return DeliveryResponse{}, err
	}
	defer session.close()
	if err := writeFrame(session.connection, request); err != nil {
		return DeliveryResponse{}, clientSessionError(session.ctx, err)
	}
	response, err := readFrame(session.connection)
	if err != nil {
		return DeliveryResponse{}, clientSessionError(session.ctx, err)
	}
	if response.deliveryID != delivery.ID() ||
		response.envelopeDigest != delivery.EnvelopeDigest() {
		return DeliveryResponse{}, fmt.Errorf("%w: response does not bind the delivery", ErrFrame)
	}
	switch response.frameType {
	case frameTransportACK:
		return DeliveryResponse{transportACK: true}, nil
	case frameAdmissionReceipt:
		receipt, parseErr := agency.ParsePeerAdmissionReceiptCanonicalJSON(
			response.canonical, delivery)
		if parseErr != nil || !ed25519.Verify(peer.PublicKey, receipt.SigningMessage(),
			response.signature) {
			return DeliveryResponse{}, fmt.Errorf("%w: invalid signed admission Receipt", ErrFrame)
		}
		return DeliveryResponse{receipt: receipt,
			signature: append([]byte(nil), response.signature...)}, nil
	default:
		return DeliveryResponse{}, fmt.Errorf("%w: unexpected delivery response", ErrFrame)
	}
}

// PullArtifact fetches one object only within an exact delivery scope,
// verifies its digest, and writes it to the caller-owned Artifact store.
func (client *Client) PullArtifact(ctx context.Context, destination Peer,
	delivery agency.PeerDelivery, digest agency.Digest,
) (artifact.PutResult, error) {
	if client == nil || client.artifacts == nil {
		return artifact.PutResult{}, fmt.Errorf("%w: client Artifact store is required", ErrInput)
	}
	request, err := artifactRequestFrame(delivery, digest)
	if err != nil {
		return artifact.PutResult{}, err
	}
	peer, err := client.destination(destination)
	if err != nil {
		return artifact.PutResult{}, err
	}
	session, err := client.open(ctx, peer)
	if err != nil {
		return artifact.PutResult{}, err
	}
	defer session.close()
	if err := writeFrame(session.connection, request); err != nil {
		return artifact.PutResult{}, clientSessionError(session.ctx, err)
	}
	response, err := readFrame(session.connection)
	if err != nil {
		return artifact.PutResult{}, clientSessionError(session.ctx, err)
	}
	if response.frameType != frameArtifactResponse || response.deliveryID != delivery.ID() ||
		response.envelopeDigest != delivery.EnvelopeDigest() || response.objectDigest != digest ||
		agency.Sum(response.body) != digest {
		return artifact.PutResult{}, fmt.Errorf("%w: Artifact response does not bind the request", ErrFrame)
	}
	result, err := client.artifacts.Put(session.ctx, digest, response.body)
	if err != nil {
		return artifact.PutResult{}, fmt.Errorf("store received Artifact: %w", err)
	}
	return result, nil
}

func (client *Client) destination(input Peer) (Peer, error) {
	peer, err := preparePeer(input, true)
	if err != nil {
		return Peer{}, err
	}
	if peer.ID == client.identity.id ||
		ed25519.PublicKey(peer.PublicKey).Equal(client.identity.publicKey) {
		return Peer{}, fmt.Errorf("%w: destination cannot be the local identity", ErrInput)
	}
	return peer, nil
}

type clientSession struct {
	connection *tls.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	stop       func() bool
}

func (client *Client) open(ctx context.Context, peer Peer) (*clientSession, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, fmt.Errorf("%w: live context is required", ErrInput)
	}
	handshakeContext, handshakeCancel := context.WithTimeout(ctx, client.handshakeTimeout)
	raw, err := (&net.Dialer{}).DialContext(handshakeContext, "tcp", peer.Address)
	if err != nil {
		contextErr := handshakeContext.Err()
		handshakeCancel()
		if contextErr != nil {
			return nil, contextErr
		}
		return nil, clientSessionError(ctx, err)
	}
	connection := tls.Client(raw, clientTLSConfig(client.identity, peer))
	stopHandshake := context.AfterFunc(handshakeContext, func() { _ = connection.Close() })
	if deadline, ok := handshakeContext.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			stopHandshake()
			handshakeCancel()
			_ = connection.Close()
			return nil, fmt.Errorf("%w: set handshake deadline: %v", ErrTransport, err)
		}
	}
	err = connection.HandshakeContext(handshakeContext)
	contextErr := handshakeContext.Err()
	stopHandshake()
	handshakeCancel()
	if err != nil {
		_ = connection.Close()
		if contextErr != nil {
			return nil, contextErr
		}
		return nil, clientSessionError(ctx, err)
	}
	requestContext, requestCancel := context.WithTimeout(ctx, client.requestTimeout)
	if deadline, ok := requestContext.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			requestCancel()
			_ = connection.Close()
			return nil, fmt.Errorf("%w: set request deadline: %v", ErrTransport, err)
		}
	}
	stop := context.AfterFunc(requestContext, func() { _ = connection.Close() })
	return &clientSession{connection: connection, ctx: requestContext,
		cancel: requestCancel, stop: stop}, nil
}

func (session *clientSession) close() {
	if session == nil {
		return
	}
	if session.stop != nil {
		session.stop()
	}
	if session.cancel != nil {
		session.cancel()
	}
	if session.connection != nil {
		_ = session.connection.Close()
	}
}

func clientSessionError(ctx context.Context, cause error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(cause, ErrFrame) || errors.Is(cause, ErrInput) ||
		errors.Is(cause, ErrAuthentication) {
		return cause
	}
	return fmt.Errorf("%w: %v", ErrTransport, cause)
}

func boundedTimeout(value, fallback time.Duration) (time.Duration, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 0 || value > maximumTimeout {
		return 0, fmt.Errorf("%w: timeout is outside the supported bound", ErrInput)
	}
	return value, nil
}
