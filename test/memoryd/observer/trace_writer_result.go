package observer

import (
	"fmt"
	"slices"
)

type gateAssertion struct {
	ID     string
	Status GateStatus
}

func (writer *Writer) validateResult(result Result) (string, error) {
	if !slices.Contains([]ResultStatus{ResultPassed, ResultFailed, ResultIncomplete}, result.Status) {
		return "", fmt.Errorf("trace writer: invalid result status")
	}
	if len(result.Gates) > 64 {
		return "", fmt.Errorf("trace writer: gates exceed 64")
	}
	gateIDs := make(map[string]struct{}, len(result.Gates))
	hasFailure, hasPass := false, false
	for _, gate := range result.Gates {
		if err := writer.validateGate(gate, gateIDs); err != nil {
			return "", err
		}
		hasFailure = hasFailure || gate.Status == GateFail
		hasPass = hasPass || gate.Status == GatePass
	}
	if err := validateResultGateStatus(result, hasFailure, hasPass); err != nil {
		return "", err
	}
	return canonicalTime("result finished_at", result.FinishedAt)
}

func (writer *Writer) validateGate(gate Gate, gateIDs map[string]struct{}) error {
	if !validTraceToken(gate.ID) || !slices.Contains([]GateStatus{
		GatePass, GateFail, GateUnknown, GateNotApplicable,
	}, gate.Status) || len(gate.Evidence) > 32 {
		return fmt.Errorf("trace writer: invalid gate")
	}
	if _, duplicate := gateIDs[gate.ID]; duplicate {
		return fmt.Errorf("trace writer: repeats gate %q", gate.ID)
	}
	gateIDs[gate.ID] = struct{}{}
	if (gate.Status == GatePass || gate.Status == GateFail) && len(gate.Evidence) == 0 {
		return fmt.Errorf("trace writer: gate %q omits evidence", gate.ID)
	}
	if gate.Status == GateUnknown && len(gate.Evidence) != 0 {
		return fmt.Errorf("trace writer: unknown gate %q claims evidence", gate.ID)
	}
	return writer.validateGateEvidence(gate)
}

func (writer *Writer) validateGateEvidence(gate Gate) error {
	unique := make(map[string]struct{}, len(gate.Evidence))
	for _, evidence := range gate.Evidence {
		if _, duplicate := unique[evidence]; duplicate {
			return fmt.Errorf("trace writer: gate %q repeats evidence", gate.ID)
		}
		if _, exists := writer.seen[evidence]; !exists {
			return fmt.Errorf("trace writer: gate %q cites missing evidence", gate.ID)
		}
		if assertion, exists := writer.gateFacts[evidence]; exists &&
			(assertion.ID != gate.ID || assertion.Status != gate.Status) {
			return fmt.Errorf("trace writer: gate %q contradicts assertion %q", gate.ID, evidence)
		}
		unique[evidence] = struct{}{}
	}
	return nil
}

func validateResultGateStatus(result Result, hasFailure, hasPass bool) error {
	if result.Status == ResultPassed {
		if !hasPass {
			return fmt.Errorf("trace writer: passed result has no evidenced pass gate")
		}
		for _, gate := range result.Gates {
			if gate.Status == GateFail || gate.Status == GateUnknown {
				return fmt.Errorf("trace writer: passed result contains unresolved gate %q", gate.ID)
			}
		}
	}
	if result.Status == ResultFailed && !hasFailure {
		return fmt.Errorf("trace writer: failed result has no failed gate")
	}
	return nil
}
