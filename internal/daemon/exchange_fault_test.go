package daemon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/authority"
	"github.com/JayWanX/memory/internal/agency/peerlink"
)

var (
	errInjectedPeerAdmission = errors.New("injected Peer admission failure")
	errInjectedPeerCatalog   = errors.New("injected Peer catalog failure")
)

type expireBeforeSettleAuthority struct {
	exchangeAuthority
	store   *authority.Store
	routeID agency.RouteID
	result  chan error
	once    sync.Once
}

func (fault *expireBeforeSettleAuthority) SettlePeerDelivery(ctx context.Context,
	remote agency.OpaqueHandle, delivery agency.DeliveryID, canonical, signature []byte,
) (agency.PeerAdmissionReceipt, bool, error) {
	var revokeErr error
	fault.once.Do(func() {
		_, revokeErr = fault.store.RevokePeerRoute(ctx, fault.routeID)
	})
	if revokeErr != nil {
		return agency.PeerAdmissionReceipt{}, false, revokeErr
	}
	receipt, replayed, err := fault.exchangeAuthority.SettlePeerDelivery(ctx, remote,
		delivery, canonical, signature)
	select {
	case fault.result <- err:
	default:
	}
	return receipt, replayed, err
}

type failAdmitAuthority struct {
	exchangeAuthority
	called chan struct{}
	once   sync.Once
}

func (fault *failAdmitAuthority) AdmitPeerDelivery(context.Context,
	agency.DeliveryID,
) (authority.PeerAdmissionResult, error) {
	fault.once.Do(func() { close(fault.called) })
	return authority.PeerAdmissionResult{}, errInjectedPeerAdmission
}

type failCatalogAuthority struct {
	exchangeAuthority
	called chan struct{}
	once   sync.Once
}

func (fault *failCatalogAuthority) CatalogArtifact(context.Context,
	authority.VerifiedArtifact,
) error {
	fault.once.Do(func() { close(fault.called) })
	return errInjectedPeerCatalog
}

func TestDeliveryExpiryDuringSettlementDoesNotStopDaemon(t *testing.T) {
	origin, receiver := provisionExchangePair(t)
	origin.runtime = openExchangeNode(t, origin)
	receiver.runtime = openExchangeNode(t, receiver)
	fault := &expireBeforeSettleAuthority{exchangeAuthority: origin.runtime.store,
		store: origin.runtime.store, routeID: mustRouteID(t, "route:test-pair"),
		result: make(chan error, 1)}
	origin.runtime.exchange.store = fault
	emitRemoteHandling(t, origin.runtime, "peer-b", nil)

	receiverErrors := serveExchangeNode(t, receiver)
	originErrors := serveExchangeNode(t, origin)
	select {
	case err := <-fault.result:
		if !errors.Is(err, authority.ErrPeerDeliveryExpired) {
			t.Fatalf("settlement race = %v, want ErrPeerDeliveryExpired", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outbox never reached the settlement race")
	}

	origin.runtime.mu.Lock()
	session := origin.runtime.exchangeSession
	origin.runtime.mu.Unlock()
	select {
	case <-session.Done():
		t.Fatalf("item-local settlement expiry stopped exchange: %v", session.Err())
	case <-time.After(5 * minimumExchangeInterval):
	}
	assertDaemonReady(t, origin)
	stopExchangeNode(t, origin.runtime, originErrors)
	stopExchangeNode(t, receiver.runtime, receiverErrors)
}

func TestReceiverInternalFaultNeverBecomesTransportACK(t *testing.T) {
	t.Run("admission", func(t *testing.T) {
		origin, receiver := provisionExchangePair(t)
		origin.runtime = openExchangeNode(t, origin)
		receiver.runtime = openExchangeNode(t, receiver)
		pending := emitRemoteHandling(t, origin.runtime, "peer-b", nil)
		fault := &failAdmitAuthority{exchangeAuthority: receiver.runtime.store,
			called: make(chan struct{})}
		receiver.runtime.exchange.store = fault
		receiverErrors := serveExchangeNode(t, receiver)

		response, err := sendPendingOnce(t, origin.runtime, pending)
		if err == nil || response.IsTransportACK() {
			t.Fatalf("internal admission failure became ACK: (%v, %v)", response, err)
		}
		waitForFaultCall(t, fault.called)
		assertPendingDeliveryCount(t, origin.runtime, 1)
		assertDaemonReady(t, receiver)
		closeUnservedRuntime(t, origin.runtime)
		stopExchangeNode(t, receiver.runtime, receiverErrors)
	})

	t.Run("local catalog", func(t *testing.T) {
		origin, receiver := provisionExchangePair(t)
		origin.runtime = openExchangeNode(t, origin)
		receiver.runtime = openExchangeNode(t, receiver)
		content := []byte("already transferred Artifact")
		pending := emitRemoteHandling(t, origin.runtime, "peer-b", content)
		digests := pending.Delivery().Artifacts()
		if len(digests) != 1 {
			t.Fatalf("delivery Artifact count = %d", len(digests))
		}
		if _, err := receiver.runtime.exchange.objects.Put(context.Background(), digests[0], content); err != nil {
			t.Fatal(err)
		}
		delivery := pending.Delivery()
		signature := ed25519.Sign(origin.runtime.exchange.identity.PrivateKey,
			delivery.SigningMessage())
		staged, err := receiver.runtime.store.StagePeerDelivery(context.Background(),
			origin.identity.PeerID(), delivery.CanonicalJSON(), signature)
		if err != nil || staged.State() != authority.PeerAdmissionStateStaged {
			t.Fatalf("pre-stage catalog fault delivery = (%v, %v)", staged.State(), err)
		}
		fault := &failCatalogAuthority{exchangeAuthority: receiver.runtime.store,
			called: make(chan struct{})}
		receiver.runtime.exchange.store = fault
		receiverErrors := serveExchangeNode(t, receiver)

		response, err := sendPendingOnce(t, origin.runtime, pending)
		if err == nil || response.IsTransportACK() {
			t.Fatalf("local catalog failure became ACK: (%v, %v)", response, err)
		}
		waitForFaultCall(t, fault.called)
		assertPendingDeliveryCount(t, origin.runtime, 1)
		assertDaemonReady(t, receiver)
		closeUnservedRuntime(t, origin.runtime)
		stopExchangeNode(t, receiver.runtime, receiverErrors)
	})
}

func sendPendingOnce(t *testing.T, runtime *Runtime,
	pending authority.PendingPeerDelivery,
) (peerlink.DeliveryResponse, error) {
	t.Helper()
	destination, err := runtime.exchange.peerForRoute(pending.Route())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return runtime.exchange.client.SendDelivery(ctx, destination, pending.Delivery())
}

func waitForFaultCall(t *testing.T, called <-chan struct{}) {
	t.Helper()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver fault was not exercised")
	}
}

func assertDaemonReady(t *testing.T, node *exchangeTestNode) {
	t.Helper()
	client := unixHTTPClient(filepath.Join(node.state, controlSocketName))
	status := controlRequest(t, client, http.MethodGet, routeStatus, nil, nil, http.StatusOK)
	if string(status) != `{"schema":"memory.agency.status","status":"ready","version":1}` {
		t.Fatalf("daemon status = %s", status)
	}
}

func closeUnservedRuntime(t *testing.T, runtime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
