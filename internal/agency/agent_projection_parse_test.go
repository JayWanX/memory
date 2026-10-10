package agency

import (
	"bytes"
	"testing"
	"time"
)

func TestAgentProjectionParsersRoundTripPublicBytes(t *testing.T) {
	view := newViewParserFixture(t).public
	if err := ValidateAgentViewProjectionCanonicalJSON(view.CanonicalJSON()); err != nil {
		t.Fatalf("ValidateAgentViewProjectionCanonicalJSON() error = %v", err)
	}

	request := mustBoundRoot(t, "operation:parse-agent-projection")
	event, err := NewEvent(request, EventStamp{ID: mustEventID(t, "event:parse-agent-projection"),
		AcceptedAt: testTime, OriginSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	private, err := NewAcceptedReceipt(request, event, testTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	projected, err := ProjectAgentReceipt(private, true)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAgentReceiptProjectionCanonicalJSON(projected.CanonicalJSON())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.CanonicalJSON(), projected.CanonicalJSON()) ||
		parsed.Outcome() != projected.Outcome() || parsed.Replayed() != projected.Replayed() {
		t.Fatal("parsed Agent Receipt projection differs from original")
	}
}

func TestAgentProjectionParsersRejectNoncanonicalOrInvalidBytes(t *testing.T) {
	if err := ValidateAgentViewProjectionCanonicalJSON(
		[]byte(`{"schema":"memory.agent.view","version":8,"view":"view:test","allowed_intents":[],"schema":"memory.agent.view"}`),
	); err == nil {
		t.Fatal("duplicate View projection key was accepted")
	}
	if _, err := ParseAgentReceiptProjectionCanonicalJSON(
		[]byte(`{"schema":"memory.agent.receipt","version":1,"outcome":"accepted","replayed":false,"diagnostic":"forged"}`),
	); err == nil {
		t.Fatal("accepted Agent Receipt diagnostic was accepted")
	}
}
