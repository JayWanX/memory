package authority

import (
	"crypto/ed25519"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

type terminalObservationFixture struct {
	peerRoundTripFixture
	request  agency.PeerDelivery
	response agency.PeerDelivery
	anchor   string
}

func TestDecidePeerEffectOwnsConsequenceTargetAndCardinality(t *testing.T) {
	ordinary := verifiedPeerEffectFixture(t, agency.ConsequenceCreateHandlings, false, false)
	effect, err := decidePeerEffect(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if effect.consequence != agency.ConsequenceCreateHandlings || len(effect.targets) != 1 ||
		effect.targets[0].Destination() != agency.TargetDestinationLocal ||
		effect.targets[0].LocalPrincipal() != ordinary.LocalTarget() ||
		effect.targets[0].Requested().Alias() != ordinary.Delivery().TargetAlias() {
		t.Fatalf("ordinary decided peer effect = %#v", effect)
	}

	for _, test := range []struct {
		origin agency.Consequence
		want   agency.Consequence
	}{
		{agency.ConsequenceResolveCompleted, agency.ConsequenceObserveCompleted},
		{agency.ConsequenceResolveDeclined, agency.ConsequenceObserveDeclined},
		{agency.ConsequenceResolveUnresolved, agency.ConsequenceObserveUnresolved},
	} {
		t.Run(test.origin.String(), func(t *testing.T) {
			verified := verifiedPeerEffectFixture(t, test.origin, true,
				test.origin == agency.ConsequenceResolveCompleted)
			effect, err := decidePeerEffect(verified)
			if err != nil {
				t.Fatal(err)
			}
			if effect.consequence != test.want || len(effect.targets) != 0 {
				t.Fatalf("terminal decided peer effect = %s/%d, want %s/0",
					effect.consequence.String(), len(effect.targets), test.want.String())
			}
		})
	}
}

func TestCompletedTerminalReplyWithoutArtifactFailsBeforeDomainWrite(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	base := fixture.response
	correlation, _ := base.OriginCorrelation()
	inReplyTo, _ := base.InReplyToDelivery()
	malformed, err := agency.NewPeerDelivery(fixture.receiverRoute.RouteID, agency.PeerDeliverySpec{
		OriginEvent: base.OriginEvent(), OriginSequence: base.OriginSequence(),
		OriginAcceptedAt: base.OriginAcceptedAt(), OriginSource: base.OriginSource(),
		OriginConsequence: agency.ConsequenceResolveCompleted, OriginTargetCount: 1,
		OriginCausation: base.OriginCausation(), OriginCorrelation: correlation,
		InReplyToDelivery: inReplyTo, TargetAlias: base.TargetAlias(), Kind: base.Kind(),
		Payload: base.Payload(), CausalDepth: base.CausalDepth(), ExpiresAt: base.ExpiresAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(fixture.receiverPrivate, malformed.SigningMessage())
	staged, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, malformed.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("stage malformed completed reply = %#v, %v", staged, err)
	}
	beforeEvents := countRows(t, fixture.origin.store, "events")
	beforeHandlings := countRows(t, fixture.origin.store, "handlings")
	beforeInbox := countRows(t, fixture.origin.store, "peer_inbox")
	var beforeSequence uint64
	if err := fixture.origin.store.db.QueryRow(`SELECT origin_sequence FROM authority_clock
		WHERE singleton = 1`).Scan(&beforeSequence); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx,
		malformed.ID()); err == nil {
		t.Fatal("completed reply without Artifact unexpectedly admitted")
	}
	if got := countRows(t, fixture.origin.store, "events"); got != beforeEvents {
		t.Fatalf("failed completed reply Event rows = %d, want %d", got, beforeEvents)
	}
	if got := countRows(t, fixture.origin.store, "handlings"); got != beforeHandlings {
		t.Fatalf("failed completed reply Handling rows = %d, want %d", got, beforeHandlings)
	}
	if got := countRows(t, fixture.origin.store, "peer_inbox"); got != beforeInbox {
		t.Fatalf("failed completed reply inbox rows = %d, want %d", got, beforeInbox)
	}
	var state string
	var localEvent, receiptDigest, receiptJSON, settledAt any
	if err := fixture.origin.store.db.QueryRow(`SELECT state, local_event_id,
		receipt_digest, receipt_json, settled_at
		FROM peer_inbox WHERE delivery_id = ?`, malformed.ID().String()).
		Scan(&state, &localEvent, &receiptDigest, &receiptJSON, &settledAt); err != nil {
		t.Fatal(err)
	}
	var afterSequence uint64
	if err := fixture.origin.store.db.QueryRow(`SELECT origin_sequence FROM authority_clock
		WHERE singleton = 1`).Scan(&afterSequence); err != nil {
		t.Fatal(err)
	}
	if state != "staged" || localEvent != nil || receiptDigest != nil || receiptJSON != nil ||
		settledAt != nil || afterSequence != beforeSequence {
		t.Fatalf("failed completed reply changed durable state: state=%s event=%v receipt=%v/%v settled=%v sequence=%d/%d",
			state, localEvent, receiptDigest, receiptJSON, settledAt, afterSequence, beforeSequence)
	}
}

func newTerminalObservationFixture(t *testing.T) terminalObservationFixture {
	t.Helper()
	fixture := newPeerRoundTripFixture(t)
	request := fixture.admitOrigin(t)
	anchor := requireOnlyHandlingID(t, fixture.origin)
	receipt := fixture.admitReceiver(t, request)
	fixture.settleOrigin(t, request, receipt)
	response := admitTerminalDeclineFromCurrent(t, fixture.receiver)
	inReplyTo, present := response.InReplyToDelivery()
	if !present || inReplyTo != request.ID() {
		t.Fatalf("terminal response in-reply-to = %v,%t; want %v", inReplyTo, present, request.ID())
	}
	return terminalObservationFixture{peerRoundTripFixture: fixture, request: request,
		response: response, anchor: anchor}
}

func TestTerminalReplyObservationCreatesNoHandlingAndLeavesAnchorOpen(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	beforeEvents := countRows(t, fixture.origin.store, "events")
	beforeHandlings := countRows(t, fixture.origin.store, "handlings")
	result := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture, fixture.response)
	if result.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal observation state = %v, want accepted", result.State())
	}
	if got := countRows(t, fixture.origin.store, "events"); got != beforeEvents+1 {
		t.Fatalf("Event rows = %d, want %d", got, beforeEvents+1)
	}
	if got := countRows(t, fixture.origin.store, "handlings"); got != beforeHandlings {
		t.Fatalf("Handling rows = %d, want unchanged %d", got, beforeHandlings)
	}
	assertHandlingOpenByID(t, fixture.origin, fixture.anchor)

	receipt, present := result.Receipt()
	if !present {
		t.Fatal("accepted terminal observation has no Receipt")
	}
	local, present := receipt.LocalEvent()
	if !present {
		t.Fatal("accepted terminal observation has no local Event")
	}
	tx, err := fixture.origin.store.db.BeginTx(fixture.origin.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	details, err := loadStoredEventDetailsTx(fixture.origin.ctx, tx, local.ID().String())
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if details.consequence != agency.ConsequenceObserveDeclined ||
		details.inReplyTo != fixture.request.ID() {
		t.Fatalf("observation Event = consequence:%s in-reply-to:%s",
			details.consequence.String(), details.inReplyTo.String())
	}

	beforeEvents = countRows(t, fixture.origin.store, "events")
	signature := ed25519.Sign(fixture.receiverPrivate, fixture.response.SigningMessage())
	replayedStage, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, fixture.response.CanonicalJSON(), signature)
	if err != nil || !replayedStage.Replayed() || replayedStage.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal observation stage replay = %#v, %v", replayedStage, err)
	}
	replayed, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx, fixture.response.ID())
	if err != nil || !replayed.Replayed() || replayed.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal observation admission replay = %#v, %v", replayed, err)
	}
	if countRows(t, fixture.origin.store, "events") != beforeEvents ||
		countRows(t, fixture.origin.store, "handlings") != beforeHandlings {
		t.Fatal("terminal observation replay created a second effect")
	}
}

func TestTerminalReplyObservationRequiresExactInReplyToDeliveryBinding(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	wrong := mustDeliveryIDValue(t, "wrong-in-reply-to")
	candidate := rebuildTerminalObservationDelivery(t, fixture.originRoute.RouteID,
		fixture.response, focusEventRef(t, "event:wrong-in-reply", "wrong in reply"),
		mustCorrelation(t, fixture.response), wrong)
	requireTerminalReplyRejected(t, &fixture.peerRoundTripFixture, candidate)
	assertHandlingOpenByID(t, fixture.origin, fixture.anchor)
}

func TestTerminalReplyObservationRejectsWrongRouteRootPrincipalOrClosedAnchor(t *testing.T) {
	t.Run("wrong root", func(t *testing.T) {
		fixture := newTerminalObservationFixture(t)
		candidate := rebuildTerminalObservationDelivery(t, fixture.originRoute.RouteID,
			fixture.response, focusEventRef(t, "event:wrong-root-reply", "wrong root reply"),
			focusEventRef(t, "event:wrong-root", "wrong root"), fixture.request.ID())
		requireTerminalReplyRejected(t, &fixture.peerRoundTripFixture, candidate)
	})
	t.Run("wrong route", func(t *testing.T) {
		fixture := newTerminalObservationFixture(t)
		other := newFocusFederatedPeer(t, fixture.origin, fixture.originPrivate, 7,
			"terminal-observation-wrong-route")
		candidate, err := agency.NewPeerDelivery(other.receiverRoute.RouteID, agency.PeerDeliverySpec{
			OriginEvent:      focusEventRef(t, "event:wrong-route-reply", "wrong route reply"),
			OriginSequence:   fixture.response.OriginSequence(),
			OriginAcceptedAt: fixture.response.OriginAcceptedAt(), OriginSource: other.receiver.principal,
			OriginConsequence: agency.ConsequenceResolveDeclined, OriginTargetCount: 1,
			OriginCorrelation: mustCorrelation(t, fixture.response), InReplyToDelivery: fixture.request.ID(),
			TargetAlias: other.receiverRoute.RemoteTargetAlias, Kind: fixture.response.Kind(),
			Payload: fixture.response.Payload(), CausalDepth: fixture.response.CausalDepth(),
			ExpiresAt: fixture.response.ExpiresAt(),
		})
		if err != nil {
			t.Fatal(err)
		}
		signature := ed25519.Sign(other.receiverPrivate, candidate.SigningMessage())
		staged, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
			other.originRoute.RemotePeerID, candidate.CanonicalJSON(), signature)
		if err != nil || staged.State() != PeerAdmissionStateStaged {
			t.Fatalf("stage wrong-route candidate = %#v, %v", staged, err)
		}
		result, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx, candidate.ID())
		if err != nil || result.State() != PeerAdmissionStateRejected {
			t.Fatalf("wrong-route candidate = %#v, %v", result, err)
		}
	})
	t.Run("wrong principal", func(t *testing.T) {
		fixture := newTerminalObservationFixture(t)
		other := mustPrincipal(t, "principal:terminal-observation-other")
		if err := fixture.origin.store.EnrollPrincipal(fixture.origin.ctx, other); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.origin.store.db.Exec(`UPDATE handlings SET target_principal_id = ?
			WHERE handling_id = ?`, other.String(), fixture.anchor); err != nil {
			t.Fatal(err)
		}
		requireTerminalReplyRejected(t, &fixture.peerRoundTripFixture, fixture.response)
	})
	t.Run("closed anchor", func(t *testing.T) {
		fixture := newTerminalObservationFixture(t)
		closeCurrentLocally(t, fixture.origin, "operation:close-terminal-observation-anchor")
		requireTerminalReplyRejected(t, &fixture.peerRoundTripFixture, fixture.response)
	})
}

func TestTerminalReplyObservationIsUniquePerOutboundDelivery(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	first := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture, fixture.response)
	if first.State() != PeerAdmissionStateAccepted {
		t.Fatalf("first observation = %v", first.State())
	}
	second := rebuildTerminalObservationDelivery(t, fixture.originRoute.RouteID,
		fixture.response, focusEventRef(t, "event:second-reply", "second reply"),
		mustCorrelation(t, fixture.response), fixture.request.ID())
	requireTerminalReplyRejected(t, &fixture.peerRoundTripFixture, second)
	if revision := terminalObservationRevision(t, fixture.origin, fixture.anchor); revision != 1 {
		t.Fatalf("observation revision = %d, want 1", revision)
	}
}

func TestTerminalReplyObservationBoundFailsClosedAtSixtyFour(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	response := fixture.response
	for index := 0; index < MaxTerminalObservationsPerAnchor; index++ {
		result := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture, response)
		if result.State() != PeerAdmissionStateAccepted {
			t.Fatalf("terminal observation %d = %v, want accepted", index+1, result.State())
		}
		settleResponderDelivery(t, &fixture, response, result)
		if index+1 == MaxTerminalObservationsPerAnchor {
			break
		}
		request := admitRemoteAdvance(t, fixture.origin, fixture.originRoute.PublicAlias,
			"operation:terminal-observation-bound-request-"+strconv.Itoa(index+2))
		requestReceipt := stageAndAdmitArtifactFreeDelivery(t, fixture.receiver,
			fixture.receiverRoute.RemotePeerID, fixture.originPrivate, request)
		fixture.settleOrigin(t, request, requestReceipt)
		response = admitTerminalDeclineFromCurrent(t, fixture.receiver)
	}
	if revision := terminalObservationRevision(t, fixture.origin, fixture.anchor); revision != MaxTerminalObservationsPerAnchor {
		t.Fatalf("observation revision = %d, want %d", revision, MaxTerminalObservationsPerAnchor)
	}
	request := admitRemoteAdvance(t, fixture.origin, fixture.originRoute.PublicAlias,
		"operation:terminal-observation-bound-request-65")
	requestReceipt := stageAndAdmitArtifactFreeDelivery(t, fixture.receiver,
		fixture.receiverRoute.RemotePeerID, fixture.originPrivate, request)
	fixture.settleOrigin(t, request, requestReceipt)
	response = admitTerminalDeclineFromCurrent(t, fixture.receiver)
	beforeEvents := countRows(t, fixture.origin.store, "events")
	requireTerminalReplyRejected(t, &fixture.peerRoundTripFixture, response)
	if countRows(t, fixture.origin.store, "events") != beforeEvents {
		t.Fatal("observation bound rejection committed an Event")
	}
}

func TestTerminalReplyObservationProjectsIntoFreshView(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	if result := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture,
		fixture.response); result.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal observation state = %v", result.State())
	}
	view := decodeFocusView(t, fixture.origin.current(t))
	if view.Current == nil || len(view.Related) != 1 ||
		view.Related[0].Facts.Relation != "terminal_reply" ||
		view.Related[0].Facts.Outcome != "declined" ||
		view.Outstanding.OpenTotal != 1 || view.Outstanding.RelatedTotal != 1 ||
		view.Outstanding.RelatedProjected != 1 {
		t.Fatalf("terminal observation View = %#v", view)
	}
}

func TestTerminalReplyObservationMakesPriorViewStale(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	oldView := fixture.origin.current(t)
	if result := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture,
		fixture.response); result.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal observation state = %v", result.State())
	}
	request := subjectRequest(t, oldView, "operation:stale-after-terminal-observation",
		agency.ConsequenceResolveUnresolved, "stale completion candidate", nil)
	result, err := fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Code() != rejectionStaleSubject {
		t.Fatalf("rejection code = %s, want %s", receipt.Code().String(), rejectionStaleSubject.String())
	}
	assertHandlingOpenByID(t, fixture.origin, fixture.anchor)
}

func TestOrdinaryPeerDeliveryStillCreatesHandling(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	before := countRows(t, fixture.receiver.store, "handlings")
	fixture.admitReceiver(t, delivery)
	if got := countRows(t, fixture.receiver.store, "handlings"); got != before+1 {
		t.Fatalf("ordinary peer delivery Handlings = %d, want %d", got, before+1)
	}
}

func TestCorrelatedTerminalReplyRejectsRevokedBoundRoute(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	bound, _, _ := bindImportedTerminalReply(t, &fixture,
		agency.ConsequenceResolveDeclined, "revoked-route")
	if _, err := fixture.receiver.store.RevokePeerRoute(fixture.receiver.ctx,
		fixture.receiverRoute.RouteID); err != nil {
		t.Fatal(err)
	}
	beforeEvents := countRows(t, fixture.receiver.store, "events")
	result, err := fixture.receiver.store.Admit(fixture.receiver.ctx,
		fixture.receiver.proof, bound)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Code() != rejectionStaleRoute {
		t.Fatalf("revoked reply route code = %s, want %s", receipt.Code().String(),
			rejectionStaleRoute.String())
	}
	if countRows(t, fixture.receiver.store, "events") != beforeEvents ||
		countRows(t, fixture.receiver.store, "peer_outbox") != 0 {
		t.Fatal("rejected reply changed Event or outbox state")
	}
	assertHandlingStateCount(t, fixture.receiver, "open", 1)
	replayed, err := fixture.receiver.store.Admit(fixture.receiver.ctx,
		fixture.receiver.proof, bound)
	if err != nil || !replayed.Replayed() ||
		replayed.ReceiptDigest() != result.ReceiptDigest() {
		t.Fatalf("rejected reply replay = %#v, %v", replayed, err)
	}
}

func TestCorrelatedTerminalReplyOutboxFaultRestoresResponderHandling(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	bound, requestDelivery, _ := bindImportedTerminalReply(t, &fixture,
		agency.ConsequenceResolveCompleted, "outbox-fault")
	before := snapshotP05Authority(t, fixture.receiver.store)
	drop := installP05Fault(t, fixture.receiver.store, `CREATE TEMP TRIGGER p05_fault
		AFTER INSERT ON peer_outbox
		BEGIN SELECT RAISE(ABORT, 'fault: correlated terminal reply outbox'); END`)
	if _, err := fixture.receiver.store.Admit(fixture.receiver.ctx,
		fixture.receiver.proof, bound); err == nil {
		t.Fatal("faulted terminal reply unexpectedly succeeded")
	}
	requireP05Snapshot(t, fixture.receiver.store, before)
	drop()
	requireExactAdmissionReplay(t, fixture.receiver, bound)
	assertHandlingOutcomeCount(t, fixture.receiver, "completed", 1)
	assertHandlingStateCount(t, fixture.receiver, "open", 0)
	response := requireOnePendingDelivery(t, fixture.receiver)
	requireOriginCorrelation(t, response, requestDelivery.OriginEvent())
}

func TestConcurrentTerminalReplyObservationsAcceptExactlyOne(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	second := rebuildTerminalObservationDelivery(t, fixture.originRoute.RouteID,
		fixture.response, focusEventRef(t, "event:concurrent-reply", "concurrent reply"),
		mustCorrelation(t, fixture.response), fixture.request.ID())
	for _, candidate := range []agency.PeerDelivery{fixture.response, second} {
		signature := ed25519.Sign(fixture.receiverPrivate, candidate.SigningMessage())
		staged, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
			fixture.originRoute.RemotePeerID, candidate.CanonicalJSON(), signature)
		if err != nil || staged.State() != PeerAdmissionStateStaged {
			t.Fatalf("stage concurrent candidate = %#v, %v", staged, err)
		}
	}
	var wg sync.WaitGroup
	states := make(chan PeerAdmissionState, 2)
	for _, candidate := range []agency.PeerDelivery{fixture.response, second} {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx, candidate.ID())
			if err != nil {
				states <- PeerAdmissionStateInvalid
				return
			}
			states <- result.State()
		}()
	}
	wg.Wait()
	close(states)
	accepted, rejected := 0, 0
	for state := range states {
		switch state {
		case PeerAdmissionStateAccepted:
			accepted++
		case PeerAdmissionStateRejected:
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("concurrent outcomes = accepted:%d rejected:%d", accepted, rejected)
	}
}

func TestTerminalReplyObservationFaultRollsBackEventAndInboxSettlement(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	signature := ed25519.Sign(fixture.receiverPrivate, fixture.response.SigningMessage())
	staged, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, fixture.response.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("stage terminal observation = %#v, %v", staged, err)
	}
	beforeEvents := countRows(t, fixture.origin.store, "events")
	if _, err := fixture.origin.store.db.Exec(`CREATE TEMP TRIGGER terminal_observation_fault
		AFTER UPDATE OF local_event_id ON peer_inbox
		WHEN NEW.local_event_id IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'fault: terminal observation settlement'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx,
		fixture.response.ID()); err == nil {
		t.Fatal("faulted terminal observation unexpectedly succeeded")
	}
	if countRows(t, fixture.origin.store, "events") != beforeEvents ||
		terminalObservationRevision(t, fixture.origin, fixture.anchor) != 0 {
		t.Fatal("faulted terminal observation left a partial Event or observation link")
	}
	var state string
	var localEvent any
	if err := fixture.origin.store.db.QueryRow(`SELECT state, local_event_id FROM peer_inbox
		WHERE delivery_id = ?`, fixture.response.ID().String()).Scan(&state, &localEvent); err != nil {
		t.Fatal(err)
	}
	if state != "staged" || localEvent != nil {
		t.Fatalf("faulted inbox = state:%s local_event:%v", state, localEvent)
	}
	assertHandlingOpenByID(t, fixture.origin, fixture.anchor)
	if _, err := fixture.origin.store.db.Exec(`DROP TRIGGER terminal_observation_fault`); err != nil {
		t.Fatal(err)
	}
	accepted, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx,
		fixture.response.ID())
	if err != nil || accepted.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal observation retry = %#v, %v", accepted, err)
	}
	if terminalObservationRevision(t, fixture.origin, fixture.anchor) != 1 {
		t.Fatal("terminal observation retry did not commit exactly one link")
	}
}

func TestTerminalReplyObservationRotationIsDeterministicAndBounded(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	firstResult := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture, fixture.response)
	if firstResult.State() != PeerAdmissionStateAccepted {
		t.Fatalf("first terminal observation = %v", firstResult.State())
	}
	settleResponderDelivery(t, &fixture, fixture.response, firstResult)
	secondRequest := admitRemoteAdvance(t, fixture.origin, fixture.originRoute.PublicAlias,
		"operation:second-terminal-observation-request")
	stageAndAdmitArtifactFreeDelivery(t, fixture.receiver,
		fixture.receiverRoute.RemotePeerID, fixture.originPrivate, secondRequest)
	secondResponse := admitTerminalDeclineFromCurrent(t, fixture.receiver)
	if result := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture,
		secondResponse); result.State() != PeerAdmissionStateAccepted {
		t.Fatalf("second terminal observation = %v", result.State())
	}

	firstView := decodeFocusView(t, fixture.origin.current(t))
	if len(firstView.Related) != 1 || firstView.Outstanding.RelatedTotal != 2 ||
		firstView.Outstanding.RelatedProjected != 1 || !firstView.Outstanding.Truncated ||
		firstView.Related[0].Facts.Relation != "terminal_reply" {
		t.Fatalf("first rotated observation View = %#v", firstView)
	}
	firstEvent := firstView.Related[0].Facts.Event
	replaceInteractiveBoundary(t, fixture.origin)
	secondView := decodeFocusView(t, fixture.origin.current(t))
	if len(secondView.Related) != 1 || secondView.Related[0].Facts.Event == firstEvent ||
		secondView.Outstanding.RelatedTotal != 2 ||
		secondView.Outstanding.RelatedProjected != 1 || !secondView.Outstanding.Truncated ||
		secondView.Related[0].Facts.Relation != "terminal_reply" {
		t.Fatalf("second rotated observation View = %#v", secondView)
	}
}

func rebuildTerminalObservationDelivery(t *testing.T, route agency.RouteID,
	base agency.PeerDelivery, origin, correlation agency.EventRef, inReplyTo agency.DeliveryID,
) agency.PeerDelivery {
	t.Helper()
	delivery, err := agency.NewPeerDelivery(route, agency.PeerDeliverySpec{
		OriginEvent: origin, OriginSequence: base.OriginSequence(),
		OriginAcceptedAt: base.OriginAcceptedAt(), OriginSource: base.OriginSource(),
		OriginConsequence: agency.ConsequenceResolveDeclined, OriginTargetCount: 1,
		OriginCausation: base.OriginCausation(), OriginCorrelation: correlation,
		InReplyToDelivery: inReplyTo, TargetAlias: base.TargetAlias(), Kind: base.Kind(),
		Payload: base.Payload(), Artifacts: base.Artifacts(), CausalDepth: base.CausalDepth(),
		ExpiresAt: base.ExpiresAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func bindImportedTerminalReply(t *testing.T, fixture *peerRoundTripFixture,
	consequence agency.Consequence, suffix string,
) (agency.BoundIntent, agency.PeerDelivery, string) {
	t.Helper()
	requestDelivery := fixture.admitOrigin(t)
	fixture.admitReceiver(t, requestDelivery)
	view := fixture.receiver.current(t)
	public := requireReplyContext(t, view,
		fixture.receiverRoute.PublicAlias.String(), "")
	target, err := agency.AliasTarget(mustHandle(t,
		public.Current.Facts.ReplyTarget))
	if err != nil {
		t.Fatal(err)
	}
	operation := mustOperation(t, "operation:terminal-reply-"+suffix)
	spec := agency.IntentSpec{
		Kind:              mustLabel(t, "opaque.disposition"),
		Payload:           mustPayload(t, "bounded "+suffix+" disposition"),
		Consequence:       consequence,
		SubjectHandling:   mustHandle(t, public.Current.Facts.Handle),
		Successors:        []agency.TargetRef{target},
		CorrelationHandle: mustHandle(t, public.Current.Facts.ReplyTo),
	}
	var candidates []agency.CapturedCandidate
	artifactContent := ""
	if consequence == agency.ConsequenceResolveCompleted {
		artifactContent = "verified terminal reply evidence " + suffix
		digest := fixture.receiver.catalog(t, artifactContent)
		input, err := agency.NewArtifactCandidate(mustHandle(t,
			"candidate:terminal-reply-"+suffix))
		if err != nil {
			t.Fatal(err)
		}
		spec.Artifacts = []agency.ArtifactInput{input}
		candidate, err := agency.NewCapturedCandidate(operation, input, digest)
		if err != nil {
			t.Fatal(err)
		}
		candidates = []agency.CapturedCandidate{candidate}
	}
	intent := mustIntent(t, spec)
	bound, err := view.Bind(intent, operation, candidates)
	if err != nil {
		t.Fatalf("Bind terminal reply: %v", err)
	}
	return bound, requestDelivery, artifactContent
}

func rebuildPeerDelivery(t *testing.T, route agency.RouteID, base agency.PeerDelivery,
	origin, correlation agency.EventRef, consequence agency.Consequence, targetCount uint8,
	kind agency.SemanticLabel,
) agency.PeerDelivery {
	t.Helper()
	delivery, err := agency.NewPeerDelivery(route, agency.PeerDeliverySpec{
		OriginEvent: origin, OriginSequence: base.OriginSequence(),
		OriginAcceptedAt: base.OriginAcceptedAt(), OriginSource: base.OriginSource(),
		OriginConsequence: consequence, OriginTargetCount: targetCount,
		OriginCausation: base.OriginCausation(), OriginCorrelation: correlation,
		TargetAlias: base.TargetAlias(), Kind: kind, Payload: base.Payload(),
		Artifacts: base.Artifacts(), CausalDepth: base.CausalDepth(), ExpiresAt: base.ExpiresAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func mustCorrelation(t *testing.T, delivery agency.PeerDelivery) agency.EventRef {
	t.Helper()
	correlation, present := delivery.OriginCorrelation()
	if !present {
		t.Fatal("delivery has no correlation")
	}
	return correlation
}

func mustDeliveryIDValue(t *testing.T, seed string) agency.DeliveryID {
	t.Helper()
	digest := agency.Sum([]byte(seed)).String()
	id, err := agency.ParseDeliveryID("delivery:" + digest[len("sha256:"):])
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func terminalObservationRevision(t *testing.T, fixture *authorityFixture,
	anchor string,
) uint64 {
	t.Helper()
	handling, err := agency.NewHandlingID(anchor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	revision, err := terminalObservationRevisionTx(fixture.ctx, tx, handling)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func settleResponderDelivery(t *testing.T, fixture *terminalObservationFixture,
	delivery agency.PeerDelivery, result PeerAdmissionResult,
) {
	t.Helper()
	receipt, present := result.Receipt()
	if !present {
		t.Fatal("accepted terminal observation has no peer Receipt")
	}
	signature := ed25519.Sign(fixture.originPrivate, receipt.SigningMessage())
	if _, _, err := fixture.receiver.store.SettlePeerDelivery(fixture.receiver.ctx,
		fixture.receiverRoute.RemotePeerID, delivery.ID(), receipt.CanonicalJSON(), signature); err != nil {
		t.Fatal(err)
	}
}

func stageAndAdmitPeerDelivery(t *testing.T, fixture *peerRoundTripFixture,
	delivery agency.PeerDelivery,
) PeerAdmissionResult {
	t.Helper()
	signature := ed25519.Sign(fixture.receiverPrivate, delivery.SigningMessage())
	staged, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("stage terminal reply = %#v, %v", staged, err)
	}
	result, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx, delivery.ID())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireTerminalReplyRejected(t *testing.T, fixture *peerRoundTripFixture,
	delivery agency.PeerDelivery,
) {
	t.Helper()
	beforeEvents := countRows(t, fixture.origin.store, "events")
	result := stageAndAdmitPeerDelivery(t, fixture, delivery)
	if result.State() != PeerAdmissionStateRejected {
		t.Fatalf("terminal reply state = %v, want rejected", result.State())
	}
	if got := countRows(t, fixture.origin.store, "events"); got != beforeEvents {
		t.Fatalf("rejected terminal reply created %d Events; want %d", got, beforeEvents)
	}
}

func closeCurrentLocally(t *testing.T, fixture *authorityFixture, operation string) {
	t.Helper()
	view := fixture.current(t)
	request := subjectRequest(t, view, operation, agency.ConsequenceResolveUnresolved,
		"local responsibility is closed", nil)
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
}

func requireOnlyHandlingID(t *testing.T, fixture *authorityFixture) string {
	t.Helper()
	var id string
	if err := fixture.store.db.QueryRow(`SELECT handling_id FROM handlings`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertHandlingOpenByID(t *testing.T, fixture *authorityFixture, id string) {
	t.Helper()
	var state string
	var outcome any
	if err := fixture.store.db.QueryRow(`SELECT state, outcome FROM handlings
		WHERE handling_id = ?`, id).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "open" || outcome != nil {
		t.Fatalf("origin anchor %s = state:%s outcome:%v", id, state, outcome)
	}
}

func verifiedPeerEffectFixture(t *testing.T, origin agency.Consequence,
	reply, withArtifact bool,
) agency.VerifiedPeerDelivery {
	t.Helper()
	now := time.Date(2026, 8, 3, 4, 5, 6, 7, time.UTC)
	route := mustRoute(t, "route:decided-peer-effect:"+origin.String())
	spec := agency.PeerDeliverySpec{
		OriginEvent:    mustEventRef(t, "event:decided-peer-effect:"+origin.String(), "origin"),
		OriginSequence: 1, OriginAcceptedAt: now, OriginSource: mustPrincipal(t, "agent:origin"),
		OriginConsequence: origin, OriginTargetCount: 2,
		TargetAlias: mustHandle(t, "agent/target"), Kind: mustLabel(t, "work.request"),
		Payload: mustPayload(t, "Bounded peer candidate."), CausalDepth: 1,
		ExpiresAt: now.Add(time.Hour),
	}
	if reply {
		spec.OriginTargetCount = 1
		spec.OriginCorrelation = mustEventRef(t, "event:decided-request", "request")
		spec.InReplyToDelivery = mustDeliveryIDValue(t, "decided-request")
	}
	if withArtifact {
		spec.Artifacts = []agency.Digest{agency.Sum([]byte("verified completion"))}
	}
	delivery, err := agency.NewPeerDelivery(route, spec)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := agency.ParsePeerDeliveryCanonicalJSON(delivery.CanonicalJSON(), route)
	if err != nil {
		t.Fatal(err)
	}
	verifiedArtifacts := make([]agency.VerifiedPeerArtifact, 0, len(delivery.Artifacts()))
	for _, digest := range delivery.Artifacts() {
		artifact, err := agency.NewVerifiedPeerArtifact(digest, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		verifiedArtifacts = append(verifiedArtifacts, artifact)
	}
	verified, err := agency.NewVerifiedPeerDelivery(parsed, mustPrincipal(t, "peer:source"),
		mustPrincipal(t, "agent:local-target"), verifiedArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}
