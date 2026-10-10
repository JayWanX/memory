package peerlink

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestClosedFramesRoundTripAndKeepTransportACKNonsemantic(t *testing.T) {
	identity := testIdentity(t, "peer/origin", 0x11)
	delivery, _ := testDelivery(t, "route:frames", []byte("artifact bytes"))
	offer := assertDeliveryOfferFrame(t, identity, delivery)
	assertTransportACKFrame(t, identity, offer)
	assertAdmissionReceiptFrame(t, identity, offer, delivery)
	assertArtifactFrames(t, delivery, []byte("artifact bytes"))
}

func assertDeliveryOfferFrame(t *testing.T, identity Identity,
	delivery agency.PeerDelivery,
) DeliveryOffer {
	t.Helper()
	offerFrame, err := deliveryOfferFrame(delivery, identity.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	parsedOffer := roundTripFrame(t, offerFrame)
	if parsedOffer.frameType != frameDeliveryOffer || parsedOffer.deliveryID != delivery.ID() ||
		parsedOffer.envelopeDigest != delivery.EnvelopeDigest() ||
		!bytes.Equal(parsedOffer.canonical, delivery.CanonicalJSON()) ||
		!ed25519.Verify(identity.PrivateKey.Public().(ed25519.PublicKey),
			delivery.SigningMessage(), parsedOffer.signature) {
		t.Fatal("delivery offer lost its binding or signature")
	}
	encodedOffer := encodeFrame(t, offerFrame)
	if bytes.Contains(encodedOffer, []byte("peer_id")) ||
		bytes.Contains(encodedOffer, []byte(identity.ID.String())) {
		t.Fatal("frame carried a self-reported Peer identity")
	}
	return DeliveryOffer{deliveryID: delivery.ID(), envelopeDigest: delivery.EnvelopeDigest(),
		canonical: delivery.CanonicalJSON(), signature: parsedOffer.signature}
}

func assertTransportACKFrame(t *testing.T, identity Identity, offer DeliveryOffer) {
	t.Helper()
	ack, err := deliveryResponseFrame(offer, NewTransportACK(), identity.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	parsedACK := roundTripFrame(t, ack)
	if parsedACK.frameType != frameTransportACK || len(parsedACK.canonical) != 0 ||
		len(parsedACK.signature) != 0 {
		t.Fatal("transport ACK carried admission evidence")
	}
}

func assertAdmissionReceiptFrame(t *testing.T, identity Identity, offer DeliveryOffer,
	delivery agency.PeerDelivery,
) {
	t.Helper()
	receipt := testReceipt(t, delivery)
	response, err := NewAdmissionResponse(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptFrame, err := deliveryResponseFrame(offer, response, identity.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	parsedReceipt := roundTripFrame(t, receiptFrame)
	reconstructed, err := agency.ParsePeerAdmissionReceiptCanonicalJSON(
		parsedReceipt.canonical, delivery)
	if err != nil || !ed25519.Verify(identity.PrivateKey.Public().(ed25519.PublicKey),
		reconstructed.SigningMessage(), parsedReceipt.signature) {
		t.Fatalf("signed admission Receipt did not round trip: %v", err)
	}
}

func assertArtifactFrames(t *testing.T, delivery agency.PeerDelivery, body []byte) {
	t.Helper()
	request, err := artifactRequestFrame(delivery, delivery.Artifacts()[0])
	if err != nil {
		t.Fatal(err)
	}
	parsedRequest := roundTripFrame(t, request)
	artifactRequest := ArtifactRequest{deliveryID: parsedRequest.deliveryID,
		envelopeDigest: parsedRequest.envelopeDigest, objectDigest: parsedRequest.objectDigest}
	objectFrame, err := artifactResponseFrame(artifactRequest, body)
	if err != nil {
		t.Fatal(err)
	}
	parsedObject := roundTripFrame(t, objectFrame)
	if parsedObject.frameType != frameArtifactResponse || !bytes.Equal(parsedObject.body, body) ||
		parsedObject.objectDigest != agency.Sum(body) {
		t.Fatal("Artifact response did not preserve verified bytes")
	}
}

func TestFrameRejectsOversizeNoncanonicalUnknownAndDigestMismatch(t *testing.T) {
	delivery, _ := testDelivery(t, "route:negative-frames", []byte("correct bytes"))
	request, err := artifactRequestFrame(delivery, delivery.Artifacts()[0])
	if err != nil {
		t.Fatal(err)
	}

	var oversized bytes.Buffer
	var prefix [framePrefix]byte
	binary.BigEndian.PutUint32(prefix[:], maxHeaderSize+1)
	oversized.Write(prefix[:])
	if _, err := readFrame(&oversized); !errors.Is(err, ErrFrame) {
		t.Fatalf("oversize prefix error = %v, want ErrFrame", err)
	}

	canonical := encodeFrame(t, request)
	headerSize := binary.BigEndian.Uint32(canonical[:framePrefix])
	header := canonical[framePrefix : framePrefix+headerSize]
	noncanonicalHeader := append(append([]byte(nil), header...), ' ')
	if _, err := readFrame(bytes.NewReader(encodeRawHeader(noncanonicalHeader, nil))); !errors.Is(err, ErrFrame) {
		t.Fatalf("noncanonical header error = %v, want ErrFrame", err)
	}

	unknown := frameWire{Version: frameVersion, Type: "unknown",
		DeliveryID: delivery.ID().String(), EnvelopeDigest: delivery.EnvelopeDigest().String()}
	unknownHeader, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readFrame(bytes.NewReader(encodeRawHeader(unknownHeader, nil))); !errors.Is(err, ErrFrame) {
		t.Fatalf("unknown frame error = %v, want ErrFrame", err)
	}

	artifactRequest := ArtifactRequest{deliveryID: request.deliveryID,
		envelopeDigest: request.envelopeDigest, objectDigest: request.objectDigest}
	response, err := artifactResponseFrame(artifactRequest, []byte("correct bytes"))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := frameToWire(response)
	if err != nil {
		t.Fatal(err)
	}
	responseHeader, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Repeat([]byte{'x'}, len(response.body))
	if _, err := readFrame(bytes.NewReader(encodeRawHeader(responseHeader, tampered))); !errors.Is(err, ErrFrame) {
		t.Fatalf("Artifact digest mismatch error = %v, want ErrFrame", err)
	}

	withUnknown := strings.TrimSuffix(string(header), "}") + `,"peer_id":"forged"}`
	if _, err := readFrame(bytes.NewReader(encodeRawHeader([]byte(withUnknown), nil))); !errors.Is(err, ErrFrame) {
		t.Fatalf("unknown identity field error = %v, want ErrFrame", err)
	}
}

func TestIdentityRejectsCorruptSeedPublicBinding(t *testing.T) {
	identity := testIdentity(t, "peer/corrupt", 0x21)
	identity.PrivateKey[0] ^= 0xff
	if _, err := prepareIdentity(identity); !errors.Is(err, ErrInput) {
		t.Fatalf("prepareIdentity() error = %v, want ErrInput", err)
	}
}

func roundTripFrame(t *testing.T, value frame) frame {
	t.Helper()
	encoded := encodeFrame(t, value)
	parsed, err := readFrame(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("readFrame() error = %v", err)
	}
	return parsed
}

func encodeFrame(t *testing.T, value frame) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := writeFrame(&buffer, value); err != nil {
		t.Fatalf("writeFrame() error = %v", err)
	}
	return buffer.Bytes()
}

func encodeRawHeader(header, body []byte) []byte {
	result := make([]byte, framePrefix+len(header)+len(body))
	binary.BigEndian.PutUint32(result[:framePrefix], uint32(len(header)))
	copy(result[framePrefix:], header)
	copy(result[framePrefix+len(header):], body)
	return result
}

func testIdentity(t *testing.T, id string, seed byte) Identity {
	t.Helper()
	handle, err := agency.NewOpaqueHandle(id)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	return Identity{ID: handle, PrivateKey: privateKey}
}

func testPeer(t *testing.T, identity Identity, address string) Peer {
	t.Helper()
	return Peer{ID: identity.ID,
		PublicKey: identity.PrivateKey.Public().(ed25519.PublicKey), Address: address}
}

func testDelivery(t *testing.T, routeName string, artifact []byte) (agency.PeerDelivery, agency.RouteID) {
	t.Helper()
	route, err := agency.NewRouteID(routeName)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := agency.NewEventID("event:peerlink-origin")
	if err != nil {
		t.Fatal(err)
	}
	event, err := agency.NewEventRef(eventID, agency.Sum([]byte("origin event")))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := agency.NewAgentPrincipalID("agent:peerlink-origin")
	if err != nil {
		t.Fatal(err)
	}
	target, err := agency.NewOpaqueHandle("remote/worker")
	if err != nil {
		t.Fatal(err)
	}
	kind, err := agency.NewSemanticLabel("opaque.request")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := agency.NewSemanticPayload("Process the referenced Artifact.")
	if err != nil {
		t.Fatal(err)
	}
	acceptedAt := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	delivery, err := agency.NewPeerDelivery(route, agency.PeerDeliverySpec{
		OriginEvent: event, OriginSequence: 1, OriginAcceptedAt: acceptedAt,
		OriginSource: principal, OriginConsequence: agency.ConsequenceCreateHandlings,
		OriginTargetCount: 2, TargetAlias: target, Kind: kind, Payload: payload,
		Artifacts: []agency.Digest{agency.Sum(artifact)}, CausalDepth: 1,
		ExpiresAt: acceptedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return delivery, route
}

func testReceipt(t *testing.T, delivery agency.PeerDelivery) agency.PeerAdmissionReceipt {
	t.Helper()
	eventID, err := agency.NewEventID("event:peerlink-received")
	if err != nil {
		t.Fatal(err)
	}
	event, err := agency.NewEventRef(eventID, agency.Sum([]byte("received event")))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := agency.NewAcceptedPeerAdmissionReceipt(delivery, event,
		time.Date(2026, 8, 3, 8, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
