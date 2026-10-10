package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestValidateReportBindsAttentionTurnsAndRetainsResidualWork(t *testing.T) {
	report := validReport()
	envelope := &report.Protocol.Attention[0]
	wave := attentionWave{Wave: 1}
	for _, role := range domainRoles {
		node := attentionNode{Role: role}
		if role == "data" {
			node.OpenUnclaimed = 2
		}
		wave.Nodes = append(wave.Nodes, node)
	}
	envelope.Waves = []attentionWave{wave}
	envelope.TurnsUsed = 1
	envelope.Final[0].OpenUnclaimed = 1
	report.Turns = append(report.Turns, turnSummary{Role: "data",
		Turn:       "episode-1-open-attention-1-data",
		CapturedAt: "2026-08-04T01:00:51Z", HookCues: 1, AgentEnd: true})
	barrier := deliveryQuiescenceSummary{Phase: "episode-1-open-attention-1",
		Status: "quiescent", Attempts: 1}
	for _, role := range domainRoles {
		barrier.Nodes = append(barrier.Nodes, deliveryNodeOccupancySummary{Role: role})
	}
	report.Protocol.DeliveryQuiescence = append(report.Protocol.DeliveryQuiescence, barrier)
	if err := validateReport(report); err != nil {
		t.Fatalf("validateReport() rejected residual responsibility evidence: %v", err)
	}

	report.Turns = report.Turns[:len(report.Turns)-1]
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted an attention wave without its target turn")
	}
	report = validReport()
	report.Protocol.Attention[0].Final[0].OccupiedClaims = 1
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted residual claim occupancy")
	}
	report = validReport()
	report.Protocol.Attention[0].Goal = unsatisfiedAttentionGoal("episode-1")
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted an outcome envelope with a false goal")
	}
}

func TestFailureReportAndTraceRetainBoundedAttentionExhaustion(t *testing.T) {
	report := attentionExhaustionFailureReport()
	if err := validateFailureReport(report); err != nil {
		t.Fatalf("validateFailureReport() rejected bounded exhaustion: %v", err)
	}

	var output bytes.Buffer
	scenario := scenarioEvidence{Digest: agency.Sum([]byte("attention scenario")).String()}
	if err := writeFailureTrace(&output, report, scenario, nil); err != nil {
		t.Fatalf("writeFailureTrace() error = %v", err)
	}
	waves, exhausted, quiescent, occupied, gateEvidence := inspectAttentionTrace(t, output.String())
	if waves != 15 || exhausted != len(domainRoles) || quiescent != 0 || occupied != 0 ||
		gateEvidence != 1+len(domainRoles) {
		t.Fatalf("attention trace facts = waves %d exhausted %d quiescent %d occupied %d gate %d",
			waves, exhausted, quiescent, occupied, gateEvidence)
	}

	report.Attention = nil
	if err := validateFailureReport(report); err == nil {
		t.Fatal("validateFailureReport() accepted budget exhaustion without a final envelope")
	}
	report = attentionExhaustionFailureReport()
	report.Failure.Code = "scenario.episode-2.attention-budget-exhausted-before-outcome"
	if err := validateFailureReport(report); err == nil {
		t.Fatal("validateFailureReport() accepted a mismatched attention episode")
	}
	report = attentionExhaustionFailureReport()
	report.Attention.Goal = satisfiedAttentionGoal("episode-1")
	if err := validateFailureReport(report); err == nil {
		t.Fatal("validateFailureReport() accepted exhaustion after an observed outcome")
	}
}

func TestFailureReportAndTraceRetainGoalFreeQuiescence(t *testing.T) {
	report := validFailureReport()
	report.Failure.Code = "scenario.episode-1.attention-quiescent-without-outcome"
	envelope := &attentionEnvelope{Episode: "episode-1", Status: "quiescent_without_outcome",
		TurnLimit: attentionTurnLimit, Goal: unsatisfiedAttentionGoal("episode-1")}
	for _, role := range domainRoles {
		envelope.Final = append(envelope.Final, attentionNode{Role: role})
	}
	report.Attention = envelope
	if err := validateFailureReport(report); err != nil {
		t.Fatalf("validateFailureReport() rejected goal-free quiescence: %v", err)
	}

	var output bytes.Buffer
	scenario := scenarioEvidence{Digest: agency.Sum([]byte("attention scenario")).String()}
	if err := writeFailureTrace(&output, report, scenario, nil); err != nil {
		t.Fatalf("writeFailureTrace() error = %v", err)
	}
	waves, exhausted, quiescent, occupied, gateEvidence := inspectAttentionTrace(t, output.String())
	if waves != 0 || exhausted != 0 || quiescent != len(domainRoles) || occupied != 0 ||
		gateEvidence != 1+len(domainRoles) {
		t.Fatalf("attention trace facts = waves %d exhausted %d quiescent %d occupied %d gate %d",
			waves, exhausted, quiescent, occupied, gateEvidence)
	}
}

func TestFailureReportAndTraceRetainOccupiedClaimBoundary(t *testing.T) {
	report := validFailureReport()
	report.Failure.Code = "scenario.episode-1.attention-claim-occupied"
	envelope := &attentionEnvelope{Episode: "episode-1", Status: "claim_occupied",
		TurnLimit: attentionTurnLimit}
	for _, role := range domainRoles {
		node := attentionNode{Role: role}
		if role == "data" {
			node.OccupiedClaims = 1
		}
		envelope.Final = append(envelope.Final, node)
	}
	report.Attention = envelope
	if err := validateFailureReport(report); err != nil {
		t.Fatalf("validateFailureReport() rejected occupied boundary: %v", err)
	}

	var output bytes.Buffer
	scenario := scenarioEvidence{Digest: agency.Sum([]byte("attention scenario")).String()}
	if err := writeFailureTrace(&output, report, scenario, nil); err != nil {
		t.Fatalf("writeFailureTrace() error = %v", err)
	}
	waves, exhausted, quiescent, occupied, gateEvidence := inspectAttentionTrace(t, output.String())
	if waves != 0 || exhausted != 0 || quiescent != 0 || occupied != len(domainRoles) ||
		gateEvidence != 1+len(domainRoles) {
		t.Fatalf("attention trace facts = waves %d exhausted %d quiescent %d occupied %d gate %d",
			waves, exhausted, quiescent, occupied, gateEvidence)
	}

	report.Attention.Final[0].OccupiedClaims = 0
	if err := validateFailureReport(report); err == nil {
		t.Fatal("validateFailureReport() accepted a boundary without occupied claims")
	}
}

func attentionExhaustionFailureReport() failureReport {
	report := validFailureReport()
	report.Failure.Code = "scenario.episode-1.attention-budget-exhausted-before-outcome"
	envelope := &attentionEnvelope{Episode: "episode-1",
		Status: "budget_exhausted_before_outcome", TurnLimit: attentionTurnLimit,
		TurnsUsed: 15, Goal: unsatisfiedAttentionGoal("episode-1")}
	for wave := 1; wave <= 3; wave++ {
		envelope.Waves = append(envelope.Waves, fullAttentionWave(wave))
		for _, role := range domainRoles {
			report.Turns = append(report.Turns, turnSummary{Role: role,
				Turn:       "episode-1-open-attention-" + strconv.Itoa(wave) + "-" + role,
				CapturedAt: "2026-08-04T01:00:30Z", HookCues: 1, AgentEnd: true})
		}
	}
	envelope.Final = finalOpenUnclaimedSnapshot()
	report.Attention = envelope
	return report
}

func fullAttentionWave(wave int) attentionWave {
	value := attentionWave{Wave: wave}
	for _, role := range domainRoles {
		value.Nodes = append(value.Nodes, attentionNode{Role: role, OpenUnclaimed: 1})
	}
	return value
}

func finalOpenUnclaimedSnapshot() []attentionNode {
	nodes := make([]attentionNode, 0, len(domainRoles))
	for _, role := range domainRoles {
		unclaimed := 0
		if role == "lead" || role == "payment" {
			unclaimed = 1
		}
		nodes = append(nodes, attentionNode{Role: role, OpenUnclaimed: unclaimed})
	}
	return nodes
}

func satisfiedAttentionGoal(episode string) *attentionGoal {
	activeCanary := ledgerStatus{Charges: 1, ActiveCharges: 1, UniqueBusinesses: 1}
	return &attentionGoal{Schema: "memory.r7.domain-ops.goal", Version: 2, Episode: episode,
		Satisfied: true, Observed: ledgerStatus{Charges: 8, ActiveCharges: 4,
			VoidedCharges: 4, UniqueBusinesses: 4},
		Canary: &attentionCanary{ReceiptStatus: "succeeded", CaptureIDPresent: true,
			Observed: activeCanary, Settled: activeCanary}}
}

func unsatisfiedAttentionGoal(episode string) *attentionGoal {
	return &attentionGoal{Schema: "memory.r7.domain-ops.goal", Version: 2, Episode: episode,
		Observed: ledgerStatus{Charges: 8, ActiveCharges: 8,
			UniqueBusinesses: 4, DuplicateBusinesses: 4}}
}

func TestValidateAttentionGoalRequiresHistoricalAndLiveEvidence(t *testing.T) {
	goal := satisfiedAttentionGoal("episode-1")
	if err := validateAttentionGoal(goal, "episode-1"); err != nil {
		t.Fatalf("validateAttentionGoal() rejected complete evidence: %v", err)
	}

	goal.Canary = nil
	if err := validateAttentionGoal(goal, "episode-1"); err == nil {
		t.Fatal("validateAttentionGoal() accepted historical state without a live canary")
	}

	goal = satisfiedAttentionGoal("episode-1")
	goal.Canary.Settled = ledgerStatus{Charges: 1, VoidedCharges: 1,
		UniqueBusinesses: 1}
	if err := validateAttentionGoal(goal, "episode-1"); err == nil {
		t.Fatal("validateAttentionGoal() accepted a canary without one settled active effect")
	}

	goal = satisfiedAttentionGoal("episode-1")
	goal.Satisfied = false
	goal.Canary.ReceiptStatus = "failed"
	goal.Canary.CaptureIDPresent = false
	goal.Canary.Observed = ledgerStatus{}
	goal.Canary.Settled = ledgerStatus{}
	if err := validateAttentionGoal(goal, "episode-1"); err != nil {
		t.Fatalf("validateAttentionGoal() rejected bounded negative canary evidence: %v", err)
	}

	goal.Canary.CaptureIDPresent = true
	if err := validateAttentionGoal(goal, "episode-1"); err == nil {
		t.Fatal("validateAttentionGoal() accepted a failed receipt with a capture ID")
	}

	goal = satisfiedAttentionGoal("episode-1")
	goal.Satisfied = false
	goal.Canary.Observed = ledgerStatus{Charges: maxSyntheticChargesPerProbe + 1,
		VoidedCharges:    maxSyntheticChargesPerProbe + 1,
		UniqueBusinesses: maxSyntheticChargesPerProbe + 1}
	if err := validateAttentionGoal(goal, "episode-1"); err == nil {
		t.Fatal("validateAttentionGoal() accepted a canary above its physical charge bound")
	}

	goal = unsatisfiedAttentionGoal("episode-1")
	goal.Canary = &attentionCanary{ReceiptStatus: "failed"}
	if err := validateAttentionGoal(goal, "episode-1"); err == nil {
		t.Fatal("validateAttentionGoal() accepted a canary before historical repair")
	}
}

type attentionTraceRecord struct {
	Record string `json:"record"`
	Kind   string `json:"kind"`
	Facts  struct {
		Episode        string `json:"episode"`
		Role           string `json:"role"`
		OpenUnclaimed  *int   `json:"open_unclaimed"`
		OccupiedClaims *int   `json:"occupied_claims"`
		TurnLimit      *int   `json:"turn_limit"`
		TurnsUsed      *int   `json:"turns_used"`
		GoalDigest     string `json:"goal_digest"`
		GoalSatisfied  *bool  `json:"goal_satisfied"`
	} `json:"facts"`
	Gates []struct {
		ID       string   `json:"id"`
		Evidence []string `json:"evidence"`
	} `json:"gates"`
}

func inspectAttentionTrace(t *testing.T, output string) (int, int, int, int, int) {
	t.Helper()
	waves, exhausted, quiescent, occupied, gateEvidence := 0, 0, 0, 0, 0
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var record attentionTraceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		wave, exhaustion, quiet, occupancy := classifyAttentionTraceFact(t, record)
		waves += wave
		exhausted += exhaustion
		quiescent += quiet
		occupied += occupancy
		for _, gate := range record.Gates {
			if gate.ID == "scenario.run" {
				gateEvidence = len(gate.Evidence)
			}
		}
	}
	return waves, exhausted, quiescent, occupied, gateEvidence
}

func classifyAttentionTraceFact(t *testing.T, record attentionTraceRecord) (int, int, int, int) {
	t.Helper()
	if !strings.HasPrefix(record.Kind, "test.attention.") {
		return 0, 0, 0, 0
	}
	if record.Facts.Episode != "episode-1" || record.Facts.Role == "" ||
		record.Facts.OpenUnclaimed == nil || record.Facts.OccupiedClaims == nil ||
		record.Facts.TurnLimit == nil || record.Facts.TurnsUsed == nil {
		t.Fatal("attention Fact omitted closed numeric evidence")
	}
	switch record.Kind {
	case "test.attention.wave":
		return 1, 0, 0, 0
	case "test.attention.outcome", "test.attention.exhausted", "test.attention.quiescent":
		if _, err := agency.ParseDigest(record.Facts.GoalDigest); err != nil ||
			record.Facts.GoalSatisfied == nil {
			t.Fatal("terminal attention Fact omitted its goal binding")
		}
		if record.Kind == "test.attention.exhausted" {
			return 0, 1, 0, 0
		}
		if record.Kind == "test.attention.quiescent" {
			return 0, 0, 1, 0
		}
		return 0, 0, 0, 0
	case "test.attention.occupied":
		if record.Facts.GoalDigest != "" || record.Facts.GoalSatisfied != nil {
			t.Fatal("occupied attention Fact unexpectedly depended on a goal observation")
		}
		return 0, 0, 0, 1
	default:
		t.Fatalf("unknown attention Fact %q", record.Kind)
	}
	return 0, 0, 0, 0
}
