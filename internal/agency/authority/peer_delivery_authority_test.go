package authority

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestPeerDeliveryRoundTripUsesTwoLocalAdmissions(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	receipt := fixture.admitReceiver(t, delivery)
	fixture.settleOrigin(t, delivery, receipt)
}

func TestPeerInboxRejectsReceiptAndLocalEventDivergence(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	fixture.admitReceiver(t, delivery)
	digest := fixture.receiver.catalog(t, "unrelated local Reference")
	local := referenceRequest(t, fixture.receiver.current(t), "operation:unrelated-local-event",
		agency.ConsequencePublishReference, "playbook.unrelated", &digest)
	localResult, err := fixture.receiver.store.Admit(fixture.receiver.ctx, fixture.receiver.proof, local)
	if err != nil {
		t.Fatal(err)
	}
	localReceipt, err := agency.ParseReceiptCanonicalJSON(localResult.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	unrelated, ok := localReceipt.Event()
	if !ok {
		t.Fatal("local admission did not create an Event")
	}
	if _, err := fixture.receiver.store.db.Exec(`UPDATE peer_inbox SET local_event_id = ?
		WHERE delivery_id = ?`, unrelated.ID().String(), delivery.ID().String()); err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(fixture.originPrivate, delivery.SigningMessage())
	if _, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature); err == nil {
		t.Fatal("receipt/local Event divergence unexpectedly replayed")
	}
}

func TestMissingPeerArtifactExpiresWithoutCreatingDomainState(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	signature := ed25519.Sign(fixture.originPrivate, delivery.SigningMessage())
	staged, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("StagePeerDelivery() = %#v, %v", staged, err)
	}
	*fixture.receiver.now = delivery.ExpiresAt()
	expired, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx, delivery.ID())
	if err != nil || expired.State() != PeerAdmissionStateExpired {
		t.Fatalf("expired admission = %#v, %v", expired, err)
	}
	if countRows(t, fixture.receiver.store, "events") != 0 ||
		countRows(t, fixture.receiver.store, "handlings") != 0 {
		t.Fatal("expired delivery created local domain state")
	}
	pending, err := fixture.receiver.store.StagedPeerDeliveries(fixture.receiver.ctx,
		MaxStagedPeerDeliveries)
	if err != nil || len(pending) != 0 {
		t.Fatalf("staged projection after expiry = %#v, %v", pending, err)
	}
	replayed, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
	if err != nil || replayed.State() != PeerAdmissionStateExpired || !replayed.Replayed() {
		t.Fatalf("expired delivery replay = %#v, %v", replayed, err)
	}
}

func TestRemoteRejectionAndExpiryLeaveOriginAnchorOpen(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		fixture := newPeerRoundTripFixture(t)
		delivery := fixture.admitOrigin(t)
		signature := ed25519.Sign(fixture.originPrivate, delivery.SigningMessage())
		staged, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
			fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
		if err != nil || staged.State() != PeerAdmissionStateStaged {
			t.Fatalf("StagePeerDelivery() = %#v, %v", staged, err)
		}
		if got := fixture.receiver.catalog(t, fixture.content); got != fixture.digest {
			t.Fatalf("receiver Artifact digest = %s, want %s", got.String(), fixture.digest.String())
		}
		if _, err := fixture.receiver.store.RevokePeerRoute(fixture.receiver.ctx,
			fixture.receiverRoute.RouteID); err != nil {
			t.Fatal(err)
		}
		rejected, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx, delivery.ID())
		if err != nil || rejected.State() != PeerAdmissionStateRejected {
			t.Fatalf("rejected peer admission = %#v, %v", rejected, err)
		}
		receipt, ok := rejected.Receipt()
		if !ok || receipt.Outcome() != agency.PeerAdmissionOutcomeRejected {
			t.Fatalf("rejected peer Receipt = %#v", receipt)
		}
		assertPeerRows(t, fixture.receiver, 0, 0, 0, 1)
		fixture.settleOrigin(t, delivery, receipt)
		assertOriginAnchorOpen(t, fixture.origin)
	})

	t.Run("expired", func(t *testing.T) {
		fixture := newPeerRoundTripFixture(t)
		delivery := fixture.admitOrigin(t)
		signature := ed25519.Sign(fixture.originPrivate, delivery.SigningMessage())
		if staged, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
			fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature); err != nil ||
			staged.State() != PeerAdmissionStateStaged {
			t.Fatalf("StagePeerDelivery() = %#v, %v", staged, err)
		}
		*fixture.receiver.now = delivery.ExpiresAt()
		if expired, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx,
			delivery.ID()); err != nil || expired.State() != PeerAdmissionStateExpired {
			t.Fatalf("expired peer admission = %#v, %v", expired, err)
		}
		*fixture.origin.now = delivery.ExpiresAt()
		if pending, err := fixture.origin.store.PendingPeerDeliveries(fixture.origin.ctx,
			MaxPendingPeerDeliveries); err != nil || len(pending) != 0 {
			t.Fatalf("expired origin outbox projection = %#v, %v", pending, err)
		}
		var state string
		if err := fixture.origin.store.db.QueryRow(`SELECT state FROM peer_outbox
			WHERE delivery_id = ?`, delivery.ID().String()).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "expired" {
			t.Fatalf("origin delivery state = %q, want expired", state)
		}
		assertOriginAnchorOpen(t, fixture.origin)
	})
}

func TestPeerInboxRejectsSameDeliveryIDDifferentEnvelope(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	signature := ed25519.Sign(fixture.originPrivate, delivery.SigningMessage())
	if staged, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature); err != nil ||
		staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("StagePeerDelivery() = %#v, %v", staged, err)
	}
	correlation, _ := delivery.OriginCorrelation()
	conflict, err := agency.NewPeerDelivery(fixture.originRoute.RouteID, agency.PeerDeliverySpec{
		OriginEvent: delivery.OriginEvent(), OriginSequence: delivery.OriginSequence(),
		OriginAcceptedAt: delivery.OriginAcceptedAt(), OriginSource: delivery.OriginSource(),
		OriginConsequence: delivery.OriginConsequence(),
		OriginTargetCount: uint8(delivery.OriginTargetCount()),
		OriginCausation:   delivery.OriginCausation(), OriginCorrelation: correlation,
		TargetAlias: delivery.TargetAlias(), Kind: delivery.Kind(),
		Payload:   mustPayload(t, "same identity with different immutable semantics"),
		Artifacts: delivery.Artifacts(), CausalDepth: delivery.CausalDepth(),
		ExpiresAt: delivery.ExpiresAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.ID() != delivery.ID() || conflict.EnvelopeDigest() == delivery.EnvelopeDigest() {
		t.Fatal("test fixture did not preserve DeliveryID while changing the envelope")
	}
	conflictSignature := ed25519.Sign(fixture.originPrivate, conflict.SigningMessage())
	if _, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, conflict.CanonicalJSON(), conflictSignature); !errors.Is(err, ErrPeerDeliveryConflict) {
		t.Fatalf("same DeliveryID with different envelope = %v, want ErrPeerDeliveryConflict", err)
	}
	if got := countRows(t, fixture.receiver.store, "peer_inbox"); got != 1 {
		t.Fatalf("conflicting Delivery created %d inbox rows, want 1", got)
	}
}

func TestSettledPeerDeliveryReplayStillAuthenticatesActorAndReceipt(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	receipt := fixture.admitReceiver(t, delivery)
	fixture.settleOrigin(t, delivery, receipt)
	correctSignature := ed25519.Sign(fixture.receiverPrivate, receipt.SigningMessage())
	wrongPeer := mustHandle(t, "transport-peer:not-the-receiver")
	if _, _, err := fixture.origin.store.SettlePeerDelivery(fixture.origin.ctx, wrongPeer,
		delivery.ID(), receipt.CanonicalJSON(), correctSignature); !errors.Is(err, ErrPeerAuthentication) {
		t.Fatalf("settled replay with wrong peer error = %v", err)
	}
	wrongSignature := ed25519.Sign(fixture.originPrivate, receipt.SigningMessage())
	if _, _, err := fixture.origin.store.SettlePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, delivery.ID(), receipt.CanonicalJSON(),
		wrongSignature); !errors.Is(err, ErrPeerAuthentication) {
		t.Fatalf("settled replay with wrong signature error = %v", err)
	}
	wrongDigest := agency.Sum([]byte("wrong receipt digest"))
	if _, err := fixture.origin.store.db.Exec(`UPDATE peer_outbox SET receipt_digest = ?
		WHERE delivery_id = ?`, wrongDigest.String(), delivery.ID().String()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.origin.store.SettlePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, delivery.ID(), receipt.CanonicalJSON(),
		correctSignature); err == nil {
		t.Fatal("settled replay accepted corrupt stored Receipt digest")
	}
}

type peerRoundTripFixture struct {
	origin, receiver               *authorityFixture
	originRoute, receiverRoute     PeerRouteSpec
	originPrivate, receiverPrivate ed25519.PrivateKey
	content                        string
	digest                         agency.Digest
}

func newPeerRoundTripFixture(t *testing.T) peerRoundTripFixture {
	t.Helper()
	origin := newAuthorityFixture(t, "principal:peer-origin")
	receiver := newAuthorityFixture(t, "principal:peer-receiver")
	originPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	receiverPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	originRoute, receiverRoute := peerRoutePair(t, origin.principal, receiver.principal,
		originPrivate.Public().(ed25519.PublicKey), receiverPrivate.Public().(ed25519.PublicKey))
	mustEnrollPeerRoute(t, origin, originRoute)
	mustEnrollPeerRoute(t, receiver, receiverRoute)
	content := "bounded cross-node Artifact"
	return peerRoundTripFixture{origin: origin, receiver: receiver, originRoute: originRoute,
		receiverRoute: receiverRoute, originPrivate: originPrivate, receiverPrivate: receiverPrivate,
		content: content, digest: origin.catalog(t, content)}
}

func mustEnrollPeerRoute(t *testing.T, fixture *authorityFixture, route PeerRouteSpec) {
	t.Helper()
	if _, err := fixture.store.EnrollPeerRoute(fixture.ctx, route); err != nil {
		t.Fatal(err)
	}
}

func (fixture peerRoundTripFixture) admitOrigin(t *testing.T) agency.PeerDelivery {
	t.Helper()
	request := remoteRootRequest(t, fixture.origin.current(t), "operation:peer-round-trip",
		fixture.originRoute.PublicAlias, &fixture.digest)
	result, err := fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	assertPeerRows(t, fixture.origin, 1, 1, 1, 0)
	pending, err := fixture.origin.store.PendingPeerDeliveries(fixture.origin.ctx,
		MaxPendingPeerDeliveries)
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingPeerDeliveries() = %#v, %v", pending, err)
	}
	delivery := pending[0].Delivery()
	if delivery.CausalDepth() != 1 || delivery.Artifacts()[0] != fixture.digest ||
		pending[0].Route().RouteID() != fixture.originRoute.RouteID {
		t.Fatalf("outbound delivery = %#v", delivery)
	}
	if entitled, err := fixture.origin.store.AuthorizePeerArtifact(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, delivery.ID(), delivery.EnvelopeDigest(),
		fixture.digest); err != nil || !entitled {
		t.Fatalf("exact Artifact entitlement = %t, %v", entitled, err)
	}
	assertPeerArtifactNotEntitled(t, fixture.origin, fixture.originRoute, delivery, fixture.digest)
	return delivery
}

func (fixture peerRoundTripFixture) admitReceiver(t *testing.T,
	delivery agency.PeerDelivery,
) agency.PeerAdmissionReceipt {
	t.Helper()
	signature := ed25519.Sign(fixture.originPrivate, delivery.SigningMessage())
	staged, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("StagePeerDelivery() = %#v, %v", staged, err)
	}
	beforeArtifact, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx, delivery.ID())
	if err != nil || beforeArtifact.State() != PeerAdmissionStateStaged {
		t.Fatalf("admission before Artifact = %#v, %v", beforeArtifact, err)
	}
	if got := fixture.receiver.catalog(t, fixture.content); got != fixture.digest {
		t.Fatalf("receiver Artifact digest = %s, want %s", got.String(), fixture.digest.String())
	}
	accepted, err := fixture.receiver.store.AdmitPeerDelivery(fixture.receiver.ctx, delivery.ID())
	if err != nil || accepted.State() != PeerAdmissionStateAccepted {
		t.Fatalf("AdmitPeerDelivery() = %#v, %v", accepted, err)
	}
	receipt, ok := accepted.Receipt()
	if !ok || receipt.Outcome() != agency.PeerAdmissionOutcomeAccepted {
		t.Fatalf("receiver Receipt = %#v", receipt)
	}
	assertPeerRows(t, fixture.receiver, 1, 1, 0, 1)
	replayed, err := fixture.receiver.store.StagePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
	if err != nil || !replayed.Replayed() || replayed.State() != PeerAdmissionStateAccepted {
		t.Fatalf("inbound replay = %#v, %v", replayed, err)
	}
	return receipt
}

func (fixture peerRoundTripFixture) settleOrigin(t *testing.T, delivery agency.PeerDelivery,
	receipt agency.PeerAdmissionReceipt,
) {
	t.Helper()
	signature := ed25519.Sign(fixture.receiverPrivate, receipt.SigningMessage())
	settled, replayed, err := fixture.origin.store.SettlePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, delivery.ID(), receipt.CanonicalJSON(), signature)
	if err != nil || replayed || settled.Digest() != receipt.Digest() {
		t.Fatalf("SettlePeerDelivery() = (%s, %t, %v)", settled.Digest().String(), replayed, err)
	}
	settled, replayed, err = fixture.origin.store.SettlePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, delivery.ID(), receipt.CanonicalJSON(), signature)
	if err != nil || !replayed || settled.Digest() != receipt.Digest() {
		t.Fatalf("settlement replay = (%s, %t, %v)", settled.Digest().String(), replayed, err)
	}
	remaining, err := fixture.origin.store.PendingPeerDeliveries(fixture.origin.ctx,
		MaxPendingPeerDeliveries)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("settled pending projection = %#v, %v", remaining, err)
	}
	if countRows(t, fixture.origin.store, "handlings") != 1 {
		t.Fatal("remote Receipt incorrectly closed the origin local responsibility anchor")
	}
	assertOriginAnchorOpen(t, fixture.origin)
}

func assertOriginAnchorOpen(t *testing.T, fixture *authorityFixture) {
	t.Helper()
	var state string
	var outcome, claimAttachment any
	if err := fixture.store.db.QueryRow(`SELECT state, outcome, claim_attachment_id
		FROM handlings LIMIT 1`).Scan(&state, &outcome, &claimAttachment); err != nil {
		t.Fatal(err)
	}
	if state != "open" || outcome != nil || claimAttachment != nil {
		t.Fatalf("origin anchor = state:%s outcome:%v claim:%v", state, outcome, claimAttachment)
	}
}

func assertPeerRows(t *testing.T, fixture *authorityFixture,
	events, handlings, outbox, inbox int,
) {
	t.Helper()
	for table, want := range map[string]int{"events": events, "handlings": handlings,
		"peer_outbox": outbox, "peer_inbox": inbox} {
		if got := countRows(t, fixture.store, table); got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
}

func TestPeerDeliveryOutboxFailureRollsBackWholeLocalEffect(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:peer-atomic")
	spec := peerRouteSpec(t, fixture.principal, "atomic")
	if _, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec); err != nil {
		t.Fatal(err)
	}
	request := remoteRootRequest(t, fixture.current(t), "operation:peer-atomic",
		spec.PublicAlias, nil)
	if _, err := fixture.store.db.Exec(`CREATE TRIGGER fail_peer_outbox BEFORE INSERT ON peer_outbox
		BEGIN SELECT RAISE(ABORT, 'injected outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, request); err == nil {
		t.Fatal("injected outbox failure unexpectedly admitted")
	}
	for _, table := range []string{"events", "handlings", "peer_outbox", "operations"} {
		if got := countRows(t, fixture.store, table); got != 0 {
			t.Fatalf("%s rows after rollback = %d", table, got)
		}
	}
	if _, err := fixture.store.db.Exec(`DROP TRIGGER fail_peer_outbox`); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
}

func remoteRootRequest(t *testing.T, view BoundView, operationValue string,
	remoteAlias agency.OpaqueHandle, artifact *agency.Digest,
) agency.BoundIntent {
	t.Helper()
	remote, err := agency.AliasTarget(remoteAlias)
	if err != nil {
		t.Fatal(err)
	}
	operation := mustOperation(t, operationValue)
	spec := agency.IntentSpec{Kind: mustLabel(t, "custom.agent.signal"),
		Payload:     mustPayload(t, "consider the bounded request"),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget(), remote}}
	var candidates []agency.CapturedCandidate
	if artifact != nil {
		handle := mustHandle(t, "candidate:"+operationValue)
		input, err := agency.NewArtifactCandidate(handle)
		if err != nil {
			t.Fatal(err)
		}
		spec.Artifacts = []agency.ArtifactInput{input}
		candidate, err := agency.NewCapturedCandidate(operation, input, *artifact)
		if err != nil {
			t.Fatal(err)
		}
		candidates = []agency.CapturedCandidate{candidate}
	}
	request, err := view.Bind(mustIntent(t, spec), operation, candidates)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func peerRoutePair(t *testing.T, origin, receiver agency.AgentPrincipalID,
	originPublic, receiverPublic ed25519.PublicKey,
) (PeerRouteSpec, PeerRouteSpec) {
	t.Helper()
	routeID := mustRoute(t, "route:shared-peer-round-trip")
	originRoute := peerRouteSpec(t, origin, "receiver")
	originRoute.RouteID = routeID
	originRoute.RemotePeerID = mustHandle(t, "transport-peer:receiver")
	originRoute.RemotePublicKey = append([]byte(nil), receiverPublic...)
	originRoute.RemoteTargetAlias = mustHandle(t, "target:receiver")
	originRoute.InboundTargetAlias = mustHandle(t, "target:origin")
	receiverRoute := peerRouteSpec(t, receiver, "origin")
	receiverRoute.RouteID = routeID
	receiverRoute.RemotePeerID = mustHandle(t, "transport-peer:origin")
	receiverRoute.RemotePublicKey = append([]byte(nil), originPublic...)
	receiverRoute.RemoteTargetAlias = mustHandle(t, "target:origin")
	receiverRoute.InboundTargetAlias = mustHandle(t, "target:receiver")
	return originRoute, receiverRoute
}

func assertPeerArtifactNotEntitled(t *testing.T, fixture *authorityFixture,
	route PeerRouteSpec, delivery agency.PeerDelivery, digest agency.Digest,
) {
	t.Helper()
	wrongPeer := mustHandle(t, "transport-peer:wrong")
	wrongEnvelope := agency.Sum([]byte("wrong envelope"))
	wrongObject := agency.Sum([]byte("wrong object"))
	for name, peerAndDigest := range map[string]struct {
		peer     agency.OpaqueHandle
		envelope agency.Digest
		object   agency.Digest
	}{
		"peer":     {peer: wrongPeer, envelope: delivery.EnvelopeDigest(), object: digest},
		"envelope": {peer: route.RemotePeerID, envelope: wrongEnvelope, object: digest},
		"object":   {peer: route.RemotePeerID, envelope: delivery.EnvelopeDigest(), object: wrongObject},
	} {
		t.Run(name, func(t *testing.T) {
			allowed, err := fixture.store.AuthorizePeerArtifact(fixture.ctx, peerAndDigest.peer,
				delivery.ID(), peerAndDigest.envelope, peerAndDigest.object)
			if err != nil || allowed {
				t.Fatalf("wrong %s entitlement = %t, %v", name, allowed, err)
			}
		})
	}
}
