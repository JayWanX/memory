package authority

import (
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestPendingReplyObservationUsesAnchorIndex(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	rows, err := fixture.origin.store.db.Query(`EXPLAIN QUERY PLAN SELECT EXISTS(
		SELECT 1 FROM peer_outbox outbound
		WHERE outbound.reply_anchor_handling_id = ?
		AND NOT EXISTS(
			SELECT 1 FROM peer_inbox inbox
			WHERE inbox.in_reply_to_delivery_id = outbound.delivery_id
			AND inbox.local_event_id IS NOT NULL))`, "handling:index-oracle")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		found = found || strings.Contains(detail,
			"USING COVERING INDEX peer_outbox_reply_anchor (reply_anchor_handling_id=?)")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("pending reply observation query did not use its bounded anchor index")
	}
}

func TestCurrentProjectsPendingReplyObservationAfterRemoteRoot(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	fixture.admitOrigin(t)

	view := decodeFocusView(t, fixture.origin.current(t))
	if view.Current == nil || !view.Current.Facts.ReplyObservationPending ||
		view.Current.Facts.ReplyRequired || view.Current.Facts.ReplyTarget != "" {
		t.Fatalf("origin reply observation projection = %#v", view.Current)
	}
}

func TestPendingReplyObservationSurvivesDeliverySettlementAndLocalAdvance(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	receipt := fixture.admitReceiver(t, delivery)
	fixture.settleOrigin(t, delivery, receipt)

	view := fixture.origin.current(t)
	if current := decodeFocusView(t, view).Current; current == nil || !current.Facts.ReplyObservationPending {
		t.Fatalf("settled delivery lost pending reply observation: %#v", current)
	}
	advance := subjectRequest(t, view, "operation:advance-pending-reply",
		agency.ConsequenceAdvanceHandling, "independent local progress", nil)
	result, err := fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, advance)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)

	continued := decodeFocusView(t, fixture.origin.current(t))
	if continued.Current == nil || !continued.Current.Facts.ReplyObservationPending {
		t.Fatalf("local advance lost pending reply observation: %#v", continued.Current)
	}
}

func TestPendingReplyObservationClearsAfterExactTerminalObservation(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	before := decodeFocusView(t, fixture.origin.current(t))
	if before.Current == nil || !before.Current.Facts.ReplyObservationPending {
		t.Fatalf("origin omitted pending reply observation: %#v", before.Current)
	}

	result := stageAndAdmitPeerDelivery(t, &fixture.peerRoundTripFixture, fixture.response)
	if result.State() != PeerAdmissionStateAccepted {
		t.Fatalf("terminal observation state = %v, want accepted", result.State())
	}
	after := decodeFocusView(t, fixture.origin.current(t))
	if after.Current == nil || after.Current.Facts.ReplyObservationPending {
		t.Fatalf("accepted terminal observation remained pending: %#v", after.Current)
	}
	if len(after.Related) != 1 || after.Related[0].Facts.Relation != "terminal_reply" {
		t.Fatalf("terminal observation projection = %#v", after.Related)
	}
}

func TestExplicitResolveRemainsLegalWithPendingReplyObservation(t *testing.T) {
	fixture := newTerminalObservationFixture(t)
	view := decodeFocusView(t, fixture.origin.current(t))
	if view.Current == nil || !view.Current.Facts.ReplyObservationPending {
		t.Fatalf("origin omitted pending reply observation: %#v", view.Current)
	}

	closeCurrentLocally(t, fixture.origin, "operation:explicit-close-with-pending-reply")
	requireTerminalReplyRejected(t, &fixture.peerRoundTripFixture, fixture.response)
}
