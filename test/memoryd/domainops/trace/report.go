package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const maxReportBytes = 2 << 20

var domainRoles = []string{"data", "edge", "lead", "payment", "platform"}

type liveReport struct {
	Schema    string    `json:"schema"`
	Version   int       `json:"version"`
	Status    string    `json:"status"`
	Model     string    `json:"model"`
	Thinking  string    `json:"thinking"`
	Run       runReport `json:"run"`
	Isolation struct {
		Passed                      bool `json:"passed"`
		FreshRuntimeBetweenEpisodes bool `json:"fresh_runtime_between_episodes"`
	} `json:"isolation"`
	World struct {
		Episodes []episodeReport `json:"episodes"`
	} `json:"world"`
	Protocol struct {
		AcceptedPeerEffects int                         `json:"accepted_peer_effects"`
		ByReceiver          []peerEffectSummary         `json:"by_receiver"`
		DeliveryQuiescence  []deliveryQuiescenceSummary `json:"delivery_quiescence"`
		Attention           []attentionEnvelope         `json:"attention_envelopes"`
		Evolution           evolutionSummary            `json:"evolution"`
	} `json:"protocol"`
	Turns                      []turnSummary `json:"turns"`
	RawProviderStreamsRetained bool          `json:"raw_provider_streams_retained"`
}

type episodeReport struct {
	ID               string              `json:"id"`
	Baseline         loadSummary         `json:"baseline"`
	SyntheticProbes  syntheticProbeAudit `json:"synthetic_probes"`
	Recovery         loadSummary         `json:"recovery"`
	Stability        loadSummary         `json:"stability"`
	IncidentAfter    domainResult        `json:"incident_after"`
	IncidentCharges  domainChargeResult  `json:"incident_charges"`
	RecoveryCharges  domainChargeResult  `json:"recovery_charges"`
	StabilityCharges domainChargeResult  `json:"stability_charges"`
}

type evolutionSummary struct {
	Boundary              evolutionBoundarySummary `json:"boundary"`
	Effects               []evolutionNodeSummary   `json:"effects"`
	AcceptedReferenceUses int                      `json:"accepted_reference_uses"`
	Demonstrated          bool                     `json:"demonstrated"`
}

type evolutionBoundarySummary struct {
	Nodes           []evolutionBoundaryNode `json:"nodes"`
	ActiveHeadCount int                     `json:"active_head_count"`
}

type evolutionBoundaryNode struct {
	Role                       string                   `json:"role"`
	ConsolidationAfterSequence uint64                   `json:"consolidation_after_sequence"`
	MaxOriginSequence          uint64                   `json:"max_origin_sequence"`
	ActiveHeads                []evolutionReferenceHead `json:"active_heads"`
}

type evolutionReferenceHead struct {
	EventID     string `json:"event_id"`
	EventDigest string `json:"event_digest"`
}

type evolutionNodeSummary struct {
	Role                  string                 `json:"role"`
	BoundarySequence      uint64                 `json:"boundary_sequence"`
	ActiveHeadCount       int                    `json:"active_head_count"`
	AcceptedReferenceUses int                    `json:"accepted_reference_uses"`
	Matches               []evolutionMatchReport `json:"matches"`
}

type evolutionMatchReport struct {
	EventID          string `json:"event_id"`
	ReferenceEventID string `json:"reference_event_id"`
	ReferenceDigest  string `json:"reference_digest"`
}

type runReport struct {
	ID              string `json:"id"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
	CandidateDigest string `json:"candidate_digest"`
}

type peerEffectSummary struct {
	Role                string `json:"role"`
	AcceptedPeerEffects int    `json:"accepted_peer_effects"`
}

type deliveryQuiescenceSummary struct {
	Phase                  string                         `json:"phase"`
	Status                 string                         `json:"status"`
	Attempts               int                            `json:"attempts"`
	ElapsedSeconds         int                            `json:"elapsed_seconds"`
	PendingDeliveryRecords int                            `json:"pending_delivery_records"`
	Nodes                  []deliveryNodeOccupancySummary `json:"nodes"`
}

type deliveryNodeOccupancySummary struct {
	Role          string `json:"role"`
	PendingOutbox int    `json:"pending_outbox"`
	StagedInbox   int    `json:"staged_inbox"`
}

type loadSummary struct {
	Prefix       string           `json:"prefix"`
	Sent         int              `json:"sent"`
	Accepted     int              `json:"accepted"`
	Failed       int              `json:"failed"`
	Receipts     []serviceReceipt `json:"receipts"`
	ElapsedMS    int64            `json:"elapsed_ms"`
	Observed     monitorStatus    `json:"observed"`
	ObservedAtMS int64            `json:"observed_at_ms"`
}

type serviceReceipt struct {
	BusinessID string `json:"business_id"`
	CaptureID  int64  `json:"capture_id"`
}

type monitorStatus struct {
	Gateway gatewayStatus `json:"gateway"`
	Ledger  ledgerStatus  `json:"ledger"`
}

type gatewayStatus struct {
	Route     string `json:"route"`
	Requests  int64  `json:"requests"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
}

type ledgerStatus struct {
	Charges             int `json:"charges"`
	ActiveCharges       int `json:"active_charges"`
	VoidedCharges       int `json:"voided_charges"`
	UniqueBusinesses    int `json:"unique_businesses"`
	DuplicateBusinesses int `json:"duplicate_businesses"`
}

type domainResult struct {
	Role   string       `json:"role"`
	Result ledgerStatus `json:"result"`
}

type domainChargeResult struct {
	Role   string         `json:"role"`
	Result []chargeRecord `json:"result"`
}

type chargeRecord struct {
	Sequence   int64  `json:"sequence"`
	BusinessID string `json:"business_id"`
	AttemptKey string `json:"attempt_key"`
	State      string `json:"state"`
	VoidReason string `json:"void_reason,omitempty"`
}

func loadReport(path string) (liveReport, error) {
	var report liveReport
	if err := readBoundedJSON(path, maxReportBytes, &report); err != nil {
		return liveReport{}, err
	}
	if err := validateReport(report); err != nil {
		return liveReport{}, err
	}
	return report, nil
}

func validateReport(report liveReport) error {
	if report.Schema != "memory.r7.domain-ops.live-report" || report.Version != 7 ||
		report.Status != "passed" || report.Model == "" ||
		!validThinkingLevel(report.Thinking) ||
		report.RawProviderStreamsRetained || !report.Isolation.Passed ||
		!report.Isolation.FreshRuntimeBetweenEpisodes {
		return errors.New("sanitized live report has invalid identity or terminal status")
	}
	if _, err := agency.NewOpaqueHandle(report.Model); err != nil {
		return fmt.Errorf("sanitized live report model: %w", err)
	}
	if _, err := agency.ParseDigest(report.Run.CandidateDigest); err != nil {
		return fmt.Errorf("sanitized live report candidate: %w", err)
	}
	startedAt, err := parseReportTime("started_at", report.Run.StartedAt)
	if err != nil {
		return err
	}
	finishedAt, err := parseReportTime("finished_at", report.Run.FinishedAt)
	if err != nil || finishedAt.Before(startedAt) {
		return errors.New("sanitized live report has invalid run interval")
	}
	if _, err := agency.NewOpaqueHandle(report.Run.ID); err != nil {
		return fmt.Errorf("sanitized live report run ID: %w", err)
	}
	if err := validateWorld(report); err != nil {
		return err
	}
	attention, err := validateReportProtocol(report)
	if err != nil {
		return err
	}
	return validateTurns(report.Turns, attention.Turns)
}

func validThinkingLevel(value string) bool {
	return slices.Contains([]string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}, value)
}

func validateReportProtocol(report liveReport) (attentionValidation, error) {
	if err := validateProtocolSummary(report.Protocol.AcceptedPeerEffects,
		report.Protocol.ByReceiver); err != nil {
		return attentionValidation{}, err
	}
	attention, err := validateAttention(report.Protocol.Attention)
	if err != nil {
		return attentionValidation{}, err
	}
	if err := validateDeliveryQuiescence(
		report.Protocol.DeliveryQuiescence, attention.Barriers); err != nil {
		return attentionValidation{}, err
	}
	if err := validateEvolutionSummary(report.Protocol.Evolution); err != nil {
		return attentionValidation{}, err
	}
	return attention, nil
}

func validateProtocolSummary(total int, values []peerEffectSummary) error {
	if total < 1 || len(values) != len(domainRoles) {
		return errors.New("sanitized live report has no complete peer-effect summary")
	}
	byRole := make(map[string]int, len(values))
	sum := 0
	for _, value := range values {
		if value.AcceptedPeerEffects < 0 {
			return errors.New("sanitized live report has a negative peer-effect count")
		}
		if _, duplicate := byRole[value.Role]; duplicate {
			return errors.New("sanitized live report repeats a peer-effect receiver")
		}
		byRole[value.Role] = value.AcceptedPeerEffects
		sum += value.AcceptedPeerEffects
	}
	if sum != total {
		return errors.New("sanitized live report peer-effect total is inconsistent")
	}
	for _, role := range domainRoles {
		if _, exists := byRole[role]; !exists {
			return errors.New("sanitized live report omits a peer-effect receiver")
		}
	}
	return nil
}

func validateDeliveryQuiescence(values []deliveryQuiescenceSummary,
	attention map[string]struct{},
) error {
	want := make(map[string]struct{}, 3+len(attention))
	for episode := 1; episode <= 2; episode++ {
		want[fmt.Sprintf("episode-%d-initial-lead", episode)] = struct{}{}
	}
	want["episode-1-post-outcome-lead"] = struct{}{}
	for phase := range attention {
		want[phase] = struct{}{}
	}
	if len(values) != len(want) {
		return errors.New("sanitized live report has an incomplete delivery barrier summary")
	}
	for _, value := range values {
		if _, exists := want[value.Phase]; !exists || value.Status != "quiescent" ||
			value.Attempts < 1 || value.Attempts > 256 || value.ElapsedSeconds < 0 ||
			value.ElapsedSeconds > 30 || value.PendingDeliveryRecords != 0 ||
			len(value.Nodes) != len(domainRoles) {
			return errors.New("sanitized live report contains an invalid delivery barrier")
		}
		delete(want, value.Phase)
		seen := make(map[string]struct{}, len(value.Nodes))
		for _, node := range value.Nodes {
			if !slices.Contains(domainRoles, node.Role) || node.PendingOutbox != 0 ||
				node.StagedInbox != 0 {
				return errors.New("sanitized live report contains non-quiescent peer occupancy")
			}
			if _, duplicate := seen[node.Role]; duplicate {
				return errors.New("sanitized live report repeats a delivery barrier node")
			}
			seen[node.Role] = struct{}{}
		}
	}
	return nil
}

func validateTurns(turns []turnSummary, attention map[string]string) error {
	expected := expectedTurnRoles()
	for turn, role := range attention {
		expected[turn] = role
	}
	if len(turns) != len(expected) {
		return fmt.Errorf("sanitized live report has %d turns, want %d", len(turns), len(expected))
	}
	for _, turn := range turns {
		role, exists := expected[turn.Turn]
		if !exists || role != turn.Role {
			return errors.New("sanitized live report contains an unknown role/turn pair")
		}
		delete(expected, turn.Turn)
		if err := validateTurnSummary(turn); err != nil {
			return err
		}
	}
	if len(expected) != 0 {
		return errors.New("sanitized live report omits an expected attention turn")
	}
	return nil
}

func expectedTurnRoles() map[string]string {
	expected := make(map[string]string, 3)
	for episode := 1; episode <= 2; episode++ {
		expected[fmt.Sprintf("episode-%d-initial-lead", episode)] = "lead"
	}
	expected["episode-1-post-outcome-lead"] = "lead"
	return expected
}

func parseReportTime(label, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || value != parsed.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, fmt.Errorf("sanitized live report %s is not canonical UTC RFC3339Nano", label)
	}
	return parsed.UTC(), nil
}

func readBoundedJSON(path string, maximum int64, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open sanitized report: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return fmt.Errorf("read sanitized report: %w", err)
	}
	if int64(len(raw)) > maximum {
		return fmt.Errorf("sanitized report exceeds %d bytes", maximum)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode sanitized report: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("sanitized report has trailing data")
	}
	return nil
}
