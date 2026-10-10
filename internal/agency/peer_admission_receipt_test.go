package agency

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPeerAdmissionReceiptCanonicalRoundTrip(t *testing.T) {
	_, delivery := peerDeliveryFixture(t, "route:receipt")
	accepted, err := NewAcceptedPeerAdmissionReceipt(delivery,
		mustEventRef(t, "event:local-accepted", "local accepted"), testTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"schema_version":1,"delivery_id":"delivery:5faa16233ca9c44233fdb54dcefecffc5b66d1d37e8213a0498bd2fbb2eee901","envelope_digest":"sha256:9c06736a0fd9ef0ef0fd627eb188e129f5b321f2dc8a89e3ff5ef707bdda7f00","outcome":"accepted","recorded_at":"2026-08-03T08:01:00Z","local_event":{"id":"event:local-accepted","digest":"sha256:a7a91f9231215908d7eaa05d07e1498abe76f6c43aa053a7acb809fab4d8b5a7"}}`
	if string(accepted.CanonicalJSON()) != strings.Replace(golden,
		"sha256:9c06736a0fd9ef0ef0fd627eb188e129f5b321f2dc8a89e3ff5ef707bdda7f00",
		"sha256:470d83c4810c711f1f286917f025ac76a237b2e1e6b5540c20691350122db95c", 1) ||
		accepted.Digest().String() != "sha256:f6d7bd4233d2b8d1c67794fce53a162a0ff29265d05360661b887446f627eda1" {
		t.Fatalf("PeerAdmissionReceipt golden drift\n got: %s\ndigest: %s",
			accepted.CanonicalJSON(), accepted.Digest().String())
	}
	rejected, err := NewRejectedPeerAdmissionReceipt(delivery, mustLabel(t, "peer.denied"),
		"Target policy denied this candidate.", testTime.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []PeerAdmissionReceipt{accepted, rejected} {
		parsed, parseErr := ParsePeerAdmissionReceiptCanonicalJSON(original.CanonicalJSON(), delivery)
		if parseErr != nil {
			t.Fatalf("ParsePeerAdmissionReceiptCanonicalJSON() error = %v", parseErr)
		}
		if parsed.DeliveryID() != delivery.ID() || parsed.EnvelopeDigest() != delivery.EnvelopeDigest() ||
			parsed.Outcome() != original.Outcome() || parsed.Digest() != original.Digest() ||
			!bytes.Equal(parsed.CanonicalJSON(), original.CanonicalJSON()) ||
			!bytes.Equal(parsed.SigningMessage(), original.SigningMessage()) {
			t.Fatalf("parsed peer Receipt differs\n got: %s\nwant: %s",
				parsed.CanonicalJSON(), original.CanonicalJSON())
		}
	}
	if bytes.Equal(accepted.SigningMessage(), delivery.SigningMessage()) ||
		!bytes.HasPrefix(accepted.SigningMessage(), []byte(peerAdmissionReceiptSignatureDomain+"\x00")) {
		t.Fatal("delivery and peer Receipt signing messages are not domain separated")
	}
}

func TestPeerAdmissionReceiptRejectsMalformedOrUnboundData(t *testing.T) {
	_, delivery := peerDeliveryFixture(t, "route:receipt-strict")
	receipt, err := NewRejectedPeerAdmissionReceipt(delivery, mustLabel(t, "peer.rejected"),
		"No.", testTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	canonical := receipt.CanonicalJSON()
	unknown := bytes.Replace(canonical, []byte("{"), []byte(`{"unknown":true,`), 1)
	duplicate := bytes.Replace(canonical, []byte(`"outcome":"rejected"`),
		[]byte(`"outcome":"rejected","outcome":"accepted"`), 1)
	nonUTC := bytes.Replace(canonical, []byte(testTime.Add(time.Minute).Format(time.RFC3339Nano)),
		[]byte("2026-08-03T16:01:00+08:00"), 1)
	uppercaseDigest := bytes.Replace(canonical, []byte(delivery.EnvelopeDigest().String()),
		[]byte(strings.ToUpper(delivery.EnvelopeDigest().String())), 1)
	acceptedWithCode, _, marshalErr := canonicalJSON(peerAdmissionReceiptWire{
		SchemaVersion: 1, DeliveryID: delivery.ID().String(),
		EnvelopeDigest: delivery.EnvelopeDigest().String(), Outcome: "accepted",
		RecordedAt: testTime.Add(time.Minute).Format(time.RFC3339Nano),
		LocalEvent: &eventRefWire{ID: "event:local", Digest: Sum([]byte("local")).String()},
		Code:       "forged.code",
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	forgedBinding := bytes.Replace(canonical, []byte(delivery.EnvelopeDigest().String()),
		[]byte(Sum([]byte("other envelope")).String()), 1)
	cases := map[string][]byte{
		"unknown":             unknown,
		"duplicate":           duplicate,
		"noncanonical time":   nonUTC,
		"noncanonical digest": uppercaseDigest,
		"accepted with code":  acceptedWithCode,
		"forged binding":      forgedBinding,
		"leading whitespace":  append([]byte(" "), canonical...),
		"trailing value":      append(append([]byte(nil), canonical...), []byte(`{}`)...),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePeerAdmissionReceiptCanonicalJSON(input, delivery); err == nil {
				t.Fatalf("ParsePeerAdmissionReceiptCanonicalJSON(%s) unexpectedly succeeded", input)
			}
		})
	}
	changed, err := NewPeerDelivery(mustRoute(t, "route:receipt-strict"), PeerDeliverySpec{
		OriginEvent: delivery.OriginEvent(), OriginSequence: delivery.OriginSequence(),
		OriginAcceptedAt: delivery.OriginAcceptedAt(), OriginSource: delivery.OriginSource(),
		OriginConsequence: delivery.OriginConsequence(), OriginTargetCount: uint8(delivery.OriginTargetCount()),
		OriginCausation: delivery.OriginCausation(), TargetAlias: delivery.TargetAlias(),
		Kind: delivery.Kind(), Payload: mustPayload(t, "Changed envelope under the same logical ID."),
		Artifacts: delivery.Artifacts(), CausalDepth: delivery.CausalDepth(), ExpiresAt: delivery.ExpiresAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID() != delivery.ID() || changed.EnvelopeDigest() == delivery.EnvelopeDigest() {
		t.Fatal("same-ID/different-envelope fixture is not a conflict")
	}
	if _, err := ParsePeerAdmissionReceiptCanonicalJSON(canonical, changed); !errors.Is(err, ErrInvariant) {
		t.Fatalf("same-ID/different-envelope Receipt binding error = %v, want ErrInvariant", err)
	}
	oversize := bytes.Repeat([]byte{' '}, MaxPeerAdmissionReceiptCanonicalBytes+1)
	if _, err := ParsePeerAdmissionReceiptCanonicalJSON(oversize, delivery); !errors.Is(err, ErrLimit) {
		t.Fatalf("Receipt parser byte bound error = %v, want ErrLimit", err)
	}
}

func TestPeerAdmissionReceiptRequiresValidOutcome(t *testing.T) {
	_, delivery := peerDeliveryFixture(t, "route:receipt-bounds")
	local := mustEventRef(t, "event:local", "local")
	if _, err := NewAcceptedPeerAdmissionReceipt(delivery, EventRef{}, testTime.Add(time.Minute)); err == nil {
		t.Fatal("accepted Receipt without local Event unexpectedly succeeded")
	}
	if _, err := NewAcceptedPeerAdmissionReceipt(delivery, delivery.OriginEvent(),
		testTime.Add(time.Minute)); !errors.Is(err, ErrInvariant) {
		t.Fatalf("origin Event reuse error = %v, want ErrInvariant", err)
	}
	// recorded_at is evidence from another node's clock. Delivery expiry is
	// enforced against the receiving local clock, not by cross-node comparison.
	for _, at := range []time.Time{testTime.Add(-time.Hour), delivery.ExpiresAt().Add(time.Hour)} {
		if _, err := NewAcceptedPeerAdmissionReceipt(delivery, local, at); err != nil {
			t.Fatalf("canonical remote recorded_at %s rejected: %v", at, err)
		}
	}
	if _, err := NewRejectedPeerAdmissionReceipt(delivery, SemanticLabel{}, "", testTime.Add(time.Minute)); err == nil {
		t.Fatal("rejected Receipt without code unexpectedly succeeded")
	}
	if _, err := NewRejectedPeerAdmissionReceipt(delivery, mustLabel(t, "peer.denied"),
		strings.Repeat("x", MaxDiagnosticBytes+1), testTime.Add(time.Minute)); !errors.Is(err, ErrLimit) {
		t.Fatalf("diagnostic bound error = %v, want ErrLimit", err)
	}
}

func TestPeerAdmissionReceiptDefensiveCopies(t *testing.T) {
	_, delivery := peerDeliveryFixture(t, "route:receipt-copy")
	receipt, err := NewRejectedPeerAdmissionReceipt(delivery, mustLabel(t, "peer.denied"),
		"No.", testTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	canonical, message := receipt.CanonicalJSON(), receipt.SigningMessage()
	canonical[0], message[0] = '!', '!'
	if receipt.CanonicalJSON()[0] == '!' || receipt.SigningMessage()[0] == '!' {
		t.Fatal("PeerAdmissionReceipt exposed mutable internal bytes")
	}
}

func FuzzParsePeerAdmissionReceiptCanonicalJSON(f *testing.F) {
	route, _ := NewRouteID("route:receipt-fuzz")
	originID, _ := NewEventID("event:receipt-fuzz-origin")
	origin, _ := NewEventRef(originID, Sum([]byte("origin")))
	source, _ := NewAgentPrincipalID("agent:receipt-fuzz-origin")
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
	receipt, err := NewRejectedPeerAdmissionReceipt(delivery, kind, "bounded", testTime.Add(time.Minute))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(receipt.CanonicalJSON())
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, parseErr := ParsePeerAdmissionReceiptCanonicalJSON(data, delivery)
		if parseErr != nil {
			return
		}
		if !bytes.Equal(parsed.CanonicalJSON(), data) {
			t.Fatal("successful parser did not preserve exact canonical bytes")
		}
	})
}
