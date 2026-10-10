package agency

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

const (
	MaxPeerDeliveryCanonicalBytes = 32 << 10
	MaxPeerCausalDepth            = 32
	MaxPeerDeliveryTTL            = 24 * time.Hour

	peerDeliveryIDDomain        = "memory.peer-delivery-id.v1"
	peerDeliveryEnvelopeDomain  = "memory.peer-delivery-envelope.v1"
	peerDeliverySignatureDomain = "memory.peer-delivery-signature.v1"
)

// DeliveryID is the stable replay identity of one peer delivery. It binds the
// origin Event, a machine-only enrolled route identity, and the opaque target
// alias. The route participates in derivation but is never stored in or
// projected from PeerDelivery.
type DeliveryID struct{ digest Digest }

func ParseDeliveryID(value string) (DeliveryID, error) {
	if !strings.HasPrefix(value, "delivery:") {
		return DeliveryID{}, invalid("DeliveryID", "must use delivery:<lowercase-hex>")
	}
	digest, err := ParseDigest("sha256:" + strings.TrimPrefix(value, "delivery:"))
	if err != nil {
		return DeliveryID{}, invalid("DeliveryID", "must use 64 lowercase hexadecimal characters")
	}
	return DeliveryID{digest: digest}, nil
}

func (id DeliveryID) String() string { return "delivery:" + hex.EncodeToString(id.digest[:]) }
func (id DeliveryID) IsZero() bool   { return id.digest.IsZero() }

// PeerDeliverySpec contains only immutable origin provenance, bounded
// semantics, content references, causal depth, and expiry. OriginAcceptedAt is
// signed provenance and the TTL basis because the outbox is created atomically
// with the origin Event; it is not used to order clocks across nodes. The spec
// deliberately contains no bytes, PeerID, route, credential, or receiving-node
// authority.
type PeerDeliverySpec struct {
	OriginEvent       EventRef
	OriginSequence    uint64
	OriginAcceptedAt  time.Time
	OriginSource      AgentPrincipalID
	OriginConsequence Consequence
	OriginTargetCount uint8
	OriginCausation   []EventRef
	OriginCorrelation EventRef
	InReplyToDelivery DeliveryID
	TargetAlias       OpaqueHandle
	Kind              SemanticLabel
	Payload           SemanticPayload
	Artifacts         []Digest
	CausalDepth       uint16
	ExpiresAt         time.Time
}

// PeerDelivery is the canonical signed-envelope content for one remote
// candidate. An enrolled route is required to construct its ID, but the route
// is not retained in this value or its canonical encoding.
type PeerDelivery struct {
	id                DeliveryID
	originEvent       EventRef
	originSequence    uint64
	originAcceptedAt  time.Time
	originSource      AgentPrincipalID
	originConsequence Consequence
	originTargetCount uint8
	originCausation   []EventRef
	originCorrelation EventRef
	inReplyToDelivery DeliveryID
	targetAlias       OpaqueHandle
	kind              SemanticLabel
	payload           SemanticPayload
	artifacts         []Digest
	causalDepth       uint16
	expiresAt         time.Time
	canonical         []byte
	envelopeDigest    Digest
}

func NewPeerDelivery(enrolledRoute RouteID, spec PeerDeliverySpec) (PeerDelivery, error) {
	if enrolledRoute.IsZero() {
		return PeerDelivery{}, invalid("PeerDelivery route context", "enrolled route is required")
	}
	if err := validatePeerDeliveryRequiredShape(spec); err != nil {
		return PeerDelivery{}, err
	}
	if spec.CausalDepth > MaxPeerCausalDepth {
		return PeerDelivery{}, limit("PeerDelivery causal depth", int(spec.CausalDepth), MaxPeerCausalDepth)
	}
	originAcceptedAt, err := canonicalTime("PeerDelivery origin accepted time", spec.OriginAcceptedAt)
	if err != nil {
		return PeerDelivery{}, err
	}
	expiresAt, err := canonicalTime("PeerDelivery expiry", spec.ExpiresAt)
	if err != nil {
		return PeerDelivery{}, err
	}
	if !expiresAt.After(originAcceptedAt) {
		return PeerDelivery{}, invariant("PeerDelivery expiry", "must be after origin Event acceptance")
	}
	if expiresAt.Sub(originAcceptedAt) > MaxPeerDeliveryTTL {
		return PeerDelivery{}, limit("PeerDelivery TTL", int(expiresAt.Sub(originAcceptedAt)), int(MaxPeerDeliveryTTL))
	}
	causation, err := normalizePeerCausation(spec.OriginEvent, spec.OriginCausation, spec.OriginCorrelation)
	if err != nil {
		return PeerDelivery{}, err
	}
	artifacts, err := normalizePeerArtifacts(spec.Artifacts)
	if err != nil {
		return PeerDelivery{}, err
	}
	id, err := deriveDeliveryID(enrolledRoute, spec.OriginEvent, spec.TargetAlias)
	if err != nil {
		return PeerDelivery{}, err
	}
	delivery := PeerDelivery{
		id: id, originEvent: spec.OriginEvent, originSequence: spec.OriginSequence,
		originAcceptedAt: originAcceptedAt, originSource: spec.OriginSource,
		originConsequence: spec.OriginConsequence, originTargetCount: spec.OriginTargetCount,
		originCausation: causation, originCorrelation: spec.OriginCorrelation,
		inReplyToDelivery: spec.InReplyToDelivery,
		targetAlias:       spec.TargetAlias, kind: spec.Kind, payload: spec.Payload,
		artifacts: artifacts, causalDepth: spec.CausalDepth, expiresAt: expiresAt,
	}
	canonical, _, err := canonicalJSON(delivery.wire())
	if err != nil {
		return PeerDelivery{}, err
	}
	if len(canonical) > MaxPeerDeliveryCanonicalBytes {
		return PeerDelivery{}, limit("PeerDelivery canonical bytes", len(canonical), MaxPeerDeliveryCanonicalBytes)
	}
	delivery.canonical = canonical
	delivery.envelopeDigest = domainSeparatedDigest(peerDeliveryEnvelopeDomain, canonical)
	return delivery, nil
}

func validatePeerDeliveryRequiredShape(spec PeerDeliverySpec) error {
	if spec.OriginEvent.IsZero() || spec.OriginSequence == 0 || spec.OriginSource.IsZero() ||
		!spec.OriginConsequence.agentDeclarable() || spec.OriginTargetCount == 0 ||
		int(spec.OriginTargetCount) > MaxSuccessors || spec.TargetAlias.IsZero() ||
		spec.Kind.IsZero() || spec.CausalDepth == 0 {
		return invalid("PeerDelivery",
			"complete origin effect, target, semantic kind, and positive depth are required")
	}
	soleTargetTerminal := isTerminalConsequence(spec.OriginConsequence) && spec.OriginTargetCount == 1
	if soleTargetTerminal && (spec.OriginCorrelation.IsZero() || spec.InReplyToDelivery.IsZero()) {
		return invariant("PeerDelivery terminal reply",
			"single-target terminal origin requires correlation and in-reply-to Delivery")
	}
	if !soleTargetTerminal && !spec.InReplyToDelivery.IsZero() {
		return invariant("PeerDelivery terminal reply",
			"in-reply-to Delivery is forbidden outside a single-target terminal origin")
	}
	return nil
}

func normalizePeerCausation(origin EventRef, values []EventRef, correlation EventRef) ([]EventRef, error) {
	if len(values) > MaxCausationHandles {
		return nil, limit("PeerDelivery origin causation", len(values), MaxCausationHandles)
	}
	result := append([]EventRef(nil), values...)
	sort.Slice(result, func(i, j int) bool { return eventRefLess(result[i], result[j]) })
	for index, event := range result {
		if event.IsZero() || event == origin || (index > 0 && event == result[index-1]) {
			return nil, invalid("PeerDelivery origin causation", "contains a zero, self, or duplicate Event reference")
		}
	}
	if !correlation.IsZero() && correlation == origin {
		return nil, invalid("PeerDelivery origin correlation", "must not refer to the origin Event itself")
	}
	return result, nil
}

func normalizePeerArtifacts(values []Digest) ([]Digest, error) {
	if len(values) > MaxArtifactInputs {
		return nil, limit("PeerDelivery Artifacts", len(values), MaxArtifactInputs)
	}
	result := append([]Digest(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	for index, digest := range result {
		if digest.IsZero() || (index > 0 && digest == result[index-1]) {
			return nil, invalid("PeerDelivery Artifacts", "contains a zero or duplicate digest")
		}
	}
	return result, nil
}

func eventRefLess(left, right EventRef) bool {
	if left.ID().String() != right.ID().String() {
		return left.ID().String() < right.ID().String()
	}
	return left.Digest().String() < right.Digest().String()
}

func deriveDeliveryID(route RouteID, origin EventRef, target OpaqueHandle) (DeliveryID, error) {
	if route.IsZero() || origin.IsZero() || target.IsZero() {
		return DeliveryID{}, invalid("DeliveryID derivation", "route, origin Event, and target alias are required")
	}
	wire := peerDeliveryIDWire{SchemaVersion: 1, OriginEvent: origin.canonical().(eventRefWire),
		EnrolledRoute: route.String(), TargetAlias: target.String()}
	canonical, _, err := canonicalJSON(wire)
	if err != nil {
		return DeliveryID{}, err
	}
	return DeliveryID{digest: domainSeparatedDigest(peerDeliveryIDDomain, canonical)}, nil
}

func domainSeparatedDigest(domain string, value []byte) Digest {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(value)
	var digest Digest
	copy(digest[:], hash.Sum(nil))
	return digest
}

func signingMessage(domain string, digest Digest) []byte {
	return []byte(domain + "\x00" + digest.String())
}

func (delivery PeerDelivery) ID() DeliveryID                 { return delivery.id }
func (delivery PeerDelivery) OriginEvent() EventRef          { return delivery.originEvent }
func (delivery PeerDelivery) OriginSequence() uint64         { return delivery.originSequence }
func (delivery PeerDelivery) OriginAcceptedAt() time.Time    { return delivery.originAcceptedAt }
func (delivery PeerDelivery) OriginSource() AgentPrincipalID { return delivery.originSource }
func (delivery PeerDelivery) OriginConsequence() Consequence { return delivery.originConsequence }
func (delivery PeerDelivery) OriginTargetCount() int         { return int(delivery.originTargetCount) }
func (delivery PeerDelivery) OriginCausation() []EventRef {
	return append([]EventRef(nil), delivery.originCausation...)
}
func (delivery PeerDelivery) OriginCorrelation() (EventRef, bool) {
	return delivery.originCorrelation, !delivery.originCorrelation.IsZero()
}
func (delivery PeerDelivery) InReplyToDelivery() (DeliveryID, bool) {
	return delivery.inReplyToDelivery, !delivery.inReplyToDelivery.IsZero()
}
func (delivery PeerDelivery) TargetAlias() OpaqueHandle { return delivery.targetAlias }
func (delivery PeerDelivery) Kind() SemanticLabel       { return delivery.kind }
func (delivery PeerDelivery) Payload() SemanticPayload  { return delivery.payload }
func (delivery PeerDelivery) Artifacts() []Digest {
	return append([]Digest(nil), delivery.artifacts...)
}
func (delivery PeerDelivery) CausalDepth() uint16    { return delivery.causalDepth }
func (delivery PeerDelivery) ExpiresAt() time.Time   { return delivery.expiresAt }
func (delivery PeerDelivery) CanonicalJSON() []byte  { return copyBytes(delivery.canonical) }
func (delivery PeerDelivery) EnvelopeDigest() Digest { return delivery.envelopeDigest }
func (delivery PeerDelivery) SigningMessage() []byte {
	if delivery.envelopeDigest.IsZero() {
		return nil
	}
	return signingMessage(peerDeliverySignatureDomain, delivery.envelopeDigest)
}

// RequiresTerminalReplyMatch reports the sole source-Event shape that may
// have used R7's terminal-reply exception: a terminal consequence with exactly
// one successor. It is signed origin evidence, not sufficient receiver
// authority; peer admission must still match the exact correlation to an open
// local responsibility for the resolved target Principal.
func (delivery PeerDelivery) RequiresTerminalReplyMatch() bool {
	return !delivery.inReplyToDelivery.IsZero()
}

func (delivery PeerDelivery) clone() PeerDelivery {
	delivery.originCausation = append([]EventRef(nil), delivery.originCausation...)
	delivery.artifacts = append([]Digest(nil), delivery.artifacts...)
	delivery.canonical = copyBytes(delivery.canonical)
	return delivery
}

func (delivery PeerDelivery) wireCanonical() []byte {
	canonical, _, err := canonicalJSON(delivery.wire())
	if err != nil {
		return nil
	}
	return canonical
}

type peerDeliveryIDWire struct {
	SchemaVersion int          `json:"schema_version"`
	OriginEvent   eventRefWire `json:"origin_event"`
	EnrolledRoute string       `json:"enrolled_route"`
	TargetAlias   string       `json:"target_alias"`
}

type peerDeliveryWire struct {
	SchemaVersion     int                    `json:"schema_version"`
	DeliveryID        string                 `json:"delivery_id"`
	Origin            peerDeliveryOriginWire `json:"origin"`
	TargetAlias       string                 `json:"target_alias"`
	Kind              string                 `json:"kind"`
	Payload           string                 `json:"payload"`
	Artifacts         []string               `json:"artifacts,omitempty"`
	InReplyToDelivery string                 `json:"in_reply_to_delivery_id,omitempty"`
	CausalDepth       uint16                 `json:"causal_depth"`
	ExpiresAt         string                 `json:"expires_at"`
}

type peerDeliveryOriginWire struct {
	Event       eventRefWire   `json:"event"`
	Sequence    uint64         `json:"sequence"`
	AcceptedAt  string         `json:"accepted_at"`
	Source      string         `json:"source_principal"`
	Consequence string         `json:"consequence"`
	TargetCount uint8          `json:"target_count"`
	Causation   []eventRefWire `json:"causation,omitempty"`
	Correlation *eventRefWire  `json:"correlation,omitempty"`
}

func (delivery PeerDelivery) wire() peerDeliveryWire {
	wire := peerDeliveryWire{
		SchemaVersion: 3, DeliveryID: delivery.id.String(), TargetAlias: delivery.targetAlias.String(),
		Kind: delivery.kind.String(), Payload: delivery.payload.String(), CausalDepth: delivery.causalDepth,
		ExpiresAt: delivery.expiresAt.Format(time.RFC3339Nano),
		Origin: peerDeliveryOriginWire{
			Event: delivery.originEvent.canonical().(eventRefWire), Sequence: delivery.originSequence,
			AcceptedAt: delivery.originAcceptedAt.Format(time.RFC3339Nano), Source: delivery.originSource.String(),
			Consequence: delivery.originConsequence.String(), TargetCount: delivery.originTargetCount,
		},
	}
	if !delivery.inReplyToDelivery.IsZero() {
		wire.InReplyToDelivery = delivery.inReplyToDelivery.String()
	}
	for _, event := range delivery.originCausation {
		wire.Origin.Causation = append(wire.Origin.Causation, event.canonical().(eventRefWire))
	}
	if !delivery.originCorrelation.IsZero() {
		correlation := delivery.originCorrelation.canonical().(eventRefWire)
		wire.Origin.Correlation = &correlation
	}
	for _, digest := range delivery.artifacts {
		wire.Artifacts = append(wire.Artifacts, digest.String())
	}
	return wire
}
