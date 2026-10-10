package agency

const (
	AgentReceiptSchema            = "memory.agent.receipt"
	AgentReceiptVersion           = 1
	MaxAgentReceiptCanonicalBytes = 4 << 10
)

// AgentReceipt is the bounded response shown to an Agent. The durable Receipt
// remains private because it contains the operation key, request digest,
// timestamp, and accepted Event identity. Replayed is response metadata only;
// it is never persisted as a third outcome.
type AgentReceipt struct {
	outcome    ReceiptOutcome
	replayed   bool
	diagnostic string
	canonical  []byte
}

// ProjectAgentReceipt creates the model-safe projection of a durable Receipt.
// It intentionally exposes no Event, subject, authority, operation, digest, or
// timestamp handle.
func ProjectAgentReceipt(receipt Receipt, replayed bool) (AgentReceipt, error) {
	parsed, err := ParseReceiptCanonicalJSON(receipt.CanonicalJSON())
	if err != nil || parsed.Digest() != receipt.Digest() {
		return AgentReceipt{}, invariant("Agent Receipt", "source Receipt is not an intact canonical value")
	}
	projected := AgentReceipt{outcome: receipt.outcome, replayed: replayed, diagnostic: receipt.diagnostic}
	wire := agentReceiptWire{
		Schema: AgentReceiptSchema, Version: AgentReceiptVersion,
		Outcome: receipt.outcome.String(), Replayed: replayed,
	}
	if receipt.outcome == ReceiptOutcomeRejected {
		wire.Diagnostic = receipt.diagnostic
	}
	canonical, _, err := canonicalJSON(wire)
	if err != nil {
		return AgentReceipt{}, err
	}
	if len(canonical) > MaxAgentReceiptCanonicalBytes {
		return AgentReceipt{}, limit("Agent Receipt canonical bytes", len(canonical), MaxAgentReceiptCanonicalBytes)
	}
	projected.canonical = canonical
	return projected, nil
}

func (receipt AgentReceipt) Outcome() ReceiptOutcome { return receipt.outcome }
func (receipt AgentReceipt) Replayed() bool          { return receipt.replayed }
func (receipt AgentReceipt) Diagnostic() string      { return receipt.diagnostic }
func (receipt AgentReceipt) CanonicalJSON() []byte   { return copyBytes(receipt.canonical) }

type agentReceiptWire struct {
	Schema     string `json:"schema"`
	Version    int    `json:"version"`
	Outcome    string `json:"outcome"`
	Replayed   bool   `json:"replayed"`
	Diagnostic string `json:"diagnostic,omitempty"`
}
