package observer

import (
	"slices"
	"testing"
)

func factEvidenceInput(fact factRecord) Fact {
	return Fact{Kind: fact.Kind, Causes: slices.Clone(fact.Causes), References: References{
		Artifact: fact.Refs.Artifact, Correlation: fact.Refs.Correlation,
		Delivery: fact.Refs.Delivery, Event: fact.Refs.Event,
		EventDigest: fact.Refs.EventDigest, Handling: fact.Refs.Handling,
		Principal: fact.Refs.Principal, ReferenceHead: fact.Refs.ReferenceHead,
	}, Fields: FactFields{
		Action:        fact.Facts.Action,
		ArtifactCount: fact.Facts.ArtifactCount, AttemptCount: fact.Facts.AttemptCount,
		BatchedCount: fact.Facts.BatchedCount, Authenticated: fact.Facts.Authenticated,
		BypassedHook: fact.Facts.BypassedHook, ByteSize: fact.Facts.ByteSize,
		Code: fact.Facts.Code, Count: fact.Facts.Count, Consequence: fact.Facts.Consequence,
		DurationMillis: fact.Facts.DurationMillis, Episode: fact.Facts.Episode,
		GateID: fact.Facts.GateID, GoalDigest: fact.Facts.GoalDigest,
		GoalSatisfied: fact.Facts.GoalSatisfied, HasCurrent: fact.Facts.HasCurrent,
		HookCue: fact.Facts.HookCue, InvalidCount: fact.Facts.InvalidCount,
		OccupiedClaims: fact.Facts.OccupiedClaims,
		OpenTotal:      fact.Facts.OpenTotal, OpenUnclaimed: fact.Facts.OpenUnclaimed,
		Outcome: fact.Facts.Outcome, PayloadBytes: fact.Facts.PayloadBytes,
		Replayed: fact.Facts.Replayed, ReplyRequired: fact.Facts.ReplyRequired,
		RelatedProjected: fact.Facts.RelatedProjected,
		RelatedTotal:     fact.Facts.RelatedTotal, Role: fact.Facts.Role,
		Round:        fact.Facts.Round,
		SemanticKind: fact.Facts.SemanticKind, State: fact.Facts.State,
		Status: fact.Facts.Status, SuccessCount: fact.Facts.SuccessCount,
		TargetCount: fact.Facts.TargetCount, ToolErrorCount: fact.Facts.ToolErrorCount,
		Targets: slices.Clone(fact.Facts.Targets), TimedOut: fact.Facts.TimedOut,
		Truncated: fact.Facts.Truncated, TurnLimit: fact.Facts.TurnLimit,
		TurnsUsed: fact.Facts.TurnsUsed, ViewNonempty: fact.Facts.ViewNonempty,
	}}
}

func TestRuntimeObservationEvidenceIsClosed(t *testing.T) {
	attempts, successes, toolErrors, invalidResults, batched, count := 2, 1, 1, 0, 0, 1
	domain := Fact{Kind: "runtime.domain.operation", Fields: FactFields{
		Action: "mutation", AttemptCount: &attempts, SuccessCount: &successes,
		ToolErrorCount: &toolErrors, InvalidCount: &invalidResults, BatchedCount: &batched,
	}}
	if err := validateKindEvidence(domain, 1); err != nil {
		t.Fatalf("valid domain observation: %v", err)
	}
	domain.Causes = []string{"trace:not-allowed"}
	if err := validateKindEvidence(domain, 1); err == nil {
		t.Fatal("domain observation accepted a causal edge")
	}
	domain.Causes = nil
	domain.Fields.ToolErrorCount = &attempts
	if err := validateKindEvidence(domain, 1); err == nil {
		t.Fatal("domain observation accepted unbalanced outcome classes")
	}

	denial := Fact{Kind: "runtime.intent.denied", Fields: FactFields{
		Action: "submit", Code: "authentication_failed", Count: &count,
	}}
	if err := validateKindEvidence(denial, 2); err != nil {
		t.Fatalf("valid Intent denial: %v", err)
	}
	denial.Fields.Code = "provider-prose"
	if err := validateKindEvidence(denial, 2); err == nil {
		t.Fatal("Intent denial accepted an open diagnostic class")
	}
}

func TestRuntimeViewEvidenceRequiresConsistentStructuralMetadata(t *testing.T) {
	hasCurrent, replyRequired, truncated := true, true, true
	openTotal, relatedTotal, relatedProjected := 1, 65, 1
	view := Fact{Kind: "runtime.view.received", Fields: FactFields{
		Action: "current", HasCurrent: &hasCurrent, ReplyRequired: &replyRequired,
		OpenTotal: &openTotal, RelatedTotal: &relatedTotal,
		RelatedProjected: &relatedProjected, Truncated: &truncated,
	}}
	if err := validateKindEvidence(view, 1); err != nil {
		t.Fatalf("valid Agent View structure: %v", err)
	}
	view.Fields.ReplyRequired = nil
	if err := validateKindEvidence(view, 1); err == nil {
		t.Fatal("current Agent View omitted reply-required structure")
	}
	view.Fields.ReplyRequired = &replyRequired
	*view.Fields.RelatedProjected = 2
	if err := validateKindEvidence(view, 1); err == nil {
		t.Fatal("Agent View exceeded the related projection bound")
	}
}

func TestFactEvidenceInputCarriesClosedDomainOutcomes(t *testing.T) {
	attempts, successes, toolErrors, invalid, batched := 4, 1, 1, 1, 1
	fact := factRecord{Sequence: 1, Kind: "runtime.domain.operation", Facts: factsWire{
		Action: "read", AttemptCount: &attempts, SuccessCount: &successes,
		ToolErrorCount: &toolErrors, InvalidCount: &invalid, BatchedCount: &batched,
	}}
	projected := factEvidenceInput(fact)
	if projected.Fields.ToolErrorCount == nil || *projected.Fields.ToolErrorCount != 1 ||
		projected.Fields.InvalidCount == nil || *projected.Fields.InvalidCount != 1 ||
		projected.Fields.BatchedCount == nil || *projected.Fields.BatchedCount != 1 {
		t.Fatalf("domain outcome mapping = %#v", projected.Fields)
	}
	if err := validateKindEvidence(projected, fact.Sequence); err != nil {
		t.Fatalf("mapped domain observation: %v", err)
	}
}
