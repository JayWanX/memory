package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/test/memoryd/observer"
)

type failureReport struct {
	Schema   string    `json:"schema"`
	Version  int       `json:"version"`
	Status   string    `json:"status"`
	Model    string    `json:"model"`
	Thinking string    `json:"thinking"`
	Run      runReport `json:"run"`
	Failure  struct {
		Code       string `json:"code"`
		ObservedAt string `json:"observed_at"`
	} `json:"failure"`
	World                      []failureWorldSnapshot `json:"world"`
	Attention                  *attentionEnvelope     `json:"attention_envelope"`
	Turns                      []turnSummary          `json:"turns"`
	RawProviderStreamsRetained bool                   `json:"raw_provider_streams_retained"`
}

type failureWorldSnapshot struct {
	Episode             string `json:"episode"`
	Charges             int    `json:"charges"`
	ActiveCharges       int    `json:"active_charges"`
	VoidedCharges       int    `json:"voided_charges"`
	UniqueBusinesses    int    `json:"unique_businesses"`
	DuplicateBusinesses int    `json:"duplicate_businesses"`
}

func loadFailureReport(path string) (failureReport, error) {
	var report failureReport
	if err := readBoundedJSON(path, maxReportBytes, &report); err != nil {
		return failureReport{}, err
	}
	if err := validateFailureReport(report); err != nil {
		return failureReport{}, err
	}
	return report, nil
}

func validateFailureReport(report failureReport) error {
	if report.Schema != "memory.r7.domain-ops.failure-report" || report.Version != 7 ||
		report.Status != "failed" || report.RawProviderStreamsRetained ||
		report.Model == "" || !validThinkingLevel(report.Thinking) || report.Failure.Code == "" {
		return errors.New("sanitized failure report has invalid identity or status")
	}
	for label, value := range map[string]string{"model": report.Model,
		"run ID": report.Run.ID, "failure code": report.Failure.Code} {
		if _, err := agency.NewOpaqueHandle(value); err != nil {
			return fmt.Errorf("sanitized failure report %s: %w", label, err)
		}
	}
	if _, err := agency.ParseDigest(report.Run.CandidateDigest); err != nil {
		return fmt.Errorf("sanitized failure report candidate: %w", err)
	}
	startedAt, err := parseReportTime("started_at", report.Run.StartedAt)
	if err != nil {
		return err
	}
	finishedAt, err := parseReportTime("finished_at", report.Run.FinishedAt)
	if err != nil || finishedAt.Before(startedAt) {
		return errors.New("sanitized failure report has invalid run interval")
	}
	observedAt, err := parseReportTime("failure observed_at", report.Failure.ObservedAt)
	if err != nil || observedAt.Before(startedAt) || observedAt.After(finishedAt) {
		return errors.New("sanitized failure report has invalid failure time")
	}
	if err := validateCompletedTurnSubset(report.Turns); err != nil {
		return err
	}
	if err := validateFailureWorld(report.World); err != nil {
		return err
	}
	return validateFailedAttention(report.Failure.Code, report.Attention, report.Turns)
}

func validateFailureWorld(values []failureWorldSnapshot) error {
	if len(values) > 2 {
		return errors.New("sanitized failure report contains too many world snapshots")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !slices.Contains([]string{"episode-1", "episode-2"}, value.Episode) ||
			value.Charges < 0 || value.Charges > 1000000 || value.ActiveCharges < 0 ||
			value.VoidedCharges < 0 || value.UniqueBusinesses < 0 ||
			value.DuplicateBusinesses < 0 || value.ActiveCharges+value.VoidedCharges != value.Charges ||
			value.UniqueBusinesses > value.Charges || value.DuplicateBusinesses > value.UniqueBusinesses {
			return errors.New("sanitized failure report contains an invalid world snapshot")
		}
		if _, duplicate := seen[value.Episode]; duplicate {
			return errors.New("sanitized failure report repeats a world snapshot")
		}
		seen[value.Episode] = struct{}{}
	}
	return nil
}

func validateCompletedTurnSubset(turns []turnSummary) error {
	if len(turns) > 3+2*attentionTurnLimit {
		return errors.New("sanitized failure report contains too many completed turns")
	}
	seen := make(map[string]struct{}, len(turns))
	for _, turn := range turns {
		if !slices.Contains(domainRoles, turn.Role) || turn.Turn == "" {
			return errors.New("sanitized failure report contains an unknown role or turn")
		}
		if _, duplicate := seen[turn.Turn]; duplicate {
			return errors.New("sanitized failure report repeats a turn")
		}
		seen[turn.Turn] = struct{}{}
		if err := validateTurnSummary(turn); err != nil {
			return fmt.Errorf("sanitized failure report contains invalid completed turn %q for role %q: %w",
				turn.Turn, turn.Role, err)
		}
	}
	return nil
}

func writeFailureTrace(destination io.Writer, report failureReport,
	scenario scenarioEvidence, nodes []nodeEvidence,
) error {
	startedAt, _ := parseReportTime("started_at", report.Run.StartedAt)
	finishedAt, _ := parseReportTime("finished_at", report.Run.FinishedAt)
	observedAt, _ := parseReportTime("failure observed_at", report.Failure.ObservedAt)
	participants := make([]observer.Participant, 0, len(domainRoles))
	for _, role := range domainRoles {
		participants = append(participants, observer.Participant{Node: role, Agent: role,
			Runtime: "pi", Model: report.Model})
	}
	writer, err := observer.NewWriter(destination, observer.Run{ID: report.Run.ID,
		Scenario:  observer.Scenario{ID: scenarioID, Digest: scenario.Digest},
		StartedAt: startedAt, CandidateDigest: report.Run.CandidateDigest,
		Participants: participants})
	if err != nil {
		return err
	}
	if err := appendRuntimeFacts(writer, report.Turns); err != nil {
		return err
	}
	if err := validateTurnEventBindings(report.Turns, nodes); err != nil {
		return err
	}
	if _, err := appendArtifactFacts(writer, nodes); err != nil {
		return err
	}
	eventFacts, ordered, err := appendEventFacts(writer, nodes, report.Turns)
	if err != nil {
		return err
	}
	if err := appendDomainEffectFacts(writer, nodes, eventFacts, ordered); err != nil {
		return err
	}
	receiptFacts, err := appendReceiptFacts(writer, nodes, eventFacts)
	if err != nil {
		return err
	}
	readmittedFacts, err := appendDeliveryFacts(writer, nodes, eventFacts)
	if err != nil {
		return err
	}
	attentionFacts, err := appendFailedAttentionFacts(writer, report.Attention, observedAt)
	if err != nil {
		return err
	}
	return finishFailedTrace(writer, report.Failure.Code, attentionFacts, receiptFacts,
		readmittedFacts, observedAt, finishedAt)
}

func finishFailedTrace(writer *observer.Writer, code string, attentionFacts []string,
	receiptFacts, readmittedFacts []string, observedAt, finishedAt time.Time,
) error {
	failureFact := hashedFactID("failed-run", code)
	if _, err := writer.Append(observer.Fact{ID: failureFact, CapturedAt: observedAt,
		Source: observer.Source{Class: observer.SourceOracle, Node: "runner"},
		Kind:   "test.gate.checked", Truth: observer.TruthAssertion,
		Fields: observer.FactFields{GateID: "scenario.run", Status: "fail", Code: code}}); err != nil {
		return err
	}
	evidence := append([]string{failureFact}, attentionFacts...)
	gates := []observer.Gate{{ID: "scenario.run", Status: observer.GateFail,
		Evidence: evidence}}
	for _, gate := range []string{"scenario.recovery", "scenario.service-receipts",
		"r7.delivery-quiescence", "scenario.isolation", "scenario.evolution"} {
		gates = append(gates, observer.Gate{ID: gate, Status: observer.GateUnknown})
	}
	gates = append(gates,
		observedProtocolGate("r7.operation-receipts", receiptFacts),
		observedProtocolGate("r7.peer-accepted-effect", readmittedFacts))
	return writer.Finish(observer.Result{Status: observer.ResultFailed,
		FinishedAt: finishedAt, Gates: gates})
}

func observedProtocolGate(id string, facts []string) observer.Gate {
	if len(facts) == 0 {
		return observer.Gate{ID: id, Status: observer.GateUnknown}
	}
	if len(facts) > 32 {
		facts = facts[:32]
	}
	return observer.Gate{ID: id, Status: observer.GatePass,
		Evidence: append([]string{}, facts...)}
}
