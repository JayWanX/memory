package main

type turnSummary struct {
	Role                     string                  `json:"role"`
	Turn                     string                  `json:"turn"`
	CapturedAt               string                  `json:"captured_at"`
	HookCues                 int                     `json:"hook_cues"`
	BashCalls                int                     `json:"bash_calls"`
	DelegateCalls            int                     `json:"delegate_calls"`
	CurrentReads             int                     `json:"current_reads"`
	View                     *agentViewSummary       `json:"view,omitempty"`
	DomainOperations         domainOperationsSummary `json:"domain_operations"`
	SubmitAttempts           int                     `json:"submit_attempts"`
	IntentSubmits            int                     `json:"intent_submits"`
	AcceptedReceipts         int                     `json:"accepted_receipts"`
	AcceptedEvents           []acceptedEventSummary  `json:"accepted_events"`
	RejectedReceipts         int                     `json:"rejected_receipts"`
	SubmitDenials            int                     `json:"submit_denials"`
	SubmitInvocationFailures int                     `json:"submit_invocation_failures"`
	SubmitControlDenials     []controlDenial         `json:"submit_control_denials"`
	PostAcceptDenials        int                     `json:"post_accept_denials"`
	PrivateBindingProbes     int                     `json:"private_binding_probes"`
	AgentEnd                 bool                    `json:"agent_end"`
}

// agentViewSummary retains only the bounded structural shape needed to
// explain one Runtime attention opportunity. Handles, identities, semantic
// content, and private authority never enter the sanitized report.
type agentViewSummary struct {
	HasCurrent       bool  `json:"has_current"`
	ReplyRequired    *bool `json:"reply_required,omitempty"`
	ReplyPending     *bool `json:"reply_observation_pending,omitempty"`
	OpenTotal        int   `json:"open_total"`
	RelatedTotal     int   `json:"related_total"`
	RelatedProjected int   `json:"related_projected"`
	Truncated        bool  `json:"truncated"`
}

type acceptedEventSummary struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type domainOperationsSummary struct {
	Read     domainOperationSummary `json:"read"`
	Probe    domainOperationSummary `json:"probe"`
	Mutation domainOperationSummary `json:"mutation"`
}

type domainOperationSummary struct {
	Attempts       int `json:"attempts"`
	Successes      int `json:"successes"`
	ToolErrors     int `json:"tool_errors"`
	InvalidResults int `json:"invalid_results"`
	Batched        int `json:"batched_unattributed"`
}
