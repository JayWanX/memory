package agency

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProjectAgentReceiptDoesNotLeakPrivateReceiptAuthority(t *testing.T) {
	request := mustBoundRoot(t, "operation:private-never-project")
	event, err := NewEvent(request, EventStamp{
		ID: mustEventID(t, "event:private-never-project"), AcceptedAt: testTime, OriginSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	privateReceipt, err := NewAcceptedReceipt(request, event, testTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	agentReceipt, err := ProjectAgentReceipt(privateReceipt, false)
	if err != nil {
		t.Fatal(err)
	}
	public := agentReceipt.CanonicalJSON()
	for name, private := range map[string]string{
		"operation key":  request.OperationKey().String(),
		"request digest": request.RequestDigest().String(),
		"Event ID":       event.ID().String(),
		"Event digest":   event.Digest().String(),
		"recorded time":  privateReceipt.RecordedAt().Format(time.RFC3339Nano),
	} {
		if bytes.Contains(public, []byte(private)) {
			t.Fatalf("Agent Receipt leaks %s: %s", name, public)
		}
	}
	if got, want := string(public),
		`{"schema":"memory.agent.receipt","version":1,"outcome":"accepted","replayed":false}`; got != want {
		t.Fatalf("Agent Receipt = %s, want %s", got, want)
	}
	if len(public) > MaxAgentReceiptCanonicalBytes {
		t.Fatalf("Agent Receipt bytes = %d, maximum %d", len(public), MaxAgentReceiptCanonicalBytes)
	}
}

func TestProjectAgentReceiptRepresentsRejectionAndReplayOnlyAsPublicMetadata(t *testing.T) {
	request := mustBoundRoot(t, "operation:receipt-projections")
	rejected, err := NewRejectedReceipt(request, mustLabel(t, "stale.authority"),
		"The offered responsibility changed.", testTime)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := ProjectAgentReceipt(rejected, true)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Outcome() != ReceiptOutcomeRejected || !projected.Replayed() ||
		projected.Diagnostic() != "The offered responsibility changed." {
		t.Fatalf("rejected Agent Receipt = %#v", projected)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(projected.CanonicalJSON(), &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["replayed"]) != "true" || len(wire["diagnostic"]) == 0 {
		t.Fatalf("replayed rejection projection = %s", projected.CanonicalJSON())
	}
	allowed := map[string]struct{}{
		"schema": {}, "version": {}, "outcome": {}, "replayed": {}, "diagnostic": {},
	}
	if len(wire) != len(allowed) {
		t.Fatalf("Agent Receipt exposes an unexpected field: %s", projected.CanonicalJSON())
	}
	for field := range wire {
		if _, public := allowed[field]; !public {
			t.Fatalf("Agent Receipt exposes private field %q: %s", field, projected.CanonicalJSON())
		}
	}
}

func TestAgentReceiptAndPrivateReceiptEnforceCanonicalByteBounds(t *testing.T) {
	request := mustBoundRoot(t, "operation:max-receipt")
	diagnostic := strings.Repeat("\x01", MaxDiagnosticBytes)
	receipt, err := NewRejectedReceipt(request, mustLabel(t, strings.Repeat("a", MaxSemanticLabelBytes)),
		diagnostic, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.CanonicalJSON()) > MaxReceiptCanonicalBytes {
		t.Fatalf("private Receipt bytes = %d, maximum %d", len(receipt.CanonicalJSON()), MaxReceiptCanonicalBytes)
	}
	projected, err := ProjectAgentReceipt(receipt, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.CanonicalJSON()) > MaxAgentReceiptCanonicalBytes {
		t.Fatalf("Agent Receipt bytes = %d, maximum %d", len(projected.CanonicalJSON()),
			MaxAgentReceiptCanonicalBytes)
	}
	tooLarge := bytes.Repeat([]byte{' '}, MaxReceiptCanonicalBytes+1)
	if _, err := ParseReceiptCanonicalJSON(tooLarge); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized Receipt error = %v, want ErrLimit", err)
	}
}
