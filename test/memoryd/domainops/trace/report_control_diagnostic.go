package main

import (
	"errors"
	"slices"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/authority"
)

type controlDenial struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

func validControlDenialCode(code string) bool {
	switch code {
	case "invalid_argument", "content_required", "content_too_large", "artifact_invalid",
		"artifact_too_large", "authentication_failed", "context_required", "context_stale",
		"asset_revision_mismatch", "action_not_allowed", "operation_mismatch",
		"operation_pending", "memoryd_unavailable", "internal":
		return true
	default:
		return false
	}
}

func validateTurnSummary(turn turnSummary) error {
	if err := validateTurnBounds(turn); err != nil {
		return err
	}
	if err := validateRuntimeObservationConsistency(turn); err != nil {
		return err
	}
	return validateControlDenials(turn)
}

func validateTurnBounds(turn turnSummary) error {
	values := []int{turn.HookCues, turn.BashCalls, turn.DelegateCalls, turn.CurrentReads,
		turn.SubmitAttempts, turn.IntentSubmits, turn.AcceptedReceipts, turn.RejectedReceipts,
		turn.SubmitDenials, turn.SubmitInvocationFailures, turn.PostAcceptDenials,
		turn.PrivateBindingProbes,
		turn.DomainOperations.Read.Attempts, turn.DomainOperations.Read.Successes,
		turn.DomainOperations.Read.ToolErrors, turn.DomainOperations.Read.InvalidResults,
		turn.DomainOperations.Read.Batched,
		turn.DomainOperations.Probe.Attempts, turn.DomainOperations.Probe.Successes,
		turn.DomainOperations.Probe.ToolErrors, turn.DomainOperations.Probe.InvalidResults,
		turn.DomainOperations.Probe.Batched,
		turn.DomainOperations.Mutation.Attempts, turn.DomainOperations.Mutation.Successes,
		turn.DomainOperations.Mutation.ToolErrors, turn.DomainOperations.Mutation.InvalidResults,
		turn.DomainOperations.Mutation.Batched}
	if _, err := parseReportTime("turn captured_at", turn.CapturedAt); err != nil {
		return err
	}
	if !turn.AgentEnd || turn.HookCues < 1 || slices.ContainsFunc(values,
		func(value int) bool { return value < 0 || value > 256 }) {
		return errors.New("sanitized report contains an invalid bounded turn")
	}
	return nil
}

func validateRuntimeObservationConsistency(turn turnSummary) error {
	if turn.PrivateBindingProbes != 0 || turn.DelegateCalls > 1 ||
		turn.AcceptedReceipts > 1 ||
		len(turn.AcceptedEvents) > 1 ||
		!domainOperationClosed(turn.DomainOperations.Read) ||
		!domainOperationClosed(turn.DomainOperations.Probe) ||
		!domainOperationClosed(turn.DomainOperations.Mutation) ||
		turn.PostAcceptDenials > turn.SubmitDenials ||
		(turn.PostAcceptDenials > 0 && turn.AcceptedReceipts != 1) ||
		turn.IntentSubmits != turn.AcceptedReceipts+turn.RejectedReceipts ||
		turn.SubmitAttempts != turn.IntentSubmits+turn.SubmitDenials+turn.SubmitInvocationFailures {
		return errors.New("sanitized report contains inconsistent Runtime observations")
	}
	if !validViewSummary(turn.View, turn.CurrentReads) {
		return errors.New("sanitized report contains inconsistent Agent View metadata")
	}
	for _, event := range turn.AcceptedEvents {
		if _, err := agency.NewEventID(event.ID); err != nil {
			return errors.New("sanitized report contains an invalid accepted Event ID")
		}
		if _, err := agency.ParseDigest(event.Digest); err != nil {
			return errors.New("sanitized report contains an invalid accepted Event digest")
		}
	}
	return nil
}

func validViewSummary(view *agentViewSummary, reads int) bool {
	if reads == 0 {
		return view == nil
	}
	if view == nil || view.OpenTotal < 0 || view.OpenTotal > authority.MaxOpenHandlingsPerPrincipal ||
		view.RelatedTotal < 0 || view.RelatedTotal > agency.MaxAgentViewRelatedTotal ||
		view.RelatedProjected < 0 || view.RelatedProjected > agency.MaxAgentViewRelated ||
		view.RelatedProjected > view.RelatedTotal ||
		view.Truncated != (view.RelatedProjected < view.RelatedTotal) ||
		view.HasCurrent != (view.ReplyRequired != nil) ||
		view.HasCurrent != (view.ReplyPending != nil) {
		return false
	}
	return !view.HasCurrent || view.OpenTotal > 0
}

func domainOperationClosed(operation domainOperationSummary) bool {
	return operation.Attempts == operation.Successes+operation.ToolErrors+
		operation.InvalidResults+operation.Batched
}

func validateControlDenials(turn turnSummary) error {
	if len(turn.SubmitControlDenials) > 14 {
		return errors.New("sanitized report contains unbounded control denial classes")
	}
	denialCount := 0
	previousCode := ""
	for _, denial := range turn.SubmitControlDenials {
		if !validControlDenialCode(denial.Code) || denial.Count < 1 || denial.Count > 256 {
			return errors.New("sanitized report contains an invalid control denial")
		}
		if previousCode != "" && denial.Code <= previousCode {
			return errors.New("sanitized report control denial codes are not unique and sorted")
		}
		previousCode = denial.Code
		denialCount += denial.Count
	}
	if denialCount != turn.SubmitDenials {
		return errors.New("sanitized report does not classify every control denial")
	}
	return nil
}
