package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	attentionTurnLimit    = 16
	maxAttentionHandlings = 64
)

type attentionEnvelope struct {
	Episode   string          `json:"episode"`
	Status    string          `json:"status"`
	TurnLimit int             `json:"turn_limit"`
	TurnsUsed int             `json:"turns_used"`
	Waves     []attentionWave `json:"waves"`
	Final     []attentionNode `json:"final_nodes"`
	Goal      *attentionGoal  `json:"goal"`
}

type attentionWave struct {
	Wave  int             `json:"wave"`
	Nodes []attentionNode `json:"nodes"`
}

type attentionNode struct {
	Role           string `json:"role"`
	OpenUnclaimed  int    `json:"open_unclaimed"`
	OccupiedClaims int    `json:"occupied_claims"`
}

type attentionGoal struct {
	Schema    string           `json:"schema"`
	Version   int              `json:"version"`
	Episode   string           `json:"episode"`
	Satisfied bool             `json:"satisfied"`
	Observed  ledgerStatus     `json:"observed"`
	Canary    *attentionCanary `json:"canary"`
}

type attentionCanary struct {
	ReceiptStatus    string       `json:"receipt_status"`
	CaptureIDPresent bool         `json:"capture_id_present"`
	Observed         ledgerStatus `json:"observed"`
	Settled          ledgerStatus `json:"settled"`
}

type attentionValidation struct {
	Turns    map[string]string
	Barriers map[string]struct{}
}

type attentionFailureKind string

const (
	attentionFailureBudgetExhausted attentionFailureKind = "budget_exhausted_before_outcome"
	attentionFailureQuiescent       attentionFailureKind = "quiescent_without_outcome"
	attentionFailureClaimOccupied   attentionFailureKind = "claim_occupied"
)

func validateAttention(values []attentionEnvelope) (attentionValidation, error) {
	validated := attentionValidation{
		Turns: make(map[string]string), Barriers: make(map[string]struct{}),
	}
	if len(values) != 2 {
		return validated, errors.New("sanitized live report omits an attention envelope")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateAttentionEnvelope(value, seen, &validated); err != nil {
			return validated, err
		}
	}
	return validated, nil
}

func validateAttentionEnvelope(value attentionEnvelope, seen map[string]struct{},
	validated *attentionValidation,
) error {
	if value.Episode != "episode-1" && value.Episode != "episode-2" {
		return errors.New("sanitized live report has an unknown attention episode")
	}
	if _, duplicate := seen[value.Episode]; duplicate {
		return errors.New("sanitized live report repeats an attention envelope")
	}
	seen[value.Episode] = struct{}{}
	if value.Status != "outcome_observed" || value.TurnLimit != attentionTurnLimit ||
		value.TurnsUsed < 0 || value.TurnsUsed > value.TurnLimit {
		return errors.New("sanitized live report has an invalid attention envelope")
	}
	if err := validateAttentionGoal(value.Goal, value.Episode); err != nil ||
		!value.Goal.Satisfied {
		if err == nil {
			err = errors.New("sanitized attention envelope ended before its goal")
		}
		return err
	}
	used := 0
	for index, wave := range value.Waves {
		count, err := bindAttentionWave(value.Episode, index, wave, validated)
		if err != nil {
			return err
		}
		used += count
	}
	final, err := validateAttentionNodes(value.Final)
	if err != nil {
		return err
	}
	// Open responsibilities are durable domain state, not a workflow terminal.
	// A completed Host turn may leave them for a later context. Only leaked claim
	// occupancy contradicts this bounded attention boundary.
	if used != value.TurnsUsed || used > value.TurnLimit || positiveOccupiedNodes(final) != 0 {
		return errors.New("sanitized live report attention turns are inconsistent")
	}
	return nil
}

func validateAttentionGoal(value *attentionGoal, episode string) error {
	if value == nil {
		return errors.New("sanitized attention envelope omits its goal observation")
	}
	if value.Schema != "memory.r7.domain-ops.goal" || value.Version != 2 ||
		value.Episode != episode || validateLedgerStatus(value.Observed) != nil {
		return errors.New("sanitized attention envelope has an invalid goal observation")
	}
	historicalSatisfied := value.Observed == (ledgerStatus{Charges: 8, ActiveCharges: 4,
		VoidedCharges: 4, UniqueBusinesses: 4, DuplicateBusinesses: 0})
	if !historicalSatisfied {
		if value.Canary != nil || value.Satisfied {
			return errors.New("sanitized attention goal contradicts its historical observation")
		}
		return nil
	}
	return validateAttentionCanary(value)
}

func validateAttentionCanary(value *attentionGoal) error {
	if !validAttentionCanary(value.Canary) {
		return errors.New("sanitized attention envelope has an invalid live canary")
	}
	activeCanary := ledgerStatus{Charges: 1, ActiveCharges: 1, UniqueBusinesses: 1}
	canarySatisfied := value.Canary.ReceiptStatus == "succeeded" &&
		value.Canary.CaptureIDPresent && value.Canary.Observed == activeCanary &&
		value.Canary.Settled == activeCanary
	if canarySatisfied != value.Satisfied {
		return errors.New("sanitized attention goal contradicts its closed observation")
	}
	return nil
}

func validAttentionCanary(value *attentionCanary) bool {
	if value == nil ||
		(value.ReceiptStatus != "succeeded" && value.ReceiptStatus != "failed") ||
		(value.ReceiptStatus == "succeeded") != value.CaptureIDPresent {
		return false
	}
	return validateLedgerStatus(value.Observed) == nil &&
		validateLedgerStatus(value.Settled) == nil &&
		value.Observed.Charges <= maxSyntheticChargesPerProbe &&
		value.Settled.Charges <= maxSyntheticChargesPerProbe
}

func validateLedgerStatus(value ledgerStatus) error {
	if value.Charges < 0 || value.Charges > 1000000 || value.ActiveCharges < 0 ||
		value.VoidedCharges < 0 || value.UniqueBusinesses < 0 ||
		value.DuplicateBusinesses < 0 ||
		value.ActiveCharges+value.VoidedCharges != value.Charges ||
		value.UniqueBusinesses > value.Charges ||
		value.DuplicateBusinesses > value.UniqueBusinesses {
		return errors.New("invalid ledger status")
	}
	return nil
}

func bindAttentionWave(episode string, index int, wave attentionWave,
	validated *attentionValidation,
) (int, error) {
	if wave.Wave != index+1 {
		return 0, errors.New("sanitized live report has a non-contiguous attention wave")
	}
	nodes, err := validateAttentionNodes(wave.Nodes)
	if err != nil {
		return 0, err
	}
	if positiveOpenUnclaimedNodes(nodes) == 0 || positiveOccupiedNodes(nodes) != 0 {
		return 0, errors.New("sanitized live report has an invalid attention wave")
	}
	barrier := fmt.Sprintf("%s-open-attention-%d", episode, wave.Wave)
	validated.Barriers[barrier] = struct{}{}
	used := 0
	for role, node := range nodes {
		if node.OpenUnclaimed == 0 {
			continue
		}
		turn := fmt.Sprintf("%s-open-attention-%d-%s", episode, wave.Wave, role)
		validated.Turns[turn] = role
		used++
	}
	return used, nil
}

func validateAttentionNodes(values []attentionNode) (map[string]attentionNode, error) {
	if len(values) != len(domainRoles) {
		return nil, errors.New("sanitized live report has an incomplete attention snapshot")
	}
	nodes := make(map[string]attentionNode, len(values))
	for _, value := range values {
		if !slices.Contains(domainRoles, value.Role) || value.OpenUnclaimed < 0 ||
			value.OpenUnclaimed > maxAttentionHandlings || value.OccupiedClaims < 0 ||
			value.OccupiedClaims > maxAttentionHandlings {
			return nil, errors.New("sanitized live report has an invalid attention node")
		}
		if _, duplicate := nodes[value.Role]; duplicate {
			return nil, errors.New("sanitized live report repeats an attention node")
		}
		nodes[value.Role] = value
	}
	return nodes, nil
}

func validateFailedAttention(code string, value *attentionEnvelope,
	turns []turnSummary,
) error {
	kind, present, err := classifyAttentionFailure(code, value)
	if err != nil || !present {
		return err
	}
	completed := make(map[string]struct{}, len(turns))
	for _, turn := range turns {
		completed[turn.Turn] = struct{}{}
	}
	used, err := validateFailedAttentionWaves(*value, completed)
	if err != nil {
		return err
	}
	final, err := validateAttentionNodes(value.Final)
	if err != nil {
		return err
	}
	if used != value.TurnsUsed {
		return errors.New("sanitized failure report has inconsistent attention evidence")
	}
	if kind == attentionFailureClaimOccupied {
		if value.Goal != nil {
			return errors.New("sanitized occupied-claim evidence unexpectedly depends on a goal")
		}
		if positiveOccupiedNodes(final) == 0 {
			return errors.New("sanitized failure report does not prove an occupied claim boundary")
		}
		return nil
	}
	if validateAttentionGoal(value.Goal, value.Episode) != nil {
		return errors.New("sanitized failure report has inconsistent attention goal evidence")
	}
	if value.Goal.Satisfied {
		return errors.New("sanitized failure report stopped despite an observed outcome")
	}
	if positiveOccupiedNodes(final) != 0 {
		return errors.New("sanitized failure report mixes claim occupancy with a semantic stop")
	}
	if kind == attentionFailureQuiescent {
		if positiveOpenUnclaimedNodes(final) != 0 {
			return errors.New("sanitized failure report does not prove attention quiescence")
		}
		return nil
	}
	if positiveOpenUnclaimedNodes(final) == 0 ||
		used+positiveOpenUnclaimedNodes(final) <= value.TurnLimit {
		return errors.New("sanitized failure report does not prove attention budget exhaustion")
	}
	return nil
}

func classifyAttentionFailure(code string,
	value *attentionEnvelope,
) (attentionFailureKind, bool, error) {
	kind := attentionFailureKind("")
	suffix := ""
	switch {
	case strings.HasSuffix(code, ".attention-budget-exhausted-before-outcome"):
		kind, suffix = attentionFailureBudgetExhausted,
			".attention-budget-exhausted-before-outcome"
	case strings.HasSuffix(code, ".attention-quiescent-without-outcome"):
		kind, suffix = attentionFailureQuiescent, ".attention-quiescent-without-outcome"
	case strings.HasSuffix(code, ".attention-claim-occupied"):
		kind, suffix = attentionFailureClaimOccupied, ".attention-claim-occupied"
	default:
		if value != nil {
			return "", false, errors.New("sanitized failure report has unexpected attention evidence")
		}
		return "", false, nil
	}
	if value == nil || (value.Episode != "episode-1" && value.Episode != "episode-2") ||
		value.TurnLimit != attentionTurnLimit || value.TurnsUsed < 0 ||
		value.TurnsUsed > value.TurnLimit {
		return "", false, errors.New("sanitized failure report omits its bounded attention evidence")
	}
	if value.Status != string(kind) || code != "scenario."+value.Episode+suffix {
		return "", false, errors.New("sanitized failure report mismatches its attention identity")
	}
	return kind, true, nil
}

func validateFailedAttentionWaves(value attentionEnvelope,
	completed map[string]struct{},
) (int, error) {
	used := 0
	for index, wave := range value.Waves {
		if wave.Wave != index+1 {
			return 0, errors.New("sanitized failure report has a non-contiguous attention wave")
		}
		nodes, err := validateAttentionNodes(wave.Nodes)
		if err != nil {
			return 0, err
		}
		if positiveOpenUnclaimedNodes(nodes) == 0 || positiveOccupiedNodes(nodes) != 0 {
			return 0, errors.New("sanitized failure report has an invalid attention wave")
		}
		for role, node := range nodes {
			if node.OpenUnclaimed == 0 {
				continue
			}
			turn := fmt.Sprintf("%s-open-attention-%d-%s", value.Episode, wave.Wave, role)
			if _, exists := completed[turn]; !exists {
				return 0, errors.New("sanitized failure report omits a completed attention turn")
			}
			used++
		}
	}
	return used, nil
}

func positiveOpenUnclaimedNodes(nodes map[string]attentionNode) int {
	positive := 0
	for _, node := range nodes {
		if node.OpenUnclaimed > 0 {
			positive++
		}
	}
	return positive
}

func positiveOccupiedNodes(nodes map[string]attentionNode) int {
	positive := 0
	for _, node := range nodes {
		if node.OccupiedClaims > 0 {
			positive++
		}
	}
	return positive
}
