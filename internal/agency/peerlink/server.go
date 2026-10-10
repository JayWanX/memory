package peerlink

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/JayWanX/memory/internal/agency/artifact"
)

// DeliveryHandler receives opaque candidate bytes only after the TLS key has
// been authenticated and mapped to an owner-enrolled Peer. The handler owns
// route-bound parsing, delivery-signature verification, staging, admission,
// and Receipt construction.
type DeliveryHandler func(context.Context, AuthenticatedPeer,
	DeliveryOffer,
) (DeliveryResponse, error)

// ArtifactAuthorizer decides whether the exact authenticated Peer and
// delivery envelope may read one digest. There is deliberately no bare-digest
// object callback.
type ArtifactAuthorizer func(context.Context, AuthenticatedPeer, ArtifactRequest) (bool, error)

type ServerOptions struct {
	Identity          Identity
	Peers             []Peer
	Artifacts         *artifact.Store
	Delivery          DeliveryHandler
	AuthorizeArtifact ArtifactAuthorizer
	MaxHandlers       int
	HandshakeTimeout  time.Duration
	RequestTimeout    time.Duration
}

// Server is a bounded owner of one TCP listener and its connection handlers.
// It has no delivery queue, route selection, retry, admission, or settlement.
type Server struct {
	identity          localIdentity
	artifacts         *artifact.Store
	delivery          DeliveryHandler
	authorizeArtifact ArtifactAuthorizer
	tlsConfig         *tls.Config
	listener          net.Listener
	handshakeTimeout  time.Duration
	requestTimeout    time.Duration
	budget            chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	stopParent        func() bool
	closeOnce         sync.Once
	done              chan struct{}
	handlers          sync.WaitGroup

	mu        sync.Mutex
	active    map[net.Conn]struct{}
	acceptErr error
}

// Listen starts a real TCP/TLS peer endpoint. A canceled lifetime closes the
// listener, cancels every callback, closes every active connection, and waits
// through Server.Wait or Server.CloseContext.
func Listen(lifetime context.Context, address string, options ServerOptions) (*Server, error) {
	if lifetime == nil || lifetime.Err() != nil || !validAddress(address) ||
		options.Artifacts == nil || options.Delivery == nil || options.AuthorizeArtifact == nil {
		return nil, fmt.Errorf("%w: live context, address, Artifact store, and handlers are required", ErrInput)
	}
	identity, err := prepareIdentity(options.Identity)
	if err != nil {
		return nil, err
	}
	pins, err := peerPins(identity, options.Peers)
	if err != nil {
		return nil, err
	}
	maxHandlers, err := boundedHandlers(options.MaxHandlers)
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
	listener, err := (&net.ListenConfig{}).Listen(lifetime, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("%w: listen: %v", ErrTransport, err)
	}
	ownedContext, cancel := context.WithCancel(lifetime)
	server := &Server{identity: identity, artifacts: options.Artifacts, delivery: options.Delivery,
		authorizeArtifact: options.AuthorizeArtifact, tlsConfig: serverTLSConfig(identity, pins),
		listener: listener, handshakeTimeout: handshakeTimeout, requestTimeout: requestTimeout,
		budget: make(chan struct{}, maxHandlers), ctx: ownedContext, cancel: cancel,
		done: make(chan struct{}), active: make(map[net.Conn]struct{})}
	stopParent := context.AfterFunc(lifetime, server.initiateClose)
	server.mu.Lock()
	server.stopParent = stopParent
	closing := server.ctx.Err() != nil
	server.mu.Unlock()
	if closing {
		stopParent()
	}
	go server.run(pins)
	return server, nil
}

func (server *Server) Addr() net.Addr {
	if server == nil || server.listener == nil {
		return nil
	}
	return server.listener.Addr()
}

// Close initiates shutdown and waits for bounded handler completion.
func (server *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	return server.CloseContext(ctx)
}

// CloseContext initiates shutdown and waits until all owned goroutines exit or
// the caller's context ends.
func (server *Server) CloseContext(ctx context.Context) error {
	if server == nil || ctx == nil {
		return fmt.Errorf("%w: server and context are required", ErrInput)
	}
	server.initiateClose()
	return server.Wait(ctx)
}

// Wait joins the accept loop and every connection handler without initiating
// shutdown. An unexpected listener failure is returned after all handlers end.
func (server *Server) Wait(ctx context.Context) error {
	if server == nil || ctx == nil {
		return fmt.Errorf("%w: server and context are required", ErrInput)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-server.done:
		server.mu.Lock()
		err := server.acceptErr
		server.mu.Unlock()
		return err
	}
}

func (server *Server) run(pins map[publicKeyPin]Peer) {
	defer close(server.done)
	for {
		raw, err := server.listener.Accept()
		if err != nil {
			if server.ctx.Err() == nil {
				server.mu.Lock()
				server.acceptErr = fmt.Errorf("%w: accept: %v", ErrTransport, err)
				server.mu.Unlock()
				server.initiateClose()
			}
			break
		}
		select {
		case server.budget <- struct{}{}:
		default:
			_ = raw.Close()
			continue
		}
		if !server.track(raw) {
			<-server.budget
			_ = raw.Close()
			continue
		}
		server.handlers.Add(1)
		go server.handle(raw, pins)
	}
	server.handlers.Wait()
}

func (server *Server) handle(raw net.Conn, pins map[publicKeyPin]Peer) {
	defer server.handlers.Done()
	defer func() { <-server.budget }()
	defer server.untrack(raw)
	defer func() { _ = raw.Close() }()

	handshakeContext, handshakeCancel := context.WithTimeout(server.ctx, server.handshakeTimeout)
	connection := tls.Server(raw, server.tlsConfig)
	stopHandshake := context.AfterFunc(handshakeContext, func() { _ = connection.Close() })
	if deadline, ok := handshakeContext.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			stopHandshake()
			handshakeCancel()
			return
		}
	}
	err := connection.HandshakeContext(handshakeContext)
	stopHandshake()
	handshakeCancel()
	if err != nil {
		return
	}
	peer, err := authenticateConnection(connection.ConnectionState(), pins, time.Now().UTC())
	if err != nil {
		return
	}
	requestContext, requestCancel := context.WithTimeout(server.ctx, server.requestTimeout)
	defer requestCancel()
	stop := context.AfterFunc(requestContext, func() { _ = connection.Close() })
	defer stop()
	if deadline, ok := requestContext.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return
		}
	}
	request, err := readFrame(connection)
	if err != nil {
		return
	}
	response, err := server.respond(requestContext, peer, request)
	if err != nil {
		return
	}
	_ = writeFrame(connection, response)
}

func (server *Server) respond(ctx context.Context, peer AuthenticatedPeer,
	request frame,
) (frame, error) {
	switch request.frameType {
	case frameDeliveryOffer:
		offer := DeliveryOffer{deliveryID: request.deliveryID,
			envelopeDigest: request.envelopeDigest,
			canonical:      append([]byte(nil), request.canonical...),
			signature:      append([]byte(nil), request.signature...)}
		response, err := server.delivery(ctx, peer, offer)
		if err != nil {
			return frame{}, fmt.Errorf("delivery callback: %w", err)
		}
		return deliveryResponseFrame(offer, response, server.identity.privateKey)
	case frameArtifactRequest:
		artifactRequest := ArtifactRequest{deliveryID: request.deliveryID,
			envelopeDigest: request.envelopeDigest, objectDigest: request.objectDigest}
		authorized, err := server.authorizeArtifact(ctx, peer, artifactRequest)
		if err != nil {
			return frame{}, fmt.Errorf("Artifact authorization: %w", err)
		}
		if !authorized {
			return frame{}, fmt.Errorf("%w: Artifact scope was not authorized", ErrAuthentication)
		}
		body, err := server.artifacts.Read(ctx, artifactRequest.objectDigest, artifact.MaxObjectBytes)
		if err != nil {
			return frame{}, fmt.Errorf("read Artifact: %w", err)
		}
		return artifactResponseFrame(artifactRequest, body)
	default:
		return frame{}, fmt.Errorf("%w: first frame is not a request", ErrFrame)
	}
}

func (server *Server) initiateClose() {
	if server == nil {
		return
	}
	server.closeOnce.Do(func() {
		server.cancel()
		_ = server.listener.Close()
		server.mu.Lock()
		stopParent := server.stopParent
		connections := make([]net.Conn, 0, len(server.active))
		for connection := range server.active {
			connections = append(connections, connection)
		}
		server.mu.Unlock()
		if stopParent != nil {
			stopParent()
		}
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
}

func (server *Server) track(connection net.Conn) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.ctx.Err() != nil {
		return false
	}
	server.active[connection] = struct{}{}
	return true
}

func (server *Server) untrack(connection net.Conn) {
	server.mu.Lock()
	delete(server.active, connection)
	server.mu.Unlock()
}

func boundedHandlers(value int) (int, error) {
	if value == 0 {
		return defaultMaxHandlers, nil
	}
	if value < 0 || value > maximumHandlers {
		return 0, fmt.Errorf("%w: handler concurrency is outside the supported bound", ErrInput)
	}
	return value, nil
}
