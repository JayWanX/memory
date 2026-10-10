package peerlink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	artifactstore "github.com/JayWanX/memory/internal/agency/artifact"
)

func TestTCPRoundTripAuthenticatesDeliveryAndSeparatesACKReceiptAndArtifact(t *testing.T) {
	serverIdentity := testIdentity(t, "peer/server", 0x31)
	clientIdentity := testIdentity(t, "peer/client", 0x42)
	artifact := []byte("verified remote Artifact")
	delivery, route := testDelivery(t, "route:tcp-round-trip", artifact)
	serverCAS := testCAS(t, "server")
	if _, err := serverCAS.Put(context.Background(), agency.Sum(artifact), artifact); err != nil {
		t.Fatal(err)
	}
	clientCAS := testCAS(t, "client")

	var calls atomic.Int32
	receipt := testReceipt(t, delivery)
	server := testServer(t, serverIdentity, []Peer{testPeer(t, clientIdentity, "")}, serverCAS,
		func(_ context.Context, peer AuthenticatedPeer, offer DeliveryOffer) (DeliveryResponse, error) {
			if peer.ID() != clientIdentity.ID {
				t.Fatalf("authenticated Peer = %s, want %s", peer.ID().String(),
					clientIdentity.ID.String())
			}
			parsed, err := agency.ParsePeerDeliveryCanonicalJSON(offer.CanonicalDelivery(), route)
			if err != nil {
				return DeliveryResponse{}, err
			}
			if parsed.ID() != offer.DeliveryID() ||
				parsed.EnvelopeDigest() != offer.EnvelopeDigest() ||
				!ed25519.Verify(peer.PublicKey(), parsed.SigningMessage(), offer.Signature()) {
				return DeliveryResponse{}, errors.New("delivery signature did not bind authenticated Peer")
			}
			if calls.Add(1) == 1 {
				return NewTransportACK(), nil
			}
			return NewAdmissionResponse(receipt)
		},
		func(_ context.Context, peer AuthenticatedPeer, request ArtifactRequest) (bool, error) {
			return peer.ID() == clientIdentity.ID && request.DeliveryID() == delivery.ID() &&
				request.EnvelopeDigest() == delivery.EnvelopeDigest() &&
				request.ObjectDigest() == agency.Sum(artifact), nil
		})

	client, err := NewClient(ClientOptions{Identity: clientIdentity, Artifacts: clientCAS})
	if err != nil {
		t.Fatal(err)
	}
	destination := testPeer(t, serverIdentity, server.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ack, err := client.SendDelivery(ctx, destination, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.IsTransportACK() || len(ack.Signature()) != 0 {
		t.Fatal("first response was not a transport-only ACK")
	}
	if _, ok := ack.AdmissionReceipt(); ok {
		t.Fatal("transport ACK was exposed as admission evidence")
	}

	admitted, err := client.SendDelivery(ctx, destination, delivery)
	if err != nil {
		t.Fatal(err)
	}
	gotReceipt, ok := admitted.AdmissionReceipt()
	if !ok || admitted.IsTransportACK() || gotReceipt.Digest() != receipt.Digest() ||
		!ed25519.Verify(serverIdentity.PrivateKey.Public().(ed25519.PublicKey),
			gotReceipt.SigningMessage(), admitted.Signature()) {
		t.Fatal("second response was not the authenticated admission Receipt")
	}

	put, err := client.PullArtifact(ctx, destination, delivery, agency.Sum(artifact))
	if err != nil {
		t.Fatal(err)
	}
	if put.Digest != agency.Sum(artifact) || put.Size != int64(len(artifact)) {
		t.Fatalf("PullArtifact() = %#v", put)
	}
	stored, err := clientCAS.Read(ctx, agency.Sum(artifact), artifactstore.MaxObjectBytes)
	if err != nil || !bytes.Equal(stored, artifact) {
		t.Fatalf("client CAS bytes = %q, %v", stored, err)
	}
}

func TestTLSIdentityComesOnlyFromEnrolledKey(t *testing.T) {
	serverIdentity := testIdentity(t, "peer/auth-server", 0x51)
	victimIdentity := testIdentity(t, "peer/victim", 0x52)
	attackerIdentity := testIdentity(t, "peer/attacker", 0x53)
	delivery, _ := testDelivery(t, "route:identity", []byte("identity Artifact"))
	serverCAS := testCAS(t, "identity-server")
	seen := make(chan agency.OpaqueHandle, 1)
	var handlerCalls atomic.Int32
	server := testServer(t, serverIdentity,
		[]Peer{testPeer(t, victimIdentity, ""), testPeer(t, attackerIdentity, "")}, serverCAS,
		func(_ context.Context, peer AuthenticatedPeer, _ DeliveryOffer) (DeliveryResponse, error) {
			handlerCalls.Add(1)
			seen <- peer.ID()
			return NewTransportACK(), nil
		}, func(context.Context, AuthenticatedPeer, ArtifactRequest) (bool, error) {
			return false, nil
		})

	// The local label claims the victim ID, but the TLS key is the attacker's.
	// No ID is sent in a frame; the server must resolve the enrolled attacker.
	forgedIdentity := Identity{ID: victimIdentity.ID, PrivateKey: attackerIdentity.PrivateKey}
	client, err := NewClient(ClientOptions{Identity: forgedIdentity})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.SendDelivery(ctx,
		testPeer(t, serverIdentity, server.Addr().String()), delivery); err != nil {
		t.Fatal(err)
	}
	select {
	case authenticated := <-seen:
		if authenticated != attackerIdentity.ID {
			t.Fatalf("authenticated ID = %s, want key owner %s", authenticated.String(),
				attackerIdentity.ID.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls.Load())
	}
}

func TestTLSRejectsUnenrolledClientAndWrongServerPinBeforeHandler(t *testing.T) {
	serverIdentity := testIdentity(t, "peer/reject-server", 0x61)
	enrolledIdentity := testIdentity(t, "peer/enrolled", 0x62)
	unenrolledIdentity := testIdentity(t, "peer/unenrolled", 0x63)
	wrongServer := testIdentity(t, "peer/wrong-server", 0x64)
	delivery, _ := testDelivery(t, "route:authentication-rejection", []byte("auth Artifact"))
	var handlerCalls atomic.Int32
	server := testServer(t, serverIdentity, []Peer{testPeer(t, enrolledIdentity, "")},
		testCAS(t, "reject-server"),
		func(context.Context, AuthenticatedPeer, DeliveryOffer) (DeliveryResponse, error) {
			handlerCalls.Add(1)
			return NewTransportACK(), nil
		}, func(context.Context, AuthenticatedPeer, ArtifactRequest) (bool, error) {
			return false, nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unenrolledClient, err := NewClient(ClientOptions{Identity: unenrolledIdentity})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unenrolledClient.SendDelivery(ctx,
		testPeer(t, serverIdentity, server.Addr().String()), delivery); err == nil {
		t.Fatal("unenrolled client key unexpectedly reached the handler")
	}

	enrolledClient, err := NewClient(ClientOptions{Identity: enrolledIdentity})
	if err != nil {
		t.Fatal(err)
	}
	wrongDestination := testPeer(t, wrongServer, server.Addr().String())
	wrongDestination.ID = serverIdentity.ID
	if _, err := enrolledClient.SendDelivery(ctx, wrongDestination, delivery); err == nil {
		t.Fatal("wrong server public-key pin unexpectedly authenticated")
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
	}
}

func TestServerBoundsConcurrencyAndCloseWaitsForHandlers(t *testing.T) {
	serverIdentity := testIdentity(t, "peer/bounded-server", 0x71)
	clientIdentity := testIdentity(t, "peer/bounded-client", 0x72)
	delivery, _ := testDelivery(t, "route:bounded", []byte("bounded Artifact"))
	entered := make(chan struct{})
	exited := make(chan struct{})
	var calls atomic.Int32
	server := testServerWithOptions(t, "127.0.0.1:0", ServerOptions{
		Identity: serverIdentity, Peers: []Peer{testPeer(t, clientIdentity, "")},
		Artifacts: testCAS(t, "bounded-server"), MaxHandlers: 1,
		Delivery: func(ctx context.Context, _ AuthenticatedPeer,
			_ DeliveryOffer,
		) (DeliveryResponse, error) {
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-ctx.Done()
			close(exited)
			return DeliveryResponse{}, ctx.Err()
		},
		AuthorizeArtifact: func(context.Context, AuthenticatedPeer,
			ArtifactRequest,
		) (bool, error) {
			return false, nil
		},
	})
	client, err := NewClient(ClientOptions{Identity: clientIdentity,
		HandshakeTimeout: time.Second, RequestTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	destination := testPeer(t, serverIdentity, server.Addr().String())
	firstDone := make(chan error, 1)
	go func() {
		_, sendErr := client.SendDelivery(context.Background(), destination, delivery)
		firstDone <- sendErr
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first handler did not start")
	}

	secondContext, secondCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer secondCancel()
	if _, err := client.SendDelivery(secondContext, destination, delivery); err == nil {
		t.Fatal("second concurrent connection exceeded the handler budget")
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}

	closeContext, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()
	if err := server.CloseContext(closeContext); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("handler was not canceled before shutdown completed")
	}
	select {
	case err := <-firstDone:
		if err == nil {
			t.Fatal("closed in-flight request unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("client did not observe connection shutdown")
	}
	if err := server.CloseContext(closeContext); err != nil {
		t.Fatalf("repeated CloseContext() error = %v", err)
	}
}

func testCAS(t *testing.T, name string) *artifactstore.Store {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := artifactstore.Open(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testServer(t *testing.T, identity Identity, peers []Peer, store *artifactstore.Store,
	delivery DeliveryHandler, authorize ArtifactAuthorizer,
) *Server {
	t.Helper()
	return testServerWithOptions(t, "127.0.0.1:0", ServerOptions{Identity: identity,
		Peers: peers, Artifacts: store, Delivery: delivery, AuthorizeArtifact: authorize})
}

func testServerWithOptions(t *testing.T, address string, options ServerOptions) *Server {
	t.Helper()
	lifetime, cancel := context.WithCancel(context.Background())
	server, err := Listen(lifetime, address, options)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := server.CloseContext(ctx); err != nil {
			t.Errorf("CloseContext() cleanup error = %v", err)
		}
	})
	return server
}
