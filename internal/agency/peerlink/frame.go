package peerlink

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
)

const (
	frameVersion  = 1
	framePrefix   = 4
	maxHeaderSize = 48 << 10
)

type frameType string

const (
	frameDeliveryOffer    frameType = "delivery.offer"
	frameTransportACK     frameType = "transport.ack"
	frameAdmissionReceipt frameType = "admission.receipt"
	frameArtifactRequest  frameType = "artifact.request"
	frameArtifactResponse frameType = "artifact.response"
)

type frameWire struct {
	Version        uint8     `json:"version"`
	Type           frameType `json:"type"`
	DeliveryID     string    `json:"delivery_id"`
	EnvelopeDigest string    `json:"envelope_digest"`
	ObjectDigest   string    `json:"object_digest,omitempty"`
	Canonical      []byte    `json:"canonical,omitempty"`
	Signature      []byte    `json:"signature,omitempty"`
	BodyBytes      int64     `json:"body_bytes,omitempty"`
}

type frame struct {
	frameType      frameType
	deliveryID     agency.DeliveryID
	envelopeDigest agency.Digest
	objectDigest   agency.Digest
	canonical      []byte
	signature      []byte
	body           []byte
}

// DeliveryOffer is an authenticated transport envelope. Its canonical bytes
// remain an authority candidate until the caller parses them under the exact
// enrolled route and admits them locally.
type DeliveryOffer struct {
	deliveryID     agency.DeliveryID
	envelopeDigest agency.Digest
	canonical      []byte
	signature      []byte
}

func (offer DeliveryOffer) DeliveryID() agency.DeliveryID { return offer.deliveryID }
func (offer DeliveryOffer) EnvelopeDigest() agency.Digest { return offer.envelopeDigest }
func (offer DeliveryOffer) CanonicalDelivery() []byte {
	return append([]byte(nil), offer.canonical...)
}
func (offer DeliveryOffer) Signature() []byte {
	return append([]byte(nil), offer.signature...)
}

// DeliveryResponse is exactly one transport ACK or one signed admission
// Receipt. TransportACK never implies admission or completion.
type DeliveryResponse struct {
	transportACK bool
	receipt      agency.PeerAdmissionReceipt
	signature    []byte
}

// NewTransportACK creates a transport-only response for a server callback.
func NewTransportACK() DeliveryResponse { return DeliveryResponse{transportACK: true} }

// NewAdmissionResponse creates an admission response for a server callback.
// peerlink signs it only after checking its exact request binding.
func NewAdmissionResponse(receipt agency.PeerAdmissionReceipt) (DeliveryResponse, error) {
	if !completeReceipt(receipt) {
		return DeliveryResponse{}, fmt.Errorf("%w: complete admission Receipt is required", ErrInput)
	}
	return DeliveryResponse{receipt: receipt}, nil
}

func (response DeliveryResponse) IsTransportACK() bool { return response.transportACK }
func (response DeliveryResponse) AdmissionReceipt() (agency.PeerAdmissionReceipt, bool) {
	return response.receipt, !response.transportACK && completeReceipt(response.receipt)
}
func (response DeliveryResponse) Signature() []byte {
	return append([]byte(nil), response.signature...)
}

// ArtifactRequest is always scoped to one authenticated delivery envelope.
// Authorization remains the server callback's responsibility.
type ArtifactRequest struct {
	deliveryID     agency.DeliveryID
	envelopeDigest agency.Digest
	objectDigest   agency.Digest
}

func (request ArtifactRequest) DeliveryID() agency.DeliveryID { return request.deliveryID }
func (request ArtifactRequest) EnvelopeDigest() agency.Digest { return request.envelopeDigest }
func (request ArtifactRequest) ObjectDigest() agency.Digest   { return request.objectDigest }

func deliveryOfferFrame(delivery agency.PeerDelivery, privateKey ed25519.PrivateKey) (frame, error) {
	if delivery.ID().IsZero() || delivery.EnvelopeDigest().IsZero() ||
		len(delivery.CanonicalJSON()) == 0 || len(privateKey) != ed25519.PrivateKeySize {
		return frame{}, fmt.Errorf("%w: complete delivery and signing key are required", ErrInput)
	}
	return frame{frameType: frameDeliveryOffer, deliveryID: delivery.ID(),
		envelopeDigest: delivery.EnvelopeDigest(), canonical: delivery.CanonicalJSON(),
		signature: ed25519.Sign(privateKey, delivery.SigningMessage())}, nil
}

func deliveryResponseFrame(offer DeliveryOffer, response DeliveryResponse,
	privateKey ed25519.PrivateKey,
) (frame, error) {
	if offer.deliveryID.IsZero() || offer.envelopeDigest.IsZero() {
		return frame{}, fmt.Errorf("%w: complete delivery binding is required", ErrInput)
	}
	if response.transportACK {
		if completeReceipt(response.receipt) || len(response.signature) != 0 {
			return frame{}, fmt.Errorf("%w: transport ACK cannot carry admission evidence", ErrInput)
		}
		return frame{frameType: frameTransportACK, deliveryID: offer.deliveryID,
			envelopeDigest: offer.envelopeDigest}, nil
	}
	if !completeReceipt(response.receipt) || len(privateKey) != ed25519.PrivateKeySize ||
		response.receipt.DeliveryID() != offer.deliveryID ||
		response.receipt.EnvelopeDigest() != offer.envelopeDigest {
		return frame{}, fmt.Errorf("%w: admission Receipt does not bind the offer", ErrInput)
	}
	return frame{frameType: frameAdmissionReceipt, deliveryID: offer.deliveryID,
		envelopeDigest: offer.envelopeDigest, canonical: response.receipt.CanonicalJSON(),
		signature: ed25519.Sign(privateKey, response.receipt.SigningMessage())}, nil
}

func artifactRequestFrame(delivery agency.PeerDelivery, digest agency.Digest) (frame, error) {
	if delivery.ID().IsZero() || delivery.EnvelopeDigest().IsZero() || digest.IsZero() ||
		!deliveryReferencesArtifact(delivery, digest) {
		return frame{}, fmt.Errorf("%w: Artifact must belong to the exact delivery", ErrInput)
	}
	return frame{frameType: frameArtifactRequest, deliveryID: delivery.ID(),
		envelopeDigest: delivery.EnvelopeDigest(), objectDigest: digest}, nil
}

func artifactResponseFrame(request ArtifactRequest, body []byte) (frame, error) {
	if request.deliveryID.IsZero() || request.envelopeDigest.IsZero() ||
		request.objectDigest.IsZero() || len(body) > artifact.MaxObjectBytes ||
		agency.Sum(body) != request.objectDigest {
		return frame{}, fmt.Errorf("%w: verified delivery-scoped Artifact is required", ErrInput)
	}
	return frame{frameType: frameArtifactResponse, deliveryID: request.deliveryID,
		envelopeDigest: request.envelopeDigest, objectDigest: request.objectDigest,
		body: append([]byte(nil), body...)}, nil
}

func completeReceipt(receipt agency.PeerAdmissionReceipt) bool {
	return !receipt.DeliveryID().IsZero() && !receipt.EnvelopeDigest().IsZero() &&
		len(receipt.CanonicalJSON()) > 0 && len(receipt.SigningMessage()) > 0
}

func deliveryReferencesArtifact(delivery agency.PeerDelivery, expected agency.Digest) bool {
	for _, digest := range delivery.Artifacts() {
		if digest == expected {
			return true
		}
	}
	return false
}

func writeFrame(writer io.Writer, value frame) error {
	wire, err := frameToWire(value)
	if err != nil {
		return err
	}
	header, err := json.Marshal(wire)
	if err != nil || len(header) == 0 || len(header) > maxHeaderSize {
		return fmt.Errorf("%w: encode bounded frame header", ErrFrame)
	}
	var prefix [framePrefix]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(header)))
	if err := writeAll(writer, prefix[:]); err != nil {
		return fmt.Errorf("%w: write frame prefix: %v", ErrTransport, err)
	}
	if err := writeAll(writer, header); err != nil {
		return fmt.Errorf("%w: write frame header: %v", ErrTransport, err)
	}
	if err := writeAll(writer, value.body); err != nil {
		return fmt.Errorf("%w: write frame body: %v", ErrTransport, err)
	}
	return nil
}

func readFrame(reader io.Reader) (frame, error) {
	var prefix [framePrefix]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return frame{}, fmt.Errorf("%w: read frame prefix: %v", ErrTransport, err)
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > maxHeaderSize {
		return frame{}, fmt.Errorf("%w: header size exceeds bound", ErrFrame)
	}
	header := make([]byte, int(size))
	if _, err := io.ReadFull(reader, header); err != nil {
		return frame{}, fmt.Errorf("%w: read frame header: %v", ErrTransport, err)
	}
	wire, err := parseFrameWire(header)
	if err != nil {
		return frame{}, err
	}
	value, err := wireToFrame(wire)
	if err != nil {
		return frame{}, err
	}
	if wire.BodyBytes > 0 {
		value.body = make([]byte, int(wire.BodyBytes))
		if _, err := io.ReadFull(reader, value.body); err != nil {
			return frame{}, fmt.Errorf("%w: read frame body: %v", ErrTransport, err)
		}
	}
	if wire.Type == frameArtifactResponse && agency.Sum(value.body) != value.objectDigest {
		return frame{}, fmt.Errorf("%w: Artifact digest mismatch", ErrFrame)
	}
	return value, nil
}

func frameToWire(value frame) (frameWire, error) {
	wire := frameWire{Version: frameVersion, Type: value.frameType,
		DeliveryID: value.deliveryID.String(), EnvelopeDigest: value.envelopeDigest.String()}
	if !value.objectDigest.IsZero() {
		wire.ObjectDigest = value.objectDigest.String()
	}
	wire.Canonical = append([]byte(nil), value.canonical...)
	wire.Signature = append([]byte(nil), value.signature...)
	wire.BodyBytes = int64(len(value.body))
	if _, err := wireToFrame(wire); err != nil {
		return frameWire{}, err
	}
	if value.frameType == frameArtifactResponse && agency.Sum(value.body) != value.objectDigest {
		return frameWire{}, fmt.Errorf("%w: Artifact digest mismatch", ErrFrame)
	}
	return wire, nil
}

func parseFrameWire(raw []byte) (frameWire, error) {
	if !utf8.Valid(raw) {
		return frameWire{}, fmt.Errorf("%w: header is not valid UTF-8", ErrFrame)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire frameWire
	if err := decoder.Decode(&wire); err != nil {
		return frameWire{}, fmt.Errorf("%w: decode frame header: %v", ErrFrame, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return frameWire{}, err
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, raw) {
		return frameWire{}, fmt.Errorf("%w: frame header is not canonical", ErrFrame)
	}
	return wire, nil
}

func wireToFrame(wire frameWire) (frame, error) {
	if wire.Version != frameVersion {
		return frame{}, fmt.Errorf("%w: unsupported frame version", ErrFrame)
	}
	deliveryID, err := agency.ParseDeliveryID(wire.DeliveryID)
	if err != nil {
		return frame{}, fmt.Errorf("%w: invalid delivery ID", ErrFrame)
	}
	envelopeDigest, err := agency.ParseDigest(wire.EnvelopeDigest)
	if err != nil {
		return frame{}, fmt.Errorf("%w: invalid envelope digest", ErrFrame)
	}
	value := frame{frameType: wire.Type, deliveryID: deliveryID,
		envelopeDigest: envelopeDigest, canonical: append([]byte(nil), wire.Canonical...),
		signature: append([]byte(nil), wire.Signature...)}
	if wire.ObjectDigest != "" {
		value.objectDigest, err = agency.ParseDigest(wire.ObjectDigest)
		if err != nil {
			return frame{}, fmt.Errorf("%w: invalid object digest", ErrFrame)
		}
	}
	if err := validateFrameShape(wire, value); err != nil {
		return frame{}, err
	}
	return value, nil
}

func validateFrameShape(wire frameWire, value frame) error {
	switch wire.Type {
	case frameDeliveryOffer:
		return validateDeliveryOffer(wire, value)
	case frameTransportACK:
		return validateTransportACK(wire, value)
	case frameAdmissionReceipt:
		return validateAdmissionReceipt(wire, value)
	case frameArtifactRequest:
		return validateArtifactRequest(wire, value)
	case frameArtifactResponse:
		return validateArtifactResponse(wire, value)
	default:
		return fmt.Errorf("%w: unknown frame type", ErrFrame)
	}
}

func validateDeliveryOffer(wire frameWire, value frame) error {
	if !value.objectDigest.IsZero() || wire.BodyBytes != 0 ||
		len(wire.Signature) != ed25519.SignatureSize ||
		validateEmbeddedJSON(wire.Canonical, agency.MaxPeerDeliveryCanonicalBytes) != nil {
		return fmt.Errorf("%w: invalid delivery offer", ErrFrame)
	}
	return nil
}

func validateTransportACK(wire frameWire, value frame) error {
	if !value.objectDigest.IsZero() || wire.BodyBytes != 0 || len(wire.Canonical) != 0 ||
		len(wire.Signature) != 0 {
		return fmt.Errorf("%w: transport ACK carries forbidden fields", ErrFrame)
	}
	return nil
}

func validateAdmissionReceipt(wire frameWire, value frame) error {
	if !value.objectDigest.IsZero() || wire.BodyBytes != 0 ||
		len(wire.Signature) != ed25519.SignatureSize ||
		validateEmbeddedJSON(wire.Canonical, agency.MaxPeerAdmissionReceiptCanonicalBytes) != nil {
		return fmt.Errorf("%w: invalid admission Receipt", ErrFrame)
	}
	return nil
}

func validateArtifactRequest(wire frameWire, value frame) error {
	if value.objectDigest.IsZero() || wire.BodyBytes != 0 || len(wire.Canonical) != 0 ||
		len(wire.Signature) != 0 {
		return fmt.Errorf("%w: invalid Artifact request", ErrFrame)
	}
	return nil
}

func validateArtifactResponse(wire frameWire, value frame) error {
	if value.objectDigest.IsZero() || len(wire.Canonical) != 0 || len(wire.Signature) != 0 ||
		wire.BodyBytes < 0 || wire.BodyBytes > artifact.MaxObjectBytes {
		return fmt.Errorf("%w: invalid Artifact response", ErrFrame)
	}
	return nil
}

func validateEmbeddedJSON(raw []byte, maximum int) error {
	if len(raw) == 0 || len(raw) > maximum || !utf8.Valid(raw) || !json.Valid(raw) {
		return fmt.Errorf("%w: embedded JSON is invalid or oversized", ErrFrame)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || !bytes.Equal(compact.Bytes(), raw) {
		return fmt.Errorf("%w: embedded JSON is not compact", ErrFrame)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing frame input: %v", ErrFrame, err)
	}
	return fmt.Errorf("%w: multiple frame values", ErrFrame)
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}
