package authority

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestFederatedObservationsShareCorrelationAndRemainBoundedCandidates(t *testing.T) {
	origin := newAuthorityFixture(t, "principal:focus-federated-origin")
	originPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	peers := newFocusFederatedPeers(t, origin, originPrivate)

	publishTestReference(t, origin, "local.guidance", "locally admitted guidance")
	wantReference := focusReferenceSnapshot(t, origin, "local.guidance")
	exchanges := requireFederatedFocusFanout(t, origin, peers)
	roundTripFederatedFocusObservations(t, origin, originPrivate, exchanges, wantReference)
	requireFederatedFocusRotation(t, origin, peers)
	requireFocusReferenceUnchanged(t, origin, wantReference)
}

func newFocusFederatedPeers(t *testing.T, origin *authorityFixture,
	originPrivate ed25519.PrivateKey,
) []focusFederatedPeer {
	t.Helper()
	peers := make([]focusFederatedPeer, 0, 3)
	for index, suffix := range []string{"alpha", "beta", "gamma"} {
		peers = append(peers, newFocusFederatedPeer(t, origin, originPrivate, index, suffix))
	}
	return peers
}

type focusFederatedExchange struct {
	peer     *focusFederatedPeer
	delivery agency.PeerDelivery
}

func requireFederatedFocusFanout(t *testing.T, origin *authorityFixture,
	peers []focusFederatedPeer,
) []focusFederatedExchange {
	t.Helper()
	admitFederatedFocusRequest(t, origin, peers)
	if got := countRows(t, origin.store, "peer_outbox"); got != len(peers) {
		t.Fatalf("fan-out outbox rows = %d, want %d", got, len(peers))
	}

	pending, err := origin.store.PendingPeerDeliveries(origin.ctx, MaxPendingPeerDeliveries)
	if err != nil || len(pending) != len(peers) {
		t.Fatalf("fan-out deliveries = %#v, %v", pending, err)
	}
	byRoute := make(map[string]*focusFederatedPeer, len(peers))
	for index := range peers {
		byRoute[peers[index].originRoute.RouteID.String()] = &peers[index]
	}
	exchanges := make([]focusFederatedExchange, 0, len(pending))
	var request agency.EventRef
	for index, item := range pending {
		peer := byRoute[item.Route().RouteID().String()]
		if peer == nil {
			t.Fatalf("delivery used unknown route %s", item.Route().RouteID().String())
		}
		delivery := item.Delivery()
		if index == 0 {
			request = delivery.OriginEvent()
		} else if delivery.OriginEvent() != request {
			t.Fatalf("fan-out origin Event = %v, want %v", delivery.OriginEvent(), request)
		}
		exchanges = append(exchanges, focusFederatedExchange{peer: peer, delivery: delivery})
	}
	return exchanges
}

func roundTripFederatedFocusObservations(t *testing.T, origin *authorityFixture,
	originPrivate ed25519.PrivateKey, exchanges []focusFederatedExchange,
	wantReference focusReferenceState,
) {
	t.Helper()
	for _, exchange := range exchanges {
		admitFederatedFocusRequestAtPeer(t, originPrivate, exchange.peer, exchange.delivery)
		admitFederatedFocusObservation(t, origin, exchange.peer, exchange.delivery)
		requireFocusReferenceUnchanged(t, origin, wantReference)
	}
}

func requireFederatedFocusRotation(t *testing.T, origin *authorityFixture,
	peers []focusFederatedPeer,
) {
	t.Helper()
	wantPayloads := map[string]bool{"consider independent observations": false}
	for _, peer := range peers {
		wantPayloads[peer.observation] = false
	}
	for turn := 0; turn < len(wantPayloads); turn++ {
		view := decodeFocusView(t, origin.current(t))
		requireFederatedFocusView(t, view, wantPayloads, len(peers))
		if turn+1 < len(wantPayloads) {
			replaceInteractiveBoundary(t, origin)
		}
	}
	for payload, seen := range wantPayloads {
		if !seen {
			t.Fatalf("correlated candidate never entered Current: %q", payload)
		}
	}
}

func requireFederatedFocusView(t *testing.T, view focusViewWire,
	wantPayloads map[string]bool, peerCount int,
) {
	t.Helper()
	if view.Current == nil {
		t.Fatal("federated focus View has no Current")
	}
	payload := view.Current.Semantic.Payload
	seen, exists := wantPayloads[payload]
	if !exists {
		t.Fatalf("unexpected Current payload %q", payload)
	}
	if seen {
		t.Fatalf("Current payload %q was selected twice before all candidates", payload)
	}
	wantPayloads[payload] = true
	if len(view.Related) != agency.MaxAgentViewRelated ||
		view.Related[0].Facts.Relation != "correlation" {
		t.Fatalf("bounded related projection = %#v", view.Related)
	}
	wantRelated := peerCount
	if payload != "consider independent observations" {
		// The root has no correlation field of its own, so it is not a
		// related candidate while one of its correlated replies is Current.
		wantRelated--
	}
	if view.Outstanding.OpenTotal != len(wantPayloads) ||
		view.Outstanding.RelatedTotal != wantRelated ||
		view.Outstanding.RelatedProjected != agency.MaxAgentViewRelated ||
		!view.Outstanding.Truncated {
		t.Fatalf("bounded outstanding projection = %#v", view.Outstanding)
	}
}

type focusFederatedPeer struct {
	receiver                   *authorityFixture
	originRoute, receiverRoute PeerRouteSpec
	receiverPrivate            ed25519.PrivateKey
	suffix                     string
	observation                string
}

func newFocusFederatedPeer(t *testing.T, origin *authorityFixture,
	originPrivate ed25519.PrivateKey, index int, suffix string,
) focusFederatedPeer {
	t.Helper()
	receiver := newAuthorityFixture(t, "principal:focus-federated-"+suffix)
	receiverPrivate := ed25519.NewKeyFromSeed(bytes.Repeat(
		[]byte{byte(0x61 + index)}, ed25519.SeedSize))
	originRoute, receiverRoute := focusFederatedRoutePair(t, origin.principal,
		receiver.principal, suffix, originPrivate.Public().(ed25519.PublicKey),
		receiverPrivate.Public().(ed25519.PublicKey))
	mustEnrollPeerRoute(t, origin, originRoute)
	mustEnrollPeerRoute(t, receiver, receiverRoute)
	return focusFederatedPeer{receiver: receiver, originRoute: originRoute,
		receiverRoute: receiverRoute, receiverPrivate: receiverPrivate,
		suffix:      suffix,
		observation: "independent observation " + suffix}
}

func focusFederatedRoutePair(t *testing.T, origin, receiver agency.AgentPrincipalID,
	suffix string, originPublic, receiverPublic ed25519.PublicKey,
) (PeerRouteSpec, PeerRouteSpec) {
	t.Helper()
	routeID := mustRoute(t, "route:focus-federated-"+suffix)
	originRoute := peerRouteSpec(t, origin, "focus-receiver-"+suffix)
	originRoute.RouteID = routeID
	originRoute.RemotePeerID = mustHandle(t, "transport-peer:focus-receiver-"+suffix)
	originRoute.RemotePublicKey = append([]byte(nil), receiverPublic...)
	originRoute.RemoteTargetAlias = mustHandle(t, "target:focus-receiver-"+suffix)
	originRoute.InboundTargetAlias = mustHandle(t, "target:focus-origin-"+suffix)
	receiverRoute := peerRouteSpec(t, receiver, "focus-origin-"+suffix)
	receiverRoute.RouteID = routeID
	receiverRoute.RemotePeerID = mustHandle(t, "transport-peer:focus-origin-"+suffix)
	receiverRoute.RemotePublicKey = append([]byte(nil), originPublic...)
	receiverRoute.RemoteTargetAlias = mustHandle(t, "target:focus-origin-"+suffix)
	receiverRoute.InboundTargetAlias = mustHandle(t, "target:focus-receiver-"+suffix)
	return originRoute, receiverRoute
}

func admitFederatedFocusRequest(t *testing.T, origin *authorityFixture,
	peers []focusFederatedPeer,
) {
	t.Helper()
	successors := []agency.TargetRef{agency.SelfTarget()}
	for _, peer := range peers {
		remote, err := agency.AliasTarget(peer.originRoute.PublicAlias)
		if err != nil {
			t.Fatal(err)
		}
		successors = append(successors, remote)
	}
	operation := mustOperation(t, "operation:focus-federated-request")
	intent := mustIntent(t, agency.IntentSpec{
		Kind:        mustLabel(t, "opaque.consult.request"),
		Payload:     mustPayload(t, "consider independent observations"),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  successors,
	})
	bound, err := origin.current(t).Bind(intent, operation, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := origin.store.Admit(origin.ctx, origin.proof, bound)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
}

func admitFederatedFocusRequestAtPeer(t *testing.T, originPrivate ed25519.PrivateKey,
	peer *focusFederatedPeer, delivery agency.PeerDelivery,
) {
	t.Helper()
	signature := ed25519.Sign(originPrivate, delivery.SigningMessage())
	staged, err := peer.receiver.store.StagePeerDelivery(peer.receiver.ctx,
		peer.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("stage fan-out delivery = %#v, %v", staged, err)
	}
	accepted, err := peer.receiver.store.AdmitPeerDelivery(peer.receiver.ctx, delivery.ID())
	if err != nil || accepted.State() != PeerAdmissionStateAccepted {
		t.Fatalf("admit fan-out delivery = %#v, %v", accepted, err)
	}
}

func admitFederatedFocusObservation(t *testing.T, origin *authorityFixture,
	peer *focusFederatedPeer, request agency.PeerDelivery,
) {
	t.Helper()
	view := peer.receiver.current(t)
	public := decodeFocusView(t, view)
	if public.Current == nil || public.Current.Facts.ReplyTo == "" ||
		!public.Current.Facts.ReplyRequired ||
		public.Current.Facts.ReplyTarget != peer.receiverRoute.PublicAlias.String() {
		t.Fatalf("peer reply authority = %#v", public.Current)
	}
	target, err := agency.AliasTarget(mustHandle(t, public.Current.Facts.ReplyTarget))
	if err != nil {
		t.Fatal(err)
	}
	operation := mustOperation(t, "operation:focus-observation:"+peer.suffix)
	intent := mustIntent(t, agency.IntentSpec{
		Kind:              mustLabel(t, "opaque.consult.observation"),
		Payload:           mustPayload(t, peer.observation),
		Consequence:       agency.ConsequenceAdvanceHandling,
		SubjectHandling:   mustHandle(t, public.Current.Facts.Handle),
		Successors:        []agency.TargetRef{target},
		CorrelationHandle: mustHandle(t, public.Current.Facts.ReplyTo),
	})
	bound, err := view.Bind(intent, operation, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := peer.receiver.store.Admit(peer.receiver.ctx, peer.receiver.proof, bound)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	pending, err := peer.receiver.store.PendingPeerDeliveries(peer.receiver.ctx,
		MaxPendingPeerDeliveries)
	if err != nil || len(pending) != 1 {
		t.Fatalf("peer response delivery = %#v, %v", pending, err)
	}
	response := pending[0].Delivery()
	requireOriginCorrelation(t, response, request.OriginEvent())
	signature := ed25519.Sign(peer.receiverPrivate, response.SigningMessage())
	staged, err := origin.store.StagePeerDelivery(origin.ctx, peer.originRoute.RemotePeerID,
		response.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("stage peer observation = %#v, %v", staged, err)
	}
	accepted, err := origin.store.AdmitPeerDelivery(origin.ctx, response.ID())
	if err != nil || accepted.State() != PeerAdmissionStateAccepted {
		t.Fatalf("admit peer observation = %#v, %v", accepted, err)
	}
}

type focusReferenceState struct{ head, state, artifact string }

func requireFocusReferenceUnchanged(t *testing.T, fixture *authorityFixture,
	want focusReferenceState,
) {
	t.Helper()
	if got := focusReferenceSnapshot(t, fixture, "local.guidance"); got != want {
		t.Fatalf("peer observation changed local Reference: got %#v want %#v", got, want)
	}
	if got := countRows(t, fixture.store, "reference_lineage"); got != 1 {
		t.Fatalf("peer observation created Reference lineage rows = %d, want 1", got)
	}
}

func focusReferenceSnapshot(t *testing.T, fixture *authorityFixture,
	key string,
) focusReferenceState {
	t.Helper()
	var snapshot focusReferenceState
	if err := fixture.store.db.QueryRow(`SELECT head_event_id, state, artifact_digest
		FROM active_references WHERE reference_key = ?`, key).Scan(
		&snapshot.head, &snapshot.state, &snapshot.artifact); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
