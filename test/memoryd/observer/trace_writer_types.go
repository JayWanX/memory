package observer

import "time"

// SourceClass identifies which boundary observed or committed a Fact.
type SourceClass string

const (
	SourceRuntime     SourceClass = "runtime"
	SourceR7Authority SourceClass = "r7_authority"
	SourceTransport   SourceClass = "transport"
	SourceOracle      SourceClass = "oracle"
	SourceRunner      SourceClass = "runner"
)

// TruthClass describes the evidentiary status of a Fact. It does not rank or
// reinterpret the fact's semantics.
type TruthClass string

const (
	TruthObservation       TruthClass = "observation"
	TruthAcceptedLocalFact TruthClass = "accepted_local_fact"
	TruthDerivedProjection TruthClass = "derived_projection"
	TruthAssertion         TruthClass = "assertion"
)

// ResultStatus is the terminal integrity status of one trace file.
type ResultStatus string

const (
	ResultPassed     ResultStatus = "passed"
	ResultFailed     ResultStatus = "failed"
	ResultIncomplete ResultStatus = "incomplete"
)

// GateStatus is the outcome reported by an independent test oracle.
type GateStatus string

const (
	GatePass          GateStatus = "pass"
	GateFail          GateStatus = "fail"
	GateUnknown       GateStatus = "unknown"
	GateNotApplicable GateStatus = "not_applicable"
)

// Scenario identifies the test definition whose execution produced a trace.
type Scenario struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// Participant is bounded run metadata. It never contains a prompt, message,
// transcript, credential, or tool input/output.
type Participant struct {
	Node    string `json:"node"`
	Agent   string `json:"agent,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
}

// Run is the immutable header supplied when a Writer is constructed.
type Run struct {
	ID              string
	Scenario        Scenario
	StartedAt       time.Time
	CandidateDigest string
	Participants    []Participant
}

// Source names the node and evidence boundary that produced a Fact.
type Source struct {
	Class SourceClass `json:"class"`
	Node  string      `json:"node"`
}

// References contains bounded protocol identities and content digests. It
// carries no Artifact bytes or semantic payload.
type References struct {
	Artifact      string `json:"artifact,omitempty"`
	Correlation   string `json:"correlation,omitempty"`
	Delivery      string `json:"delivery,omitempty"`
	Event         string `json:"event,omitempty"`
	EventDigest   string `json:"event_digest,omitempty"`
	Handling      string `json:"handling,omitempty"`
	Principal     string `json:"principal,omitempty"`
	ReferenceHead string `json:"reference_head,omitempty"`
}

// FactFields is the closed metadata vocabulary rendered by the observer.
// Pointer scalars distinguish an observed zero or false value from absence.
type FactFields struct {
	Action           string   `json:"action,omitempty"`
	ArtifactCount    *int     `json:"artifact_count,omitempty"`
	AttemptCount     *int     `json:"attempt_count,omitempty"`
	BatchedCount     *int     `json:"batched_unattributed_count,omitempty"`
	Authenticated    *bool    `json:"authenticated,omitempty"`
	BypassedHook     *bool    `json:"bypassed_hook,omitempty"`
	ByteSize         *int64   `json:"byte_size,omitempty"`
	Code             string   `json:"code,omitempty"`
	Count            *int     `json:"count,omitempty"`
	Consequence      string   `json:"consequence,omitempty"`
	DurationMillis   *int64   `json:"duration_ms,omitempty"`
	Episode          string   `json:"episode,omitempty"`
	GateID           string   `json:"gate_id,omitempty"`
	GoalDigest       string   `json:"goal_digest,omitempty"`
	GoalSatisfied    *bool    `json:"goal_satisfied,omitempty"`
	HasCurrent       *bool    `json:"has_current,omitempty"`
	HookCue          *bool    `json:"hook_cue,omitempty"`
	InvalidCount     *int     `json:"invalid_result_count,omitempty"`
	OccupiedClaims   *int     `json:"occupied_claims,omitempty"`
	OpenTotal        *int     `json:"open_total,omitempty"`
	OpenUnclaimed    *int     `json:"open_unclaimed,omitempty"`
	Outcome          string   `json:"outcome,omitempty"`
	PayloadBytes     *int     `json:"payload_bytes,omitempty"`
	Replayed         *bool    `json:"replayed,omitempty"`
	ReplyRequired    *bool    `json:"reply_required,omitempty"`
	RelatedProjected *int     `json:"related_projected,omitempty"`
	RelatedTotal     *int     `json:"related_total,omitempty"`
	Role             string   `json:"role,omitempty"`
	Round            *int     `json:"round,omitempty"`
	SemanticKind     string   `json:"semantic_kind,omitempty"`
	State            string   `json:"state,omitempty"`
	Status           string   `json:"status,omitempty"`
	SuccessCount     *int     `json:"success_count,omitempty"`
	TargetCount      *int     `json:"target_count,omitempty"`
	ToolErrorCount   *int     `json:"tool_error_count,omitempty"`
	Targets          []string `json:"targets,omitempty"`
	TimedOut         *bool    `json:"timed_out,omitempty"`
	Truncated        *bool    `json:"truncated,omitempty"`
	TurnLimit        *int     `json:"turn_limit,omitempty"`
	TurnsUsed        *int     `json:"turns_used,omitempty"`
	ViewNonempty     *bool    `json:"view_nonempty,omitempty"`
}

// Fact is one sanitized observation or committed effect. Sequence is assigned
// by Writer; every Cause must name an earlier Fact in the same trace.
type Fact struct {
	ID         string
	CapturedAt time.Time
	Source     Source
	Agent      string
	Turn       string
	Kind       string
	Truth      TruthClass
	Causes     []string
	References References
	Fields     FactFields
}

// Gate cites the exact Fact IDs used by an independent test oracle.
type Gate struct {
	ID       string     `json:"id"`
	Status   GateStatus `json:"status"`
	Evidence []string   `json:"evidence"`
}

// Result supplies terminal status. Writer owns record_count and trace_digest.
type Result struct {
	Status     ResultStatus
	FinishedAt time.Time
	Gates      []Gate
}
