package observer

import (
	"fmt"
	"regexp"
	"slices"
	"time"
)

const (
	traceSchema   = "memory.test.trace"
	traceVersion  = 2
	maxTraceLine  = 16 << 10
	maxTraceFacts = 100000
)

var (
	tokenPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	tracePattern      = regexp.MustCompile(`^trace:[A-Za-z0-9][A-Za-z0-9._:-]{0,121}$`)
	sourceClasses     = []string{"runtime", "r7_authority", "transport", "oracle", "runner"}
	truthClasses      = []string{"observation", "accepted_local_fact", "derived_projection", "assertion"}
	intentDenialCodes = []string{
		"invalid_argument", "content_required", "content_too_large", "artifact_invalid",
		"artifact_too_large", "authentication_failed", "context_required", "context_stale",
		"asset_revision_mismatch", "action_not_allowed", "operation_mismatch",
		"operation_pending", "memoryd_unavailable", "internal",
	}
)

func validateWriterRun(run Run) (string, error) {
	if !validTraceToken(run.ID) || !validTraceToken(run.Scenario.ID) ||
		!digestPattern.MatchString(run.Scenario.Digest) {
		return "", fmt.Errorf("trace writer: invalid run or scenario identity")
	}
	if run.CandidateDigest != "" && !digestPattern.MatchString(run.CandidateDigest) {
		return "", fmt.Errorf("trace writer: invalid candidate digest")
	}
	if len(run.Participants) > 32 {
		return "", fmt.Errorf("trace writer: participants exceed 32")
	}
	for _, participant := range run.Participants {
		if !validTraceToken(participant.Node) || !validOptionalTokens(
			participant.Agent, participant.Runtime, participant.Model) {
			return "", fmt.Errorf("trace writer: invalid participant metadata")
		}
	}
	return canonicalTime("run started_at", run.StartedAt)
}

func (writer *Writer) validateFact(fact Fact, sequence int) (string, error) {
	if !tracePattern.MatchString(fact.ID) {
		return "", fmt.Errorf("trace writer: fact %d has invalid id", sequence)
	}
	if _, duplicate := writer.seen[fact.ID]; duplicate {
		return "", fmt.Errorf("trace writer: fact %d repeats id %q", sequence, fact.ID)
	}
	if !validOptionalTokens(fact.Agent, fact.Turn) || !validTraceToken(fact.Source.Node) {
		return "", fmt.Errorf("trace writer: fact %d has invalid source or runtime metadata", sequence)
	}
	classification, known := factClassifications[fact.Kind]
	if !known || classification.source != string(fact.Source.Class) ||
		classification.truth != string(fact.Truth) {
		return "", fmt.Errorf("trace writer: fact %d has invalid kind/source/truth classification", sequence)
	}
	if err := writer.validateCauses(fact.Causes, sequence); err != nil {
		return "", err
	}
	if err := validateReferences(fact.References, sequence); err != nil {
		return "", err
	}
	if err := validateFactFields(fact.Fields, sequence); err != nil {
		return "", err
	}
	if err := validateKindEvidence(fact, sequence); err != nil {
		return "", err
	}
	return canonicalTime(fmt.Sprintf("fact %d captured_at", sequence), fact.CapturedAt)
}

type kindEvidenceRule struct {
	label string
	valid func(Fact) bool
}

var kindEvidenceRules = map[string]kindEvidenceRule{
	"runtime.domain.operation": {"domain operation observation", validDomainOperationEvidence},
	"runtime.view.received":    {"Agent View structural projection", validRuntimeViewEvidence},
	"runtime.intent.denied":    {"Intent denial observation", validIntentDenialEvidence},
	"r7.event.accepted":        {"accepted Event evidence", validAcceptedEventEvidence},
	"r7.handling.resolved":     {"terminal Handling evidence", validResolvedHandlingEvidence},
	"test.gate.checked":        {"gate assertion evidence", validGateAssertionEvidence},
	"test.attention.wave":      {"attention wave evidence", validAttentionSnapshotEvidence},
	"test.attention.outcome":   {"attention outcome evidence", validOutcomeAttentionEvidence},
	"test.attention.exhausted": {"attention exhaustion evidence", validExhaustedAttentionEvidence},
	"test.attention.quiescent": {"attention quiescence evidence", validQuiescentAttentionEvidence},
	"test.attention.occupied":  {"occupied attention boundary evidence", validOccupiedAttentionEvidence},
}

func validRuntimeViewEvidence(fact Fact) bool {
	fields := fact.Fields
	if fields.Action != "current" ||
		fields.HasCurrent == nil || fields.OpenTotal == nil || fields.RelatedTotal == nil ||
		fields.RelatedProjected == nil || fields.Truncated == nil ||
		*fields.OpenTotal < 0 || *fields.OpenTotal > 64 ||
		*fields.RelatedTotal < 0 || *fields.RelatedTotal > 128 ||
		*fields.RelatedProjected < 0 || *fields.RelatedProjected > 1 ||
		*fields.RelatedProjected > *fields.RelatedTotal ||
		*fields.Truncated != (*fields.RelatedProjected < *fields.RelatedTotal) ||
		*fields.HasCurrent != (fields.ReplyRequired != nil) {
		return false
	}
	return !*fields.HasCurrent || *fields.OpenTotal > 0
}

func validDomainOperationEvidence(fact Fact) bool {
	return len(fact.Causes) == 0 &&
		slices.Contains([]string{"read", "probe", "mutation"}, fact.Fields.Action) &&
		hasAllOperationCounts(fact.Fields) && *fact.Fields.AttemptCount > 0 &&
		operationCountSum(fact.Fields) == *fact.Fields.AttemptCount
}

func hasAllOperationCounts(fields FactFields) bool {
	return fields.AttemptCount != nil && fields.SuccessCount != nil &&
		fields.ToolErrorCount != nil && fields.InvalidCount != nil && fields.BatchedCount != nil
}

func operationCountSum(fields FactFields) int {
	return *fields.SuccessCount + *fields.ToolErrorCount + *fields.InvalidCount +
		*fields.BatchedCount
}

func validIntentDenialEvidence(fact Fact) bool {
	return len(fact.Causes) == 0 && fact.Fields.Action == "submit" &&
		fact.Fields.Count != nil && *fact.Fields.Count > 0 &&
		slices.Contains(intentDenialCodes, fact.Fields.Code)
}

func validateKindEvidence(fact Fact, sequence int) error {
	rule, constrained := kindEvidenceRules[fact.Kind]
	if !constrained || rule.valid(fact) {
		return nil
	}
	return fmt.Errorf("trace writer: fact %d %s is required for %s",
		sequence, rule.label, fact.Kind)
}

func validAcceptedEventEvidence(fact Fact) bool {
	return fact.References.Event != "" && fact.References.EventDigest != "" &&
		fact.Fields.SemanticKind != "" && fact.Fields.Consequence != ""
}

func validResolvedHandlingEvidence(fact Fact) bool {
	return fact.References.Handling != "" && fact.Fields.State == "terminal" &&
		slices.Contains([]string{"completed", "declined", "unresolved"}, fact.Fields.Outcome)
}

func validAttentionSnapshotEvidence(fact Fact) bool {
	return validAttentionFields(fact) && *fact.Fields.OccupiedClaims == 0
}

func validOutcomeAttentionEvidence(fact Fact) bool {
	return validFinalAttentionEvidence(fact, true) && *fact.Fields.OccupiedClaims == 0
}

func validExhaustedAttentionEvidence(fact Fact) bool {
	return validFinalAttentionEvidence(fact, false) && *fact.Fields.OccupiedClaims == 0
}

func validQuiescentAttentionEvidence(fact Fact) bool {
	return validExhaustedAttentionEvidence(fact) && *fact.Fields.OpenUnclaimed == 0
}

func validOccupiedAttentionEvidence(fact Fact) bool {
	return validAttentionFields(fact) && fact.Fields.GoalDigest == "" &&
		fact.Fields.GoalSatisfied == nil
}

func validGateAssertionEvidence(fact Fact) bool {
	return validTraceToken(fact.Fields.GateID) && slices.Contains([]string{
		string(GatePass), string(GateFail), string(GateUnknown), string(GateNotApplicable),
	}, fact.Fields.Status)
}

func validFinalAttentionEvidence(fact Fact, goalSatisfied bool) bool {
	return validFinalAttentionFields(fact) && *fact.Fields.GoalSatisfied == goalSatisfied
}

func validFinalAttentionFields(fact Fact) bool {
	return validAttentionFields(fact) && digestPattern.MatchString(fact.Fields.GoalDigest) &&
		fact.Fields.GoalSatisfied != nil
}

func validAttentionFields(fact Fact) bool {
	return len(fact.Causes) == 0 && fact.Fields.Episode != "" && fact.Fields.Role != "" &&
		fact.Fields.Round != nil && *fact.Fields.Round > 0 &&
		fact.Fields.OpenUnclaimed != nil && fact.Fields.OccupiedClaims != nil &&
		fact.Fields.TurnLimit != nil && fact.Fields.TurnsUsed != nil &&
		*fact.Fields.TurnLimit > 0 &&
		*fact.Fields.TurnsUsed >= 0 && *fact.Fields.TurnsUsed <= *fact.Fields.TurnLimit
}

func (writer *Writer) validateCauses(causes []string, sequence int) error {
	if len(causes) > 16 {
		return fmt.Errorf("trace writer: fact %d causes exceed 16", sequence)
	}
	unique := make(map[string]struct{}, len(causes))
	for _, cause := range causes {
		if !tracePattern.MatchString(cause) {
			return fmt.Errorf("trace writer: fact %d has invalid cause", sequence)
		}
		if _, duplicate := unique[cause]; duplicate {
			return fmt.Errorf("trace writer: fact %d repeats cause %q", sequence, cause)
		}
		if _, exists := writer.seen[cause]; !exists {
			return fmt.Errorf("trace writer: fact %d cause %q is not an earlier fact", sequence, cause)
		}
		unique[cause] = struct{}{}
	}
	return nil
}

func validateReferences(refs References, sequence int) error {
	for _, value := range []string{refs.Artifact, refs.EventDigest} {
		if value != "" && !digestPattern.MatchString(value) {
			return fmt.Errorf("trace writer: fact %d has invalid digest reference", sequence)
		}
	}
	if !validOptionalTokens(refs.Correlation, refs.Delivery, refs.Event,
		refs.Handling, refs.Principal, refs.ReferenceHead) {
		return fmt.Errorf("trace writer: fact %d has invalid reference", sequence)
	}
	return nil
}

func validateFactFields(fields FactFields, sequence int) error {
	checks := []struct {
		name    string
		value   string
		allowed []string
	}{
		{"action", fields.Action, []string{"current", "submit", "capture", "read", "probe", "mutation", "other"}},
		{"consequence", fields.Consequence, []string{
			"handling.create", "handling.advance", "handling.resolve.completed",
			"handling.resolve.declined", "handling.resolve.unresolved", "reference.publish",
			"reference.supersede", "reference.retract", "observation.completed",
			"observation.declined", "observation.unresolved",
		}},
		{"outcome", fields.Outcome, []string{"accepted", "rejected", "replayed", "completed", "declined", "unresolved"}},
		{"state", fields.State, []string{"open", "active", "pending", "settled", "expired", "retracted", "terminal"}},
		{"status", fields.Status, []string{"pass", "fail", "incomplete", "unknown", "not_applicable"}},
	}
	for _, check := range checks {
		if check.value != "" && !slices.Contains(check.allowed, check.value) {
			return fmt.Errorf("trace writer: fact %d has invalid %s", sequence, check.name)
		}
	}
	if err := validateFactMetadata(fields, sequence); err != nil {
		return err
	}
	if fields.TargetCount != nil && *fields.TargetCount != len(fields.Targets) {
		return fmt.Errorf("trace writer: fact %d has inconsistent target metadata", sequence)
	}
	integers := []struct {
		name    string
		value   *int
		minimum int
		maximum int
	}{
		{"artifact_count", fields.ArtifactCount, 0, 64},
		{"attempt_count", fields.AttemptCount, 0, 256},
		{"batched_unattributed_count", fields.BatchedCount, 0, 256},
		{"count", fields.Count, 1, 256},
		{"invalid_result_count", fields.InvalidCount, 0, 256},
		{"occupied_claims", fields.OccupiedClaims, 0, 64},
		{"open_total", fields.OpenTotal, 0, 64},
		{"open_unclaimed", fields.OpenUnclaimed, 0, 64},
		{"round", fields.Round, 0, 1024},
		{"related_projected", fields.RelatedProjected, 0, 1},
		{"related_total", fields.RelatedTotal, 0, 128},
		{"payload_bytes", fields.PayloadBytes, 0, 32 << 10},
		{"success_count", fields.SuccessCount, 0, 256},
		{"tool_error_count", fields.ToolErrorCount, 0, 256},
		{"target_count", fields.TargetCount, 0, 16},
		{"turn_limit", fields.TurnLimit, 1, 256}, {"turns_used", fields.TurnsUsed, 0, 256},
	}
	for _, value := range integers {
		if value.value != nil && (*value.value < value.minimum || *value.value > value.maximum) {
			return fmt.Errorf("trace writer: fact %d has out-of-bound %s", sequence, value.name)
		}
	}
	operationCounts := []*int{fields.AttemptCount, fields.SuccessCount, fields.ToolErrorCount,
		fields.InvalidCount, fields.BatchedCount}
	present := 0
	for _, value := range operationCounts {
		if value != nil {
			present++
		}
	}
	if present != 0 && (present != len(operationCounts) ||
		operationCountSum(fields) != *fields.AttemptCount) {
		return fmt.Errorf("trace writer: fact %d has inconsistent operation counts", sequence)
	}
	if !validInt64(fields.ByteSize, 0, 16<<20) {
		return fmt.Errorf("trace writer: fact %d has out-of-bound byte_size", sequence)
	}
	if !validInt64(fields.DurationMillis, 0, 3600000) {
		return fmt.Errorf("trace writer: fact %d has out-of-bound duration_ms", sequence)
	}
	return nil
}

func canonicalTime(label string, value time.Time) (string, error) {
	if value.IsZero() {
		return "", fmt.Errorf("trace writer: %s is required", label)
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	if len(formatted) > 35 {
		return "", fmt.Errorf("trace writer: %s exceeds timestamp bound", label)
	}
	return formatted, nil
}

func validOptionalTokens(values ...string) bool {
	for _, value := range values {
		if value != "" && !validTraceToken(value) {
			return false
		}
	}
	return true
}

func validTraceToken(value string) bool { return tokenPattern.MatchString(value) }

func validInt64(value *int64, minimum, maximum int64) bool {
	return value == nil || (*value >= minimum && *value <= maximum)
}
