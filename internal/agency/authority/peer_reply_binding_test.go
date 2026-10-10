package authority

import (
	"crypto/ed25519"
	"database/sql"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestTerminalReplyFollowsBoundSubjectAdvanceAnchor(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	root := rootRequest(t, fixture.origin.current(t), "operation:later-remote-root",
		"retain local responsibility before choosing a peer")
	result, err := fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, root)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	anchor := requireOnlyHandlingID(t, fixture.origin)

	delivery := admitRemoteAdvance(t, fixture.origin, fixture.originRoute.PublicAlias,
		"operation:later-remote-advance")
	assertPeerReplyBinding(t, fixture.origin, delivery.ID(), anchor, delivery.OriginEvent())
	stageAndAdmitArtifactFreeDelivery(t, fixture.receiver, fixture.receiverRoute.RemotePeerID,
		fixture.originPrivate, delivery)

	reply := admitTerminalDeclineFromCurrent(t, fixture.receiver)
	assertPeerReplyBindingAbsent(t, fixture.receiver, reply.ID())
	accepted := stageAndAdmitPeerDelivery(t, &fixture, reply)
	if accepted.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal reply state = %v, want accepted", accepted.State())
	}
	assertHandlingOpenByID(t, fixture.origin, anchor)
	events, handlings := countRows(t, fixture.origin.store, "events"),
		countRows(t, fixture.origin.store, "handlings")

	signature := ed25519.Sign(fixture.receiverPrivate, reply.SigningMessage())
	replayedStage, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, reply.CanonicalJSON(), signature)
	if err != nil || !replayedStage.Replayed() || replayedStage.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal reply stage replay = %#v, %v", replayedStage, err)
	}
	replayed, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx, reply.ID())
	if err != nil || !replayed.Replayed() || replayed.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal reply admission replay = %#v, %v", replayed, err)
	}
	if countRows(t, fixture.origin.store, "events") != events ||
		countRows(t, fixture.origin.store, "handlings") != handlings {
		t.Fatal("terminal reply replay created a second local effect")
	}
	assertHandlingOpenByID(t, fixture.origin, anchor)
}

func admitRemoteAdvance(t *testing.T, fixture *authorityFixture,
	remoteAlias agency.OpaqueHandle, operationValue string,
) agency.PeerDelivery {
	t.Helper()
	view := fixture.current(t)
	remote, err := agency.AliasTarget(remoteAlias)
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, agency.IntentSpec{
		Kind:            mustLabel(t, "opaque.remote-work"),
		Payload:         mustPayload(t, "request bounded work from the selected peer"),
		Consequence:     agency.ConsequenceAdvanceHandling,
		SubjectHandling: currentSubjectHandle(t, view),
		Successors:      []agency.TargetRef{remote},
	})
	bound, err := view.Bind(intent, mustOperation(t, operationValue), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, bound)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	return requirePendingDeliveryForRoute(t, fixture, bound.Targets()[0].RemoteRoute())
}

func admitTerminalDeclineFromCurrent(t *testing.T,
	fixture *authorityFixture,
) agency.PeerDelivery {
	t.Helper()
	view := fixture.current(t)
	public := decodeFocusView(t, view)
	if public.Current == nil || !public.Current.Facts.ReplyRequired {
		t.Fatalf("terminal responder has no required reply context: %#v", public.Current)
	}
	target, err := agency.AliasTarget(mustHandle(t, public.Current.Facts.ReplyTarget))
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, agency.IntentSpec{
		Kind:              mustLabel(t, "opaque.terminal-reply"),
		Payload:           mustPayload(t, "bounded work is declined"),
		Consequence:       agency.ConsequenceResolveDeclined,
		SubjectHandling:   mustHandle(t, public.Current.Facts.Handle),
		Successors:        []agency.TargetRef{target},
		CorrelationHandle: mustHandle(t, public.Current.Facts.ReplyTo),
	})
	bound, err := view.Bind(intent,
		mustOperation(t, "operation:terminal-decline:"+public.Current.Facts.Handle), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, bound)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	return requireOnePendingDelivery(t, fixture)
}

func stageAndAdmitArtifactFreeDelivery(t *testing.T, receiver *authorityFixture,
	remotePeer agency.OpaqueHandle, signer ed25519.PrivateKey, delivery agency.PeerDelivery,
) agency.PeerAdmissionReceipt {
	t.Helper()
	signature := ed25519.Sign(signer, delivery.SigningMessage())
	staged, err := receiver.store.StagePeerDelivery(receiver.ctx, remotePeer,
		delivery.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("stage Artifact-free delivery = %#v, %v", staged, err)
	}
	accepted, err := receiver.store.AdmitPeerDelivery(receiver.ctx, delivery.ID())
	if err != nil || accepted.State() != PeerAdmissionStateAccepted {
		t.Fatalf("admit Artifact-free delivery = %#v, %v", accepted, err)
	}
	receipt, present := accepted.Receipt()
	if !present {
		t.Fatal("accepted Artifact-free delivery has no Receipt")
	}
	return receipt
}

func requirePendingDeliveryForRoute(t *testing.T, fixture *authorityFixture,
	route agency.RouteID,
) agency.PeerDelivery {
	t.Helper()
	pending, err := fixture.store.PendingPeerDeliveries(fixture.ctx, MaxPendingPeerDeliveries)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pending {
		if item.Route().RouteID() == route {
			return item.Delivery()
		}
	}
	t.Fatalf("no pending delivery for route %s", route.String())
	return agency.PeerDelivery{}
}

func assertPeerReplyBinding(t *testing.T, fixture *authorityFixture,
	delivery agency.DeliveryID, wantHandling string, wantRoot agency.EventRef,
) {
	t.Helper()
	var handling, rootID, rootDigest string
	err := fixture.store.db.QueryRow(`SELECT reply_anchor_handling_id,
		expected_reply_root_event_id, expected_reply_root_event_digest
		FROM peer_outbox WHERE delivery_id = ?`, delivery.String()).
		Scan(&handling, &rootID, &rootDigest)
	if err != nil {
		t.Fatal(err)
	}
	if handling != wantHandling || rootID != wantRoot.ID().String() ||
		rootDigest != wantRoot.Digest().String() {
		t.Fatalf("reply binding = handling:%q root:%q/%q; want %q %v",
			handling, rootID, rootDigest, wantHandling, wantRoot)
	}
}

func assertPeerReplyBindingAbsent(t *testing.T, fixture *authorityFixture,
	delivery agency.DeliveryID,
) {
	t.Helper()
	var handling, rootID, rootDigest sql.NullString
	err := fixture.store.db.QueryRow(`SELECT reply_anchor_handling_id,
		expected_reply_root_event_id, expected_reply_root_event_digest
		FROM peer_outbox WHERE delivery_id = ?`, delivery.String()).
		Scan(&handling, &rootID, &rootDigest)
	if err != nil {
		t.Fatal(err)
	}
	if handling.Valid || rootID.Valid || rootDigest.Valid {
		t.Fatalf("terminal reply outbox acquired reply binding: %#v %#v %#v",
			handling, rootID, rootDigest)
	}
}
