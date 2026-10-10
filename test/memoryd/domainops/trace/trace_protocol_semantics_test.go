package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestEvolutionTraceIsNotApplicableWithoutAClaim(t *testing.T) {
	summary := evolutionSummary{}
	facts, err := evolutionEffectFacts(summary, nil)
	if err != nil {
		t.Fatalf("evolutionEffectFacts() rejected an absent evolution claim: %v", err)
	}
	if len(facts) != 0 || evolutionGateStatus(false) != "not_applicable" {
		t.Fatalf("absent evolution observation = facts %v, gate %q",
			facts, evolutionGateStatus(false))
	}

	summary.AcceptedReferenceUses = 1
	if _, err := evolutionEffectFacts(summary, nil); err == nil {
		t.Fatal("evolutionEffectFacts() accepted an unproved Reference-use claim")
	}
}

func TestFailureTracePreservesProvedProtocolGates(t *testing.T) {
	report := validFailureReport()
	scenario := scenarioEvidence{Digest: agency.Sum([]byte("failed protocol proof")).String()}
	nodes := failedProtocolEvidence()
	var output bytes.Buffer
	if err := writeFailureTrace(&output, report, scenario, nodes); err != nil {
		t.Fatalf("writeFailureTrace(authority evidence) error = %v", err)
	}
	trace := output.String()
	assertTraceGate(t, trace, "scenario.run", "fail", false)
	assertTraceGate(t, trace, "r7.operation-receipts", "pass", true)
	assertTraceGate(t, trace, "r7.peer-accepted-effect", "pass", true)
}

func failedProtocolEvidence() []nodeEvidence {
	acceptedAt := time.Date(2026, 8, 4, 1, 0, 10, 0, time.UTC)
	root := eventEvidence{Node: "lead", ID: "event:failed-root-proof",
		Digest:     agency.Sum([]byte("failed root proof Event")).String(),
		AcceptedAt: acceptedAt, OriginSequence: 1, SourcePrincipal: "principal:lead",
		SemanticKind: "opaque.request", Consequence: "handling.create"}
	remote := eventEvidence{Node: "data", ID: "event:failed-remote-proof",
		Digest:     agency.Sum([]byte("failed remote proof Event")).String(),
		AcceptedAt: acceptedAt.Add(2 * time.Second), OriginSequence: 1,
		SourcePrincipal: "principal:lead-surrogate", SemanticKind: "opaque.request",
		Consequence: "handling.create", CausalDepth: 1,
		Causation: []eventRefWire{{ID: root.ID, Digest: root.Digest}}}
	return []nodeEvidence{
		{Role: "lead", Events: []eventEvidence{root},
			Handlings: []handlingEvidence{{Node: "lead", ID: "handling:failed-root-proof",
				TargetPrincipal: "principal:lead", HeadEventID: root.ID, State: "open",
				CreatedSequence: 1}},
			Operations: []operationEvidence{{Node: "lead",
				Digest:  agency.Sum([]byte("failed but durable receipt")).String(),
				Outcome: "accepted", RecordedAt: acceptedAt.Add(time.Second),
				EventID: root.ID, EventDigest: root.Digest}}},
		{Role: "data", Events: []eventEvidence{remote},
			Handlings: []handlingEvidence{{Node: "data", ID: "handling:failed-remote-proof",
				TargetPrincipal: "principal:data", HeadEventID: remote.ID, State: "open",
				CreatedSequence: 1}},
			Deliveries: []deliveryEvidence{{Node: "data", Direction: "inbox",
				ID: "delivery:" + strings.Repeat("b", 64), State: "settled", Accepted: true,
				CapturedAt: remote.AcceptedAt.Add(time.Second), OriginEventID: root.ID,
				OriginEventDigest: root.Digest, LocalEventID: remote.ID,
				LocalEventDigest: remote.Digest}}},
	}
}

type testTraceRecord struct {
	Record string   `json:"record"`
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Causes []string `json:"causes"`
	Facts  struct {
		GateID string `json:"gate_id"`
		Status string `json:"status"`
	} `json:"facts"`
	Gates []struct {
		ID       string   `json:"id"`
		Status   string   `json:"status"`
		Evidence []string `json:"evidence"`
	} `json:"gates"`
}

func assertSuccessfulAttentionEvidence(t *testing.T, trace string) {
	t.Helper()
	outcomeFacts := make(map[string]struct{})
	var recoveryCauses []string
	for _, record := range decodeTestTrace(t, trace) {
		if record.Kind == "test.attention.outcome" {
			outcomeFacts[record.ID] = struct{}{}
		}
		if record.Kind == "test.gate.checked" && record.Facts.GateID == "scenario.recovery" {
			recoveryCauses = record.Causes
		}
	}
	if len(outcomeFacts) != 2*len(domainRoles) || len(recoveryCauses) != len(outcomeFacts) {
		t.Fatalf("attention outcomes = %d, recovery causes = %d",
			len(outcomeFacts), len(recoveryCauses))
	}
	for _, cause := range recoveryCauses {
		if _, exists := outcomeFacts[cause]; !exists {
			t.Fatalf("scenario recovery cites non-attention cause %q", cause)
		}
	}
}

func traceGateStatus(t *testing.T, trace, gateID string) string {
	t.Helper()
	for _, record := range decodeTestTrace(t, trace) {
		if record.Record != "result" {
			continue
		}
		for _, gate := range record.Gates {
			if gate.ID == gateID {
				return gate.Status
			}
		}
	}
	t.Fatalf("trace result omits gate %q", gateID)
	return ""
}

func assertTraceGate(t *testing.T, trace, gateID, status string, needsEvidence bool) {
	t.Helper()
	for _, record := range decodeTestTrace(t, trace) {
		if record.Record != "result" {
			continue
		}
		for _, gate := range record.Gates {
			if gate.ID != gateID {
				continue
			}
			if gate.Status != status || needsEvidence && len(gate.Evidence) == 0 {
				t.Fatalf("gate %q = status %q, evidence %d", gateID,
					gate.Status, len(gate.Evidence))
			}
			return
		}
	}
	t.Fatalf("trace result omits gate %q", gateID)
}

func decodeTestTrace(t *testing.T, trace string) []testTraceRecord {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(trace), "\n")
	records := make([]testTraceRecord, 0, len(lines))
	for _, line := range lines {
		var record testTraceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}
