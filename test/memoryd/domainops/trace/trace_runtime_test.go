package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/test/memoryd/observer"
)

type projectedRuntimeFact struct {
	Record string   `json:"record"`
	Kind   string   `json:"kind"`
	Causes []string `json:"causes"`
	Facts  struct {
		Action           string `json:"action"`
		Code             string `json:"code"`
		Count            *int   `json:"count"`
		AttemptCount     *int   `json:"attempt_count"`
		SuccessCount     *int   `json:"success_count"`
		ToolErrorCount   *int   `json:"tool_error_count"`
		InvalidCount     *int   `json:"invalid_result_count"`
		BatchedCount     *int   `json:"batched_unattributed_count"`
		HasCurrent       *bool  `json:"has_current"`
		ReplyRequired    *bool  `json:"reply_required"`
		OpenTotal        *int   `json:"open_total"`
		RelatedTotal     *int   `json:"related_total"`
		RelatedProjected *int   `json:"related_projected"`
		Truncated        *bool  `json:"truncated"`
	} `json:"facts"`
}

func projectTestRuntime(t *testing.T) string {
	t.Helper()
	started := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	var output bytes.Buffer
	writer, err := observer.NewWriter(&output, observer.Run{
		ID: "runtime-edge-test",
		Scenario: observer.Scenario{ID: "runtime-edge-test",
			Digest: agency.Sum([]byte("runtime-edge-test")).String()},
		StartedAt:    started,
		Participants: []observer.Participant{{Node: "lead", Agent: "lead", Runtime: "pi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn := turnSummary{Role: "lead", Turn: "initial-lead", HookCues: 1,
		CapturedAt: started.Add(time.Minute).Format(time.RFC3339Nano), BashCalls: 4,
		DomainOperations: domainOperationsSummary{
			Read:     domainOperationSummary{Attempts: 2, Successes: 2},
			Mutation: domainOperationSummary{Attempts: 1, ToolErrors: 1},
		},
		DelegateCalls: 1, CurrentReads: 1, View: &agentViewSummary{
			HasCurrent: true, ReplyRequired: boolPointer(true), ReplyPending: boolPointer(false), OpenTotal: 1,
			RelatedTotal: 65, RelatedProjected: 1, Truncated: true,
		}, SubmitAttempts: 2, IntentSubmits: 1,
		SubmitDenials: 1, SubmitControlDenials: []controlDenial{{
			Code: "authentication_failed", Count: 1,
		}},
		AgentEnd: true}
	if err := appendRuntimeFacts(writer, []turnSummary{turn}); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestRuntimeProjectionIncludesAgentViewStructure(t *testing.T) {
	facts := decodeProjectedRuntimeFacts(t, projectTestRuntime(t))
	view, found := findProjectedRuntimeFact(facts, "runtime.view.received", "current")
	if !found || view.Facts.HasCurrent == nil || !*view.Facts.HasCurrent ||
		view.Facts.ReplyRequired == nil || !*view.Facts.ReplyRequired ||
		view.Facts.OpenTotal == nil || *view.Facts.OpenTotal != 1 ||
		view.Facts.RelatedTotal == nil || *view.Facts.RelatedTotal != 65 ||
		view.Facts.RelatedProjected == nil || *view.Facts.RelatedProjected != 1 ||
		view.Facts.Truncated == nil || !*view.Facts.Truncated {
		t.Fatalf("Agent View structural projection = %+v", view.Facts)
	}
}

func decodeProjectedRuntimeFacts(t *testing.T, output string) []projectedRuntimeFact {
	t.Helper()
	var facts []projectedRuntimeFact
	for _, line := range strings.Split(strings.TrimSpace(output), "\n")[1:] {
		var record projectedRuntimeFact
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		facts = append(facts, record)
	}
	return facts
}

func findProjectedRuntimeFact(facts []projectedRuntimeFact, kind, action string) (projectedRuntimeFact, bool) {
	for _, fact := range facts {
		if fact.Kind == kind && fact.Facts.Action == action {
			return fact, true
		}
	}
	return projectedRuntimeFact{}, false
}

func TestRuntimeProjectionAddsNoCausalEdges(t *testing.T) {
	for _, fact := range decodeProjectedRuntimeFacts(t, projectTestRuntime(t)) {
		if fact.Record == "fact" && len(fact.Causes) != 0 {
			t.Fatalf("runtime observation inferred causes: %v", fact.Causes)
		}
	}
}

func TestRuntimeProjectionIncludesBoundedOperationAndDenialCounts(t *testing.T) {
	facts := decodeProjectedRuntimeFacts(t, projectTestRuntime(t))
	read, foundRead := findProjectedRuntimeFact(facts, "runtime.domain.operation", "read")
	mutation, foundMutation := findProjectedRuntimeFact(facts, "runtime.domain.operation", "mutation")
	denial, foundDenial := findProjectedRuntimeFact(facts, "runtime.intent.denied", "submit")
	_, foundDelegate := findProjectedRuntimeFact(facts, "runtime.delegate.invoked", "")
	if !foundDelegate || !foundRead || !foundMutation || !foundDenial {
		t.Fatalf("missing observations: delegate=%t read=%t mutation=%t denial=%t",
			foundDelegate, foundRead, foundMutation, foundDenial)
	}
	assertRuntimeCount(t, "read attempts", read.Facts.AttemptCount, 2)
	assertRuntimeCount(t, "read successes", read.Facts.SuccessCount, 2)
	assertRuntimeCount(t, "mutation attempts", mutation.Facts.AttemptCount, 1)
	assertRuntimeCount(t, "mutation successes", mutation.Facts.SuccessCount, 0)
	assertRuntimeCount(t, "mutation tool errors", mutation.Facts.ToolErrorCount, 1)
	assertRuntimeCount(t, "mutation invalid results", mutation.Facts.InvalidCount, 0)
	assertRuntimeCount(t, "mutation batched calls", mutation.Facts.BatchedCount, 0)
	if denial.Facts.Code != "authentication_failed" || denial.Facts.Count == nil ||
		*denial.Facts.Count != 1 {
		t.Fatalf("denial observation = %+v", denial.Facts)
	}
}

func assertRuntimeCount(t *testing.T, label string, actual *int, expected int) {
	t.Helper()
	if actual == nil || *actual != expected {
		t.Fatalf("%s = %v, want %d", label, actual, expected)
	}
}

func TestRuntimeProjectionOmitsUnsafeContent(t *testing.T) {
	output := projectTestRuntime(t)
	for _, forbidden := range []string{"message", "https://private.example", "/admin/void",
		"provider prose", "attachment_credential"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("runtime projection retained forbidden content %q", forbidden)
		}
	}
}

func TestTurnSummaryFailsClosedOnUnsafeObservationCounts(t *testing.T) {
	valid := turnSummary{Role: "data", Turn: "turn-a",
		CapturedAt: "2026-08-04T01:01:00Z", HookCues: 1, BashCalls: 2,
		DomainOperations: domainOperationsSummary{
			Mutation: domainOperationSummary{Attempts: 1, Successes: 1},
		},
		SubmitAttempts: 1, SubmitDenials: 1,
		SubmitControlDenials: []controlDenial{{Code: "authentication_failed", Count: 1}},
		AgentEnd:             true}
	if err := validateTurnSummary(valid); err != nil {
		t.Fatalf("valid bounded observations: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*turnSummary)
	}{
		{"success without attempt", func(turn *turnSummary) {
			turn.DomainOperations.Mutation.Successes = 2
		}},
		{"unclassified operation", func(turn *turnSummary) {
			turn.DomainOperations.Mutation.Successes = 0
		}},
		{"unclassified denial", func(turn *turnSummary) {
			turn.SubmitControlDenials = nil
		}},
		{"open denial code", func(turn *turnSummary) {
			turn.SubmitControlDenials[0].Code = "provider-prose"
		}},
		{"duplicate denial code", func(turn *turnSummary) {
			turn.SubmitDenials = 2
			turn.SubmitControlDenials = append(turn.SubmitControlDenials,
				controlDenial{Code: "authentication_failed", Count: 1})
		}},
		{"unsorted denial codes", func(turn *turnSummary) {
			turn.SubmitDenials = 2
			turn.SubmitControlDenials = []controlDenial{
				{Code: "context_required", Count: 1},
				{Code: "action_not_allowed", Count: 1},
			}
		}},
		{"view without read", func(turn *turnSummary) {
			turn.View = &agentViewSummary{}
		}},
		{"read without view", func(turn *turnSummary) {
			turn.CurrentReads = 1
			turn.BashCalls = 3
		}},
		{"current view without reply structure", func(turn *turnSummary) {
			turn.CurrentReads = 1
			turn.BashCalls = 3
			turn.View = &agentViewSummary{HasCurrent: true, OpenTotal: 1}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.SubmitControlDenials = slices.Clone(valid.SubmitControlDenials)
			test.mutate(&candidate)
			if err := validateTurnSummary(candidate); err == nil {
				t.Fatal("validateTurnSummary() accepted unsafe observations")
			}
		})
	}
}

func TestTurnSummaryAcceptsNativeCurrentWithoutBash(t *testing.T) {
	turn := turnSummary{Role: "lead", Turn: "native-current-only",
		CapturedAt: "2026-08-04T01:01:00Z", HookCues: 1, CurrentReads: 1,
		View: &agentViewSummary{}, AgentEnd: true}
	if err := validateTurnSummary(turn); err != nil {
		t.Fatalf("validateTurnSummary() rejected native Current without Bash: %v", err)
	}
}

func TestAppendEventFactsProjectsExpectedReferenceHead(t *testing.T) {
	started := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	var output bytes.Buffer
	writer, err := observer.NewWriter(&output, observer.Run{
		ID: "expected-reference-test",
		Scenario: observer.Scenario{ID: "expected-reference-test",
			Digest: agency.Sum([]byte("expected-reference-test")).String()},
		StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	referenceDigest := agency.Sum([]byte("reference head")).String()
	nodes := []nodeEvidence{{Role: "lead", Events: []eventEvidence{
		{Node: "lead", ID: "event:reference", Digest: referenceDigest,
			AcceptedAt: started.Add(time.Second), OriginSequence: 1,
			SourcePrincipal: "principal:lead", SemanticKind: "knowledge.keep",
			Consequence: "reference.publish"},
		{Node: "lead", ID: "event:supersede", Digest: agency.Sum([]byte("supersede")).String(),
			AcceptedAt: started.Add(2 * time.Second), OriginSequence: 2,
			SourcePrincipal: "principal:lead", SemanticKind: "knowledge.refine",
			Consequence: "reference.supersede", ReferenceHead: "event:reference",
			ReferenceDigest: referenceDigest},
	}}}
	if _, _, err := appendEventFacts(writer, nodes, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"reference_head":"event:reference"`) {
		t.Fatal("accepted supersede Fact omitted its exact expected Reference head")
	}
}

func TestValidateReportBoundsRuntimePrivateDelegation(t *testing.T) {
	report := validReport()
	report.Turns[0].DelegateCalls = 1
	if err := validateReport(report); err != nil {
		t.Fatalf("validateReport() rejected one bounded Runtime-private delegate: %v", err)
	}
	report.Turns[0].DelegateCalls = 2
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted more than one Runtime-private delegate in a turn")
	}
}

func TestValidateFailureReportBoundsRuntimePrivateDelegation(t *testing.T) {
	report := validFailureReport()
	report.Turns[0].DelegateCalls = 1
	if err := validateFailureReport(report); err != nil {
		t.Fatalf("validateFailureReport() rejected one bounded Runtime-private delegate: %v", err)
	}
	report.Turns[0].DelegateCalls = 2
	if err := validateFailureReport(report); err == nil {
		t.Fatal("validateFailureReport() accepted more than one Runtime-private delegate")
	}
}

func TestTurnEventBindingIsIndependentFromVisibleReceiptTraffic(t *testing.T) {
	turn := turnSummary{Role: "lead", Turn: "indirect-effect",
		CapturedAt: "2026-08-04T01:00:30Z", HookCues: 1, AgentEnd: true,
		AcceptedEvents: []acceptedEventSummary{{ID: "event:indirect",
			Digest: agency.Sum([]byte("indirect effect")).String()}}}
	if err := validateTurnSummary(turn); err != nil {
		t.Fatalf("validateTurnSummary() rejected an authority-bound indirect Effect: %v", err)
	}

	turn.AcceptedEvents = nil
	turn.SubmitAttempts, turn.IntentSubmits, turn.AcceptedReceipts = 1, 1, 1
	if err := validateTurnSummary(turn); err != nil {
		t.Fatalf("validateTurnSummary() rejected a visible accepted replay: %v", err)
	}

	turn.AcceptedEvents = []acceptedEventSummary{
		{ID: "event:first", Digest: agency.Sum([]byte("first effect")).String()},
		{ID: "event:second", Digest: agency.Sum([]byte("second effect")).String()},
	}
	if err := validateTurnSummary(turn); err == nil {
		t.Fatal("validateTurnSummary() accepted two new Effects in one turn")
	}
}

func TestReportValidationAccountsRuntimeSubmitInvocationFailures(t *testing.T) {
	report := validReport()
	report.Turns[0].SubmitAttempts = 1
	report.Turns[0].SubmitInvocationFailures = 1
	if err := validateReport(report); err != nil {
		t.Fatalf("validateReport() rejected one bounded Runtime invocation failure: %v", err)
	}
	report.Turns[0].SubmitInvocationFailures = 0
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted an unaccounted submit attempt")
	}

	failure := validFailureReport()
	failure.Turns[0].SubmitAttempts = 1
	failure.Turns[0].SubmitInvocationFailures = 1
	if err := validateFailureReport(failure); err != nil {
		t.Fatalf("validateFailureReport() rejected one bounded Runtime invocation failure: %v", err)
	}
}
