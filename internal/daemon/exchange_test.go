package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/authority"
)

type exchangeTestNode struct {
	state     string
	principal agency.AgentPrincipalID
	identity  TransportIdentity
	address   string
	options   ExchangeOptions
	runtime   *Runtime
}

func TestTwoDaemonExchangePullsArtifactAndACKDoesNotSettle(t *testing.T) {
	origin, receiver := provisionExchangePair(t)
	origin.runtime = openExchangeNode(t, origin)
	receiver.runtime = openExchangeNode(t, receiver)
	receiverErrors := serveExchangeNode(t, receiver)

	pending := emitRemoteHandling(t, origin.runtime, "peer-b", []byte("bounded peer evidence"))
	destination, err := origin.runtime.exchange.peerForRoute(pending.Route())
	if err != nil {
		t.Fatal(err)
	}
	response, err := origin.runtime.exchange.client.SendDelivery(context.Background(),
		destination, pending.Delivery())
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsTransportACK() {
		t.Fatal("missing receiver Artifact did not produce a transport-only ACK")
	}
	assertPendingDeliveryCount(t, origin.runtime, 1)

	originErrors := serveExchangeNode(t, origin)
	waitForSettledOutbox(t, origin.runtime)
	assertRemoteHandlingVisible(t, receiver.runtime, "bounded peer evidence")
	stopExchangeNode(t, origin.runtime, originErrors)
	stopExchangeNode(t, receiver.runtime, receiverErrors)
}

func TestTwoDaemonExchangeReplaysReceiptAfterReceiverRestart(t *testing.T) {
	origin, receiver := provisionExchangePair(t)
	origin.runtime = openExchangeNode(t, origin)
	receiver.runtime = openExchangeNode(t, receiver)
	receiverErrors := serveExchangeNode(t, receiver)

	pending := emitRemoteHandling(t, origin.runtime, "peer-b", nil)
	destination, err := origin.runtime.exchange.peerForRoute(pending.Route())
	if err != nil {
		t.Fatal(err)
	}
	response, err := origin.runtime.exchange.client.SendDelivery(context.Background(),
		destination, pending.Delivery())
	if err != nil {
		t.Fatal(err)
	}
	if _, accepted := response.AdmissionReceipt(); !accepted {
		t.Fatal("receiver did not return its signed admission Receipt")
	}
	// Simulate response loss: do not settle the origin outbox.
	assertPendingDeliveryCount(t, origin.runtime, 1)
	stopExchangeNode(t, receiver.runtime, receiverErrors)

	receiver.runtime = openExchangeNode(t, receiver)
	receiverErrors = serveExchangeNode(t, receiver)
	originErrors := serveExchangeNode(t, origin)
	waitForSettledOutbox(t, origin.runtime)
	assertRemoteHandlingVisible(t, receiver.runtime, "remote request")
	stopExchangeNode(t, origin.runtime, originErrors)
	stopExchangeNode(t, receiver.runtime, receiverErrors)
}

func TestLocalOnlyDaemonHasNoPeerIdentityOrReachabilityDependency(t *testing.T) {
	state, principal := provisionDaemonState(t)
	_, remotePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	remotePublic := remotePrivate.Public().(ed25519.PublicKey)
	remoteID, err := derivePeerIdentity(remotePublic)
	if err != nil {
		t.Fatal(err)
	}
	enrollRoute(t, state, authority.PeerRouteSpec{
		RouteID: mustRouteID(t, "route:local-only"), PublicAlias: mustHandle(t, "peer-offline"),
		RemotePeerID: remoteID, RemotePublicKey: remotePublic,
		TransportAddress: "127.0.0.1:1", RemoteTargetAlias: mustHandle(t, "target:offline"),
		InboundTargetAlias: mustHandle(t, "target:local"), LocalTargetPrincipal: principal,
	})

	runtime, err := Open(context.Background(), state, principal)
	if err != nil {
		t.Fatal(err)
	}
	errorsChannel := serveRuntimeForTest(t, runtime, state)
	stopExchangeNode(t, runtime, errorsChannel)
	if _, err := OpenWithExchange(context.Background(), state, principal,
		ExchangeOptions{ListenAddress: freeTCPAddress(t)}); err == nil {
		t.Fatal("exchange opened without a provisioned transport identity")
	}
}

func TestExchangeRejectsPeerIdentityNotDerivedFromRouteKey(t *testing.T) {
	state, principal := provisionDaemonState(t)
	if _, err := ProvisionTransportIdentity(state); err != nil {
		t.Fatal(err)
	}
	_, remotePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongID := mustHandle(t, "peer:not-derived")
	enrollRoute(t, state, authority.PeerRouteSpec{
		RouteID: mustRouteID(t, "route:wrong-key"), PublicAlias: mustHandle(t, "peer-wrong"),
		RemotePeerID: wrongID, RemotePublicKey: remotePrivate.Public().(ed25519.PublicKey),
		TransportAddress: "127.0.0.1:1", RemoteTargetAlias: mustHandle(t, "target:wrong"),
		InboundTargetAlias: mustHandle(t, "target:local"), LocalTargetPrincipal: principal,
	})
	if runtime, err := OpenWithExchange(context.Background(), state, principal,
		ExchangeOptions{ListenAddress: freeTCPAddress(t)}); err == nil || runtime != nil {
		t.Fatalf("OpenWithExchange accepted non-derived Peer identity: (%v, %v)", runtime, err)
	}
}

func TestExchangeShutdownCancelsBlockedOutboxAttempt(t *testing.T) {
	remoteListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		connection, acceptErr := remoteListener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	t.Cleanup(func() {
		_ = remoteListener.Close()
		select {
		case connection := <-accepted:
			_ = connection.Close()
		default:
		}
		<-acceptDone
	})

	state, principal := provisionDaemonState(t)
	if _, err := ProvisionTransportIdentity(state); err != nil {
		t.Fatal(err)
	}
	_, remotePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	remotePublic := remotePrivate.Public().(ed25519.PublicKey)
	remoteID, err := derivePeerIdentity(remotePublic)
	if err != nil {
		t.Fatal(err)
	}
	enrollRoute(t, state, authority.PeerRouteSpec{
		RouteID: mustRouteID(t, "route:blocked"), PublicAlias: mustHandle(t, "peer-blocked"),
		RemotePeerID: remoteID, RemotePublicKey: remotePublic,
		TransportAddress:   remoteListener.Addr().String(),
		RemoteTargetAlias:  mustHandle(t, "target:blocked"),
		InboundTargetAlias: mustHandle(t, "target:local"), LocalTargetPrincipal: principal,
	})
	options := ExchangeOptions{ListenAddress: freeTCPAddress(t),
		PollInterval: minimumExchangeInterval, BatchSize: 1}
	runtime, err := OpenWithExchange(context.Background(), state, principal, options)
	if err != nil {
		t.Fatal(err)
	}
	emitRemoteHandling(t, runtime, "peer-blocked", nil)
	errorsChannel := serveRuntimeForTest(t, runtime, state)
	var blocked net.Conn
	select {
	case blocked = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("outbox worker did not enter the blocked TLS handshake")
	}
	defer blocked.Close()
	started := time.Now()
	stopExchangeNode(t, runtime, errorsChannel)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bounded shutdown took %s", elapsed)
	}
}

func provisionExchangePair(t *testing.T) (*exchangeTestNode, *exchangeTestNode) {
	t.Helper()
	originState, originPrincipal := provisionDaemonState(t)
	receiverState, receiverPrincipal := provisionDaemonState(t)
	originIdentity, err := ProvisionTransportIdentity(originState)
	if err != nil {
		t.Fatal(err)
	}
	receiverIdentity, err := ProvisionTransportIdentity(receiverState)
	if err != nil {
		t.Fatal(err)
	}
	origin := &exchangeTestNode{state: originState, principal: originPrincipal,
		identity: originIdentity, address: freeTCPAddress(t)}
	receiver := &exchangeTestNode{state: receiverState, principal: receiverPrincipal,
		identity: receiverIdentity, address: freeTCPAddress(t)}
	origin.options = ExchangeOptions{ListenAddress: origin.address,
		PollInterval: minimumExchangeInterval, BatchSize: 2}
	receiver.options = ExchangeOptions{ListenAddress: receiver.address,
		PollInterval: minimumExchangeInterval, BatchSize: 2}
	routeID := mustRouteID(t, "route:test-pair")
	enrollRoute(t, origin.state, authority.PeerRouteSpec{
		RouteID: routeID, PublicAlias: mustHandle(t, "peer-b"),
		RemotePeerID: receiver.identity.PeerID(), RemotePublicKey: receiver.identity.PublicKey(),
		TransportAddress: receiver.address, RemoteTargetAlias: mustHandle(t, "target:b"),
		InboundTargetAlias: mustHandle(t, "target:a"), LocalTargetPrincipal: origin.principal,
	})
	enrollRoute(t, receiver.state, authority.PeerRouteSpec{
		RouteID: routeID, PublicAlias: mustHandle(t, "peer-a"),
		RemotePeerID: origin.identity.PeerID(), RemotePublicKey: origin.identity.PublicKey(),
		TransportAddress: origin.address, RemoteTargetAlias: mustHandle(t, "target:a"),
		InboundTargetAlias: mustHandle(t, "target:b"), LocalTargetPrincipal: receiver.principal,
	})
	return origin, receiver
}

func openExchangeNode(t *testing.T, node *exchangeTestNode) *Runtime {
	t.Helper()
	runtime, err := OpenWithExchange(context.Background(), node.state, node.principal, node.options)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func enrollRoute(t *testing.T, state string, spec authority.PeerRouteSpec) {
	t.Helper()
	store, err := authority.Open(context.Background(), filepath.Join(state, authorityFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnrollPeerRoute(context.Background(), spec); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func emitRemoteHandling(t *testing.T, runtime *Runtime, peerAlias string,
	artifactContent []byte,
) authority.PendingPeerDelivery {
	t.Helper()
	attached, err := runtime.service.attach(context.Background(),
		agency.Sum([]byte("emit-remote-handling")))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := authority.NewAttachmentProof(attached.id, attached.credential)
	if err != nil {
		t.Fatal(err)
	}
	currentKey := mustOperationKey(t, "operation:peer-current")
	current, err := authority.NewCurrentOperation(currentKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.service.current(context.Background(), proof, current); err != nil {
		t.Fatal(err)
	}
	alias, err := agency.AliasTarget(mustHandle(t, peerAlias))
	if err != nil {
		t.Fatal(err)
	}
	kind, err := agency.NewSemanticLabel("work.request")
	if err != nil {
		t.Fatal(err)
	}
	payloadValue := "remote request"
	if artifactContent != nil {
		payloadValue = string(artifactContent)
	}
	payload, err := agency.NewSemanticPayload(payloadValue)
	if err != nil {
		t.Fatal(err)
	}
	spec := agency.IntentSpec{Kind: kind, Payload: payload,
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget(), alias}}
	var bindings []candidateBinding
	if artifactContent != nil {
		captured, err := runtime.service.capture(context.Background(), artifactContent)
		if err != nil {
			t.Fatal(err)
		}
		input, err := agency.NewArtifactCandidate(captured.handle)
		if err != nil {
			t.Fatal(err)
		}
		spec.Artifacts = []agency.ArtifactInput{input}
		bindings = []candidateBinding{{handle: captured.handle, digest: captured.digest}}
	}
	intent, err := agency.NewAgentIntent(spec)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.service.submit(context.Background(), proof, current,
		mustOperationKey(t, "operation:peer-submit"), intent, bindings)
	if err != nil || receipt.Outcome() != agency.ReceiptOutcomeAccepted {
		t.Fatalf("remote root admission = (%v, %v)", receipt.Outcome(), err)
	}
	pending, err := runtime.store.PendingPeerDeliveries(context.Background(), 2)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending delivery = (%d, %v)", len(pending), err)
	}
	return pending[0]
}

func serveExchangeNode(t *testing.T, node *exchangeTestNode) <-chan error {
	t.Helper()
	return serveRuntimeForTest(t, node.runtime, node.state)
}

func serveRuntimeForTest(t *testing.T, runtime *Runtime, state string) <-chan error {
	t.Helper()
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- runtime.Serve(context.Background()) }()
	waitForSocket(t, filepath.Join(state, controlSocketName))
	return errorsChannel
}

func stopExchangeNode(t *testing.T, runtime *Runtime, serveErrors <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveErrors:
		if err != nil {
			t.Fatalf("Serve() = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Serve did not join bounded Close")
	}
}

func waitForSettledOutbox(t *testing.T, runtime *Runtime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := runtime.store.PendingPeerDeliveries(context.Background(), 2)
		if err == nil && len(pending) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("origin outbox did not settle from signed remote Receipt")
}

func assertPendingDeliveryCount(t *testing.T, runtime *Runtime, expected int) {
	t.Helper()
	pending, err := runtime.store.PendingPeerDeliveries(context.Background(), 2)
	if err != nil || len(pending) != expected {
		t.Fatalf("pending deliveries = (%d, %v), want %d", len(pending), err, expected)
	}
}

func assertRemoteHandlingVisible(t *testing.T, runtime *Runtime, payload string) {
	t.Helper()
	attached, err := runtime.service.attach(context.Background(),
		agency.Sum([]byte("remote-handling-visible")))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := authority.NewAttachmentProof(attached.id, attached.credential)
	if err != nil {
		t.Fatal(err)
	}
	current, err := authority.NewCurrentOperation(mustOperationKey(t, "operation:receiver-current"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := runtime.service.current(context.Background(), proof, current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(view.CanonicalJSON(), []byte(payload)) {
		t.Fatalf("receiver View has no re-admitted payload %q: %s", payload, view.CanonicalJSON())
	}
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func mustHandle(t *testing.T, value string) agency.OpaqueHandle {
	t.Helper()
	handle, err := agency.NewOpaqueHandle(value)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func mustRouteID(t *testing.T, value string) agency.RouteID {
	t.Helper()
	route, err := agency.NewRouteID(value)
	if err != nil {
		t.Fatal(err)
	}
	return route
}

func mustOperationKey(t *testing.T, value string) agency.OperationKey {
	t.Helper()
	operation, err := agency.NewOperationKey(value)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}
