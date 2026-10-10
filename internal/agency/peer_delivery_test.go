package agency

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func peerDeliveryFixture(t *testing.T, routeName string) (RouteID, PeerDelivery) {
	t.Helper()
	route := mustRoute(t, routeName)
	delivery, err := NewPeerDelivery(route, PeerDeliverySpec{
		OriginEvent:       mustEventRef(t, "event:origin", "origin event"),
		OriginSequence:    7,
		OriginAcceptedAt:  testTime,
		OriginSource:      mustPrincipal(t, "agent:origin"),
		OriginConsequence: ConsequenceCreateHandlings,
		OriginTargetCount: 2,
		OriginCausation:   []EventRef{mustEventRef(t, "event:z", "z"), mustEventRef(t, "event:a", "a")},
		OriginCorrelation: mustEventRef(t, "event:correlation", "correlation"),
		TargetAlias:       mustHandle(t, "remote/target"),
		Kind:              mustLabel(t, "opaque.request"),
		Payload:           mustPayload(t, "Process the referenced Artifact."),
		Artifacts:         []Digest{Sum([]byte("artifact-b")), Sum([]byte("artifact-a"))},
		CausalDepth:       1,
		ExpiresAt:         testTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("NewPeerDelivery() error = %v", err)
	}
	return route, delivery
}

func TestPeerDeliveryCanonicalRoundTripAndMachineRouteBinding(t *testing.T) {
	route, delivery := peerDeliveryFixture(t, "route:peer-b")
	const golden = `{"schema_version":2,"delivery_id":"delivery:7db29c73693e7caf15d30dd55472dc2d75b37da2cbbb8885e5041ad5bd195aa8","origin":{"event":{"id":"event:origin","digest":"sha256:a475a88338ff719d76e28dba4faaf6fc1f171377056082b90ff2872377201d64"},"sequence":7,"accepted_at":"2026-08-03T08:00:00Z","source_principal":"agent:origin","consequence":"handling.create","target_count":2,"causation":[{"id":"event:a","digest":"sha256:ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"},{"id":"event:z","digest":"sha256:594e519ae499312b29433b7dd8a97ff068defcba9755b6d5d00e84c524d67b06"}],"correlation":{"id":"event:correlation","digest":"sha256:577bf0c4f4ee4f887ec975d9f5356309244babf9fad24797025228a2092d78fd"}},"target_alias":"remote/target","kind":"opaque.request","payload":"Process the referenced Artifact.","artifacts":["sha256:13051e349c6f87a0f83427b2d742806fc8e01699c7429ce5b94acd0cece66dbc","sha256:6462d191923d0d849234de241cf341d949f88488593f29e3601b94bd645b7dee"],"causal_depth":1,"expires_at":"2026-08-03T09:00:00Z"}`
	if string(delivery.CanonicalJSON()) != strings.Replace(golden,
		`"schema_version":2`, `"schema_version":3`, 1) ||
		delivery.EnvelopeDigest().String() != "sha256:6bf9cf7447df0649866bbe495d950e726262339e29bcd372e68ce700f542ef9a" {
		t.Fatalf("PeerDelivery golden drift\n got: %s\ndigest: %s",
			delivery.CanonicalJSON(), delivery.EnvelopeDigest().String())
	}
	parsed, err := ParsePeerDeliveryCanonicalJSON(delivery.CanonicalJSON(), route)
	if err != nil {
		t.Fatalf("ParsePeerDeliveryCanonicalJSON() error = %v", err)
	}
	if parsed.ID() != delivery.ID() || parsed.EnvelopeDigest() != delivery.EnvelopeDigest() ||
		!bytes.Equal(parsed.CanonicalJSON(), delivery.CanonicalJSON()) ||
		!bytes.Equal(parsed.SigningMessage(), delivery.SigningMessage()) ||
		parsed.Delivery().OriginConsequence() != ConsequenceCreateHandlings ||
		parsed.Delivery().OriginTargetCount() != 2 || parsed.Delivery().RequiresTerminalReplyMatch() {
		t.Fatalf("parsed delivery differs\n got: %s\nwant: %s", parsed.CanonicalJSON(), delivery.CanonicalJSON())
	}
	if bytes.Contains(delivery.CanonicalJSON(), []byte(route.String())) {
		t.Fatalf("canonical PeerDelivery leaked enrolled route: %s", delivery.CanonicalJSON())
	}
	for _, forbidden := range []string{"peer_id", "credential", "local_principal", "route"} {
		if bytes.Contains(delivery.CanonicalJSON(), []byte(`"`+forbidden+`"`)) {
			t.Fatalf("canonical PeerDelivery contains forbidden field %q", forbidden)
		}
	}
	if _, err := ParsePeerDeliveryCanonicalJSON(delivery.CanonicalJSON(), mustRoute(t, "route:other")); !errors.Is(err, ErrInvariant) {
		t.Fatalf("wrong enrolled route error = %v, want ErrInvariant", err)
	}

	_, same := peerDeliveryFixture(t, "route:peer-b")
	if same.ID() != delivery.ID() || same.EnvelopeDigest() != delivery.EnvelopeDigest() {
		t.Fatal("same inputs did not reproduce stable delivery identity")
	}
	_, otherRoute := peerDeliveryFixture(t, "route:peer-c")
	if otherRoute.ID() == delivery.ID() {
		t.Fatal("machine-only enrolled route did not domain-bind DeliveryID")
	}
}

func TestPeerDeliveryIDAndEnvelopeHaveSeparateDomains(t *testing.T) {
	route, original := peerDeliveryFixture(t, "route:domains")
	changed, err := NewPeerDelivery(route, PeerDeliverySpec{
		OriginEvent: original.OriginEvent(), OriginSequence: original.OriginSequence(),
		OriginAcceptedAt: original.OriginAcceptedAt(), OriginSource: original.OriginSource(),
		OriginConsequence: original.OriginConsequence(), OriginTargetCount: uint8(original.OriginTargetCount()),
		OriginCausation: original.OriginCausation(), TargetAlias: original.TargetAlias(),
		Kind: original.Kind(), Payload: mustPayload(t, "Different bounded semantics."),
		Artifacts: original.Artifacts(), CausalDepth: original.CausalDepth(), ExpiresAt: original.ExpiresAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID() != original.ID() {
		t.Fatal("immutable envelope content changed stable logical DeliveryID")
	}
	if changed.EnvelopeDigest() == original.EnvelopeDigest() {
		t.Fatal("semantic change did not change envelope digest")
	}
	if original.EnvelopeDigest() == Sum(original.CanonicalJSON()) {
		t.Fatal("envelope digest lacks its domain separator")
	}
	if bytes.Equal(original.SigningMessage(), original.CanonicalJSON()) ||
		!bytes.HasPrefix(original.SigningMessage(), []byte(peerDeliverySignatureDomain+"\x00")) {
		t.Fatal("delivery signing message is not independently domain separated")
	}
}

func TestPeerDeliveryDefensiveCopies(t *testing.T) {
	_, delivery := peerDeliveryFixture(t, "route:copies")
	canonical := delivery.CanonicalJSON()
	causation := delivery.OriginCausation()
	artifacts := delivery.Artifacts()
	message := delivery.SigningMessage()
	canonical[0], causation[0], artifacts[0], message[0] = '!', EventRef{}, Digest{}, '!'
	if delivery.CanonicalJSON()[0] == '!' || delivery.OriginCausation()[0].IsZero() ||
		delivery.Artifacts()[0].IsZero() || delivery.SigningMessage()[0] == '!' {
		t.Fatal("PeerDelivery exposed mutable internal storage")
	}
}

func TestPeerDeliveryRejectsMalformedAndNoncanonicalJSON(t *testing.T) {
	route, delivery := peerDeliveryFixture(t, "route:strict")
	canonical := delivery.CanonicalJSON()
	unknown := bytes.Replace(canonical, []byte("{"), []byte(`{"unknown":true,`), 1)
	duplicate := bytes.Replace(canonical, []byte(`"causal_depth":1`),
		[]byte(`"causal_depth":1,"causal_depth":2`), 1)
	nestedUnknown := bytes.Replace(canonical, []byte(`"event":{`), []byte(`"event":{"unknown":true,`), 1)
	nonUTC := bytes.Replace(canonical, []byte(testTime.Format(time.RFC3339Nano)),
		[]byte("2026-08-03T16:00:00+08:00"), 1)
	upperDigest := bytes.Replace(canonical, []byte(delivery.OriginEvent().Digest().String()),
		[]byte(strings.ToUpper(delivery.OriginEvent().Digest().String())), 1)
	forgedID := bytes.Replace(canonical, []byte(delivery.ID().String()),
		[]byte("delivery:"+strings.Repeat("a", 64)), 1)
	cases := map[string][]byte{
		"unknown field":       unknown,
		"nested unknown":      nestedUnknown,
		"duplicate field":     duplicate,
		"leading whitespace":  append([]byte(" "), canonical...),
		"trailing value":      append(append([]byte(nil), canonical...), []byte(`{}`)...),
		"noncanonical time":   nonUTC,
		"noncanonical digest": upperDigest,
		"forged delivery ID":  forgedID,
		"corrupt JSON":        append(append([]byte(nil), canonical[:len(canonical)-1]...), '!'),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePeerDeliveryCanonicalJSON(input, route); err == nil {
				t.Fatalf("ParsePeerDeliveryCanonicalJSON(%s) unexpectedly succeeded", input)
			}
		})
	}
}

func TestPeerDeliveryRejectsInvalidBounds(t *testing.T) {
	route, base := peerDeliveryFixture(t, "route:bounds")
	baseSpec := peerDeliverySpecFrom(base)
	for name, mutate := range map[string]func(*PeerDeliverySpec){
		"zero sequence":       func(spec *PeerDeliverySpec) { spec.OriginSequence = 0 },
		"invalid consequence": func(spec *PeerDeliverySpec) { spec.OriginConsequence = ConsequenceInvalid },
		"zero target count":   func(spec *PeerDeliverySpec) { spec.OriginTargetCount = 0 },
		"excess target count": func(spec *PeerDeliverySpec) { spec.OriginTargetCount = MaxSuccessors + 1 },
		"zero depth":          func(spec *PeerDeliverySpec) { spec.CausalDepth = 0 },
		"excess depth":        func(spec *PeerDeliverySpec) { spec.CausalDepth = MaxPeerCausalDepth + 1 },
		"expired at origin":   func(spec *PeerDeliverySpec) { spec.ExpiresAt = spec.OriginAcceptedAt },
		"excess TTL": func(spec *PeerDeliverySpec) {
			spec.ExpiresAt = spec.OriginAcceptedAt.Add(MaxPeerDeliveryTTL + time.Nanosecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := baseSpec
			mutate(&spec)
			if _, err := NewPeerDelivery(route, spec); err == nil {
				t.Fatal("NewPeerDelivery() unexpectedly succeeded")
			}
		})
	}
	assertPeerDeliveryCollectionBounds(t, route, baseSpec)
}

func TestPeerDeliveryIdentifiesOnlySoleTargetTerminalReplyCandidate(t *testing.T) {
	route, base := peerDeliveryFixture(t, "route:terminal-role")
	for _, test := range []struct {
		name        string
		consequence Consequence
		targetCount uint8
		want        bool
	}{
		{name: "terminal sole target", consequence: ConsequenceResolveDeclined, targetCount: 1, want: true},
		{name: "terminal with anchor", consequence: ConsequenceResolveDeclined, targetCount: 2},
		{name: "ordinary sole target", consequence: ConsequenceAdvanceHandling, targetCount: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := peerDeliverySpecFrom(base)
			spec.OriginConsequence = test.consequence
			spec.OriginTargetCount = test.targetCount
			if test.want {
				spec.InReplyToDelivery = mustDeliveryID(t, "delivery:terminal-request")
			}
			delivery, err := NewPeerDelivery(route, spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := delivery.RequiresTerminalReplyMatch(); got != test.want {
				t.Fatalf("RequiresTerminalReplyMatch() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPeerDeliveryRejectsSoleTargetTerminalWithoutExactReplyAuthority(t *testing.T) {
	route, base := peerDeliveryFixture(t, "route:terminal-authority")
	for name, mutate := range map[string]func(*PeerDeliverySpec){
		"missing correlation": func(spec *PeerDeliverySpec) {
			spec.OriginCorrelation = EventRef{}
			spec.InReplyToDelivery = mustDeliveryID(t, "delivery:request")
		},
		"missing in reply to": func(spec *PeerDeliverySpec) {},
	} {
		t.Run(name, func(t *testing.T) {
			spec := peerDeliverySpecFrom(base)
			spec.OriginConsequence = ConsequenceResolveDeclined
			spec.OriginTargetCount = 1
			mutate(&spec)
			if _, err := NewPeerDelivery(route, spec); !errors.Is(err, ErrInvariant) {
				t.Fatalf("NewPeerDelivery() error = %v, want ErrInvariant", err)
			}
		})
	}
}

func TestPeerDeliveryRejectsInvalidProvenance(t *testing.T) {
	route, base := peerDeliveryFixture(t, "route:invalid-provenance")
	baseSpec := peerDeliverySpecFrom(base)
	causal := mustEventRef(t, "event:duplicate", "duplicate")
	digest := Sum([]byte("duplicate"))
	for name, mutate := range map[string]func(*PeerDeliverySpec){
		"self causation": func(spec *PeerDeliverySpec) {
			spec.OriginCausation = []EventRef{spec.OriginEvent}
		},
		"self correlation": func(spec *PeerDeliverySpec) { spec.OriginCorrelation = spec.OriginEvent },
		"duplicate causation": func(spec *PeerDeliverySpec) {
			spec.OriginCausation = []EventRef{causal, causal}
		},
		"duplicate Artifact": func(spec *PeerDeliverySpec) {
			spec.Artifacts = []Digest{digest, digest}
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := baseSpec
			mutate(&spec)
			if _, err := NewPeerDelivery(route, spec); err == nil {
				t.Fatal("NewPeerDelivery() unexpectedly succeeded")
			}
		})
	}
}

func TestPeerDeliveryRejectsOversizeCanonicalInputs(t *testing.T) {
	route, _ := peerDeliveryFixture(t, "route:canonical-bounds")
	payload, err := NewSemanticPayload(strings.Repeat("x", MaxSemanticPayloadBytes+1))
	if err == nil || payload.String() != "" {
		t.Fatalf("oversize payload error = %v", err)
	}
	escaped, err := NewSemanticPayload(strings.Repeat("\x01", MaxSemanticPayloadBytes))
	if !errors.Is(err, ErrLimit) || escaped.String() != "" {
		t.Fatalf("escaped payload projection bound error = %v, want ErrLimit", err)
	}
	oversizeJSON := bytes.Repeat([]byte{' '}, MaxPeerDeliveryCanonicalBytes+1)
	if _, err := ParsePeerDeliveryCanonicalJSON(oversizeJSON, route); !errors.Is(err, ErrLimit) {
		t.Fatalf("parser byte bound error = %v, want ErrLimit", err)
	}
}

func peerDeliverySpecFrom(base PeerDelivery) PeerDeliverySpec {
	correlation, _ := base.OriginCorrelation()
	inReplyToDelivery, _ := base.InReplyToDelivery()
	return PeerDeliverySpec{
		OriginEvent: base.OriginEvent(), OriginSequence: base.OriginSequence(),
		OriginAcceptedAt: base.OriginAcceptedAt(), OriginSource: base.OriginSource(),
		OriginConsequence: base.OriginConsequence(), OriginTargetCount: uint8(base.OriginTargetCount()),
		OriginCausation: base.OriginCausation(), OriginCorrelation: correlation,
		InReplyToDelivery: inReplyToDelivery,
		TargetAlias:       base.TargetAlias(), Kind: base.Kind(), Payload: base.Payload(),
		Artifacts: base.Artifacts(), CausalDepth: base.CausalDepth(), ExpiresAt: base.ExpiresAt(),
	}
}

func assertPeerDeliveryCollectionBounds(t *testing.T, route RouteID, baseSpec PeerDeliverySpec) {
	t.Helper()
	tooManyArtifacts := make([]Digest, MaxArtifactInputs+1)
	for index := range tooManyArtifacts {
		tooManyArtifacts[index] = Sum([]byte{byte(index + 1)})
	}
	baseSpec.Artifacts = tooManyArtifacts
	if _, err := NewPeerDelivery(route, baseSpec); !errors.Is(err, ErrLimit) {
		t.Fatalf("Artifact bound error = %v, want ErrLimit", err)
	}
	tooManyCausal := make([]EventRef, MaxCausationHandles+1)
	for index := range tooManyCausal {
		tooManyCausal[index] = mustEventRef(t, "event:causal-"+string(rune('a'+index)),
			"causal-"+string(rune('a'+index)))
	}
	baseSpec.Artifacts = nil
	baseSpec.OriginCausation = tooManyCausal
	if _, err := NewPeerDelivery(route, baseSpec); !errors.Is(err, ErrLimit) {
		t.Fatalf("causation bound error = %v, want ErrLimit", err)
	}
}

func FuzzParsePeerDeliveryCanonicalJSON(f *testing.F) {
	route, _ := NewRouteID("route:fuzz")
	originID, _ := NewEventID("event:fuzz-origin")
	origin, _ := NewEventRef(originID, Sum([]byte("origin")))
	source, _ := NewAgentPrincipalID("agent:fuzz-origin")
	target, _ := NewOpaqueHandle("fuzz/target")
	kind, _ := NewSemanticLabel("fuzz.request")
	payload, _ := NewSemanticPayload("bounded")
	delivery, err := NewPeerDelivery(route, PeerDeliverySpec{
		OriginEvent: origin, OriginSequence: 1, OriginAcceptedAt: testTime, OriginSource: source,
		OriginConsequence: ConsequenceCreateHandlings, OriginTargetCount: 2,
		TargetAlias: target, Kind: kind, Payload: payload, CausalDepth: 1, ExpiresAt: testTime.Add(time.Hour),
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(delivery.CanonicalJSON())
	terminal, err := NewPeerDelivery(route, PeerDeliverySpec{
		OriginEvent: origin, OriginSequence: 2, OriginAcceptedAt: testTime, OriginSource: source,
		OriginConsequence: ConsequenceResolveDeclined, OriginTargetCount: 1,
		OriginCorrelation: mustFuzzEventRef("event:fuzz-request", "request"),
		InReplyToDelivery: DeliveryID{digest: Sum([]byte("delivery:fuzz-request"))},
		TargetAlias:       target, Kind: kind, Payload: payload, CausalDepth: 1,
		ExpiresAt: testTime.Add(time.Hour),
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(terminal.CanonicalJSON())
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, parseErr := ParsePeerDeliveryCanonicalJSON(data, route)
		if parseErr != nil {
			return
		}
		if !bytes.Equal(parsed.CanonicalJSON(), data) {
			t.Fatal("successful parser did not preserve exact canonical bytes")
		}
	})
}

func mustFuzzEventRef(idValue, body string) EventRef {
	id, _ := NewEventID(idValue)
	event, _ := NewEventRef(id, Sum([]byte(body)))
	return event
}
