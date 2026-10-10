package agency

import "time"

const (
	MaxPeerAdmissionReceiptCanonicalBytes = 4 << 10

	peerAdmissionReceiptDigestDomain    = "memory.peer-admission-receipt.v1"
	peerAdmissionReceiptSignatureDomain = "memory.peer-admission-receipt-signature.v1"
)

type PeerAdmissionOutcome uint8

const (
	PeerAdmissionOutcomeInvalid PeerAdmissionOutcome = iota
	PeerAdmissionOutcomeAccepted
	PeerAdmissionOutcomeRejected
)

func (outcome PeerAdmissionOutcome) String() string {
	switch outcome {
	case PeerAdmissionOutcomeAccepted:
		return "accepted"
	case PeerAdmissionOutcomeRejected:
		return "rejected"
	default:
		return ""
	}
}

// PeerAdmissionReceipt is the signed, canonical remote outcome for one exact
// delivery envelope. It reports whether the receiving authority admitted the
// candidate; transport acknowledgement is not an admission outcome. RecordedAt
// is signed remote evidence and is never compared with the origin node's clock.
type PeerAdmissionReceipt struct {
	deliveryID     DeliveryID
	envelopeDigest Digest
	outcome        PeerAdmissionOutcome
	recordedAt     time.Time
	localEvent     EventRef
	code           SemanticLabel
	diagnostic     string
	canonical      []byte
	digest         Digest
}

func NewAcceptedPeerAdmissionReceipt(delivery PeerDelivery, localEvent EventRef,
	recordedAt time.Time,
) (PeerAdmissionReceipt, error) {
	if localEvent.IsZero() {
		return PeerAdmissionReceipt{}, invalid("accepted peer admission Receipt", "local Event is required")
	}
	if localEvent == delivery.originEvent {
		return PeerAdmissionReceipt{}, invariant("accepted peer admission Receipt", "must name a new local Event")
	}
	return newPeerAdmissionReceipt(delivery, PeerAdmissionOutcomeAccepted, localEvent,
		SemanticLabel{}, "", recordedAt)
}

func NewRejectedPeerAdmissionReceipt(delivery PeerDelivery, code SemanticLabel, diagnostic string,
	recordedAt time.Time,
) (PeerAdmissionReceipt, error) {
	if code.IsZero() {
		return PeerAdmissionReceipt{}, invalid("rejected peer admission Receipt", "diagnostic code is required")
	}
	if err := validatePeerDiagnostic(diagnostic); err != nil {
		return PeerAdmissionReceipt{}, err
	}
	return newPeerAdmissionReceipt(delivery, PeerAdmissionOutcomeRejected, EventRef{},
		code, diagnostic, recordedAt)
}

func newPeerAdmissionReceipt(delivery PeerDelivery, outcome PeerAdmissionOutcome, localEvent EventRef,
	code SemanticLabel, diagnostic string, recordedAt time.Time,
) (PeerAdmissionReceipt, error) {
	if delivery.id.IsZero() || delivery.envelopeDigest.IsZero() || len(delivery.canonical) == 0 {
		return PeerAdmissionReceipt{}, invalid("peer admission Receipt", "complete delivery is required")
	}
	recorded, err := canonicalTime("peer admission Receipt recorded time", recordedAt)
	if err != nil {
		return PeerAdmissionReceipt{}, err
	}
	receipt := PeerAdmissionReceipt{
		deliveryID: delivery.id, envelopeDigest: delivery.envelopeDigest, outcome: outcome,
		recordedAt: recorded, localEvent: localEvent, code: code, diagnostic: diagnostic,
	}
	canonical, _, err := canonicalJSON(receipt.wire())
	if err != nil {
		return PeerAdmissionReceipt{}, err
	}
	if len(canonical) > MaxPeerAdmissionReceiptCanonicalBytes {
		return PeerAdmissionReceipt{}, limit("peer admission Receipt canonical bytes", len(canonical),
			MaxPeerAdmissionReceiptCanonicalBytes)
	}
	receipt.canonical = canonical
	receipt.digest = domainSeparatedDigest(peerAdmissionReceiptDigestDomain, canonical)
	return receipt, nil
}

func validatePeerDiagnostic(diagnostic string) error {
	if len(diagnostic) > MaxDiagnosticBytes {
		return limit("peer admission Receipt diagnostic", len(diagnostic), MaxDiagnosticBytes)
	}
	if _, err := NewSemanticPayload(diagnostic); err != nil {
		return invalid("peer admission Receipt diagnostic", "must be valid UTF-8 without NUL")
	}
	return nil
}

func (receipt PeerAdmissionReceipt) DeliveryID() DeliveryID        { return receipt.deliveryID }
func (receipt PeerAdmissionReceipt) EnvelopeDigest() Digest        { return receipt.envelopeDigest }
func (receipt PeerAdmissionReceipt) Outcome() PeerAdmissionOutcome { return receipt.outcome }
func (receipt PeerAdmissionReceipt) RecordedAt() time.Time         { return receipt.recordedAt }
func (receipt PeerAdmissionReceipt) LocalEvent() (EventRef, bool) {
	return receipt.localEvent, !receipt.localEvent.IsZero()
}
func (receipt PeerAdmissionReceipt) Code() SemanticLabel   { return receipt.code }
func (receipt PeerAdmissionReceipt) Diagnostic() string    { return receipt.diagnostic }
func (receipt PeerAdmissionReceipt) CanonicalJSON() []byte { return copyBytes(receipt.canonical) }
func (receipt PeerAdmissionReceipt) Digest() Digest        { return receipt.digest }
func (receipt PeerAdmissionReceipt) SigningMessage() []byte {
	if receipt.digest.IsZero() {
		return nil
	}
	return signingMessage(peerAdmissionReceiptSignatureDomain, receipt.digest)
}

type peerAdmissionReceiptWire struct {
	SchemaVersion  int           `json:"schema_version"`
	DeliveryID     string        `json:"delivery_id"`
	EnvelopeDigest string        `json:"envelope_digest"`
	Outcome        string        `json:"outcome"`
	RecordedAt     string        `json:"recorded_at"`
	LocalEvent     *eventRefWire `json:"local_event,omitempty"`
	Code           string        `json:"code,omitempty"`
	Diagnostic     string        `json:"diagnostic,omitempty"`
}

func (receipt PeerAdmissionReceipt) wire() peerAdmissionReceiptWire {
	wire := peerAdmissionReceiptWire{
		SchemaVersion: 1, DeliveryID: receipt.deliveryID.String(),
		EnvelopeDigest: receipt.envelopeDigest.String(), Outcome: receipt.outcome.String(),
		RecordedAt: receipt.recordedAt.Format(time.RFC3339Nano), Code: receipt.code.String(),
		Diagnostic: receipt.diagnostic,
	}
	if !receipt.localEvent.IsZero() {
		event := receipt.localEvent.canonical().(eventRefWire)
		wire.LocalEvent = &event
	}
	return wire
}
