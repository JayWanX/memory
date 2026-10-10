package authority

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const MaxStagedPeerDeliveries = 64

type PeerAdmissionState uint8

const (
	PeerAdmissionStateInvalid PeerAdmissionState = iota
	PeerAdmissionStateStaged
	PeerAdmissionStateAccepted
	PeerAdmissionStateRejected
	PeerAdmissionStateExpired
)

// PeerAdmissionResult is receiver-local authority. Staged means transport may
// ACK but no local Event exists. Only Accepted or Rejected carries a durable
// PeerAdmissionReceipt suitable for remote settlement.
type PeerAdmissionResult struct {
	state    PeerAdmissionState
	delivery agency.PeerDelivery
	receipt  agency.PeerAdmissionReceipt
	replayed bool
}

func (result PeerAdmissionResult) State() PeerAdmissionState { return result.state }
func (result PeerAdmissionResult) Delivery() agency.PeerDelivery {
	return result.delivery
}
func (result PeerAdmissionResult) Receipt() (agency.PeerAdmissionReceipt, bool) {
	return result.receipt, result.state == PeerAdmissionStateAccepted ||
		result.state == PeerAdmissionStateRejected
}
func (result PeerAdmissionResult) Replayed() bool { return result.replayed }

type StagedPeerDelivery struct {
	route    PeerRouteProjection
	delivery agency.PeerDelivery
}

func (staged StagedPeerDelivery) Route() PeerRouteProjection { return staged.route }
func (staged StagedPeerDelivery) Delivery() agency.PeerDelivery {
	return staged.delivery
}

// StagePeerDelivery authenticates one exact enrolled Peer and signature before
// writing anything. It preserves the signed envelope but creates no Event or
// Handling until explicit receiver-local re-admission succeeds.
func (s *Store) StagePeerDelivery(ctx context.Context, remotePeer agency.OpaqueHandle,
	canonical, signature []byte,
) (PeerAdmissionResult, error) {
	if ctx == nil || remotePeer.IsZero() || len(canonical) == 0 || len(signature) == 0 {
		return PeerAdmissionResult{}, errors.New("stage PeerDelivery: authenticated peer and envelope are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return PeerAdmissionResult{}, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("stage PeerDelivery: begin: %w", err)
	}
	defer tx.Rollback()
	result, err := stagePeerDeliveryTx(ctx, tx, remotePeer, canonical, signature, now)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("stage PeerDelivery: commit: %w", err)
	}
	return result, nil
}

func stagePeerDeliveryTx(ctx context.Context, tx *sql.Tx, remotePeer agency.OpaqueHandle,
	canonical, signature []byte, now time.Time,
) (PeerAdmissionResult, error) {
	route, found, err := peerRouteByRemotePeerTx(ctx, tx, remotePeer)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	if !found {
		return PeerAdmissionResult{}, ErrPeerAuthentication
	}
	parsed, err := agency.ParsePeerDeliveryCanonicalJSON(canonical, route.RouteID())
	if err != nil || !verifyPeerSignature(route.RemotePublicKey(), parsed.SigningMessage(), signature) {
		return PeerAdmissionResult{}, ErrPeerAuthentication
	}
	delivery := parsed.Delivery()
	existing, present, err := peerInboxResultTx(ctx, tx, delivery.ID())
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	if present {
		return replayPeerInboxTx(ctx, tx, existing, route, delivery, canonical, signature, now)
	}
	return stageFreshPeerDeliveryTx(ctx, tx, route, delivery, signature, now)
}

func replayPeerInboxTx(ctx context.Context, tx *sql.Tx, existing PeerAdmissionResult,
	route PeerRouteProjection, delivery agency.PeerDelivery, canonical, signature []byte,
	now time.Time,
) (PeerAdmissionResult, error) {
	if existing.delivery.EnvelopeDigest() != delivery.EnvelopeDigest() ||
		!bytes.Equal(existing.delivery.CanonicalJSON(), canonical) ||
		!peerInboxSignatureMatchesTx(ctx, tx, delivery.ID(), route.RouteID(), signature) {
		return PeerAdmissionResult{}, ErrPeerDeliveryConflict
	}
	if existing.state == PeerAdmissionStateStaged && !now.Before(delivery.ExpiresAt()) {
		return expirePeerInboxTx(ctx, tx, delivery, now)
	}
	existing.replayed = true
	return existing, nil
}

func stageFreshPeerDeliveryTx(ctx context.Context, tx *sql.Tx, route PeerRouteProjection,
	delivery agency.PeerDelivery, signature []byte, now time.Time,
) (PeerAdmissionResult, error) {
	if !route.Active() {
		return PeerAdmissionResult{}, ErrPeerRouteRevoked
	}
	if err := expirePeerInboxRowsTx(ctx, tx, now); err != nil {
		return PeerAdmissionResult{}, err
	}
	var staged int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_inbox WHERE state = 'staged'`).Scan(&staged); err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("stage PeerDelivery: count staged inbox: %w", err)
	}
	if staged >= MaxStagedPeerDeliveries {
		return PeerAdmissionResult{}, ErrPeerInboxBound
	}
	var result PeerAdmissionResult
	var err error
	switch {
	case delivery.TargetAlias() != route.InboundTargetAlias():
		result, err = insertRejectedPeerInboxTx(ctx, tx, route, delivery, signature,
			"peer.target_unavailable", "target alias is not admitted by this route", now)
	case !now.Before(delivery.ExpiresAt()):
		result, err = insertExpiredPeerInboxTx(ctx, tx, route, delivery, signature, now)
	default:
		inReplyTo := peerInboxReplyValue(delivery)
		_, err = tx.ExecContext(ctx, `INSERT INTO peer_inbox(
			delivery_id, route_id, in_reply_to_delivery_id, envelope_digest,
			delivery_json, delivery_signature, state, expires_at, received_at)
			VALUES(?, ?, ?, ?, ?, ?, 'staged', ?, ?)`,
			delivery.ID().String(),
			route.RouteID().String(), inReplyTo, delivery.EnvelopeDigest().String(), delivery.CanonicalJSON(),
			append([]byte(nil), signature...), formatTime(delivery.ExpiresAt()), formatTime(now))
		result = PeerAdmissionResult{state: PeerAdmissionStateStaged, delivery: delivery}
	}
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	return result, nil
}

func insertRejectedPeerInboxTx(ctx context.Context, tx *sql.Tx, route PeerRouteProjection,
	delivery agency.PeerDelivery, signature []byte, codeValue, diagnostic string, now time.Time,
) (PeerAdmissionResult, error) {
	code, err := agency.NewSemanticLabel(codeValue)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	receipt, err := agency.NewRejectedPeerAdmissionReceipt(delivery, code, diagnostic, now)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	inReplyTo := peerInboxReplyValue(delivery)
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_inbox(
		delivery_id, route_id, in_reply_to_delivery_id, envelope_digest, delivery_json, delivery_signature,
		state, expires_at, received_at, receipt_digest, receipt_json, settled_at)
		VALUES(?, ?, ?, ?, ?, ?, 'settled', ?, ?, ?, ?, ?)`, delivery.ID().String(),
		route.RouteID().String(), inReplyTo, delivery.EnvelopeDigest().String(), delivery.CanonicalJSON(),
		append([]byte(nil), signature...), formatTime(delivery.ExpiresAt()), formatTime(now),
		receipt.Digest().String(), receipt.CanonicalJSON(), formatTime(now)); err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("stage PeerDelivery: persist rejection: %w", err)
	}
	return PeerAdmissionResult{state: PeerAdmissionStateRejected, delivery: delivery,
		receipt: receipt}, nil
}

func insertExpiredPeerInboxTx(ctx context.Context, tx *sql.Tx, route PeerRouteProjection,
	delivery agency.PeerDelivery, signature []byte, now time.Time,
) (PeerAdmissionResult, error) {
	inReplyTo := peerInboxReplyValue(delivery)
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_inbox(
		delivery_id, route_id, in_reply_to_delivery_id, envelope_digest, delivery_json,
		delivery_signature, state, expires_at, received_at, settled_at)
		VALUES(?, ?, ?, ?, ?, ?, 'expired', ?, ?, ?)`,
		delivery.ID().String(), route.RouteID().String(), inReplyTo, delivery.EnvelopeDigest().String(),
		delivery.CanonicalJSON(), append([]byte(nil), signature...),
		formatTime(delivery.ExpiresAt()), formatTime(now), formatTime(now)); err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("stage PeerDelivery: persist expiry: %w", err)
	}
	return PeerAdmissionResult{state: PeerAdmissionStateExpired, delivery: delivery}, nil
}

func peerInboxReplyValue(delivery agency.PeerDelivery) any {
	inReplyTo, present := delivery.InReplyToDelivery()
	if !present {
		return nil
	}
	return inReplyTo.String()
}

func peerRouteByRemotePeerTx(ctx context.Context, tx *sql.Tx,
	remotePeer agency.OpaqueHandle,
) (PeerRouteProjection, bool, error) {
	row := tx.QueryRowContext(ctx, peerRouteColumns+` WHERE remote_peer_id = ?`, remotePeer.String())
	route, err := scanPeerRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PeerRouteProjection{}, false, nil
	}
	if err != nil {
		return PeerRouteProjection{}, false, fmt.Errorf("inspect peer route by authenticated identity: %w", err)
	}
	return route, true, nil
}

func peerInboxSignatureMatchesTx(ctx context.Context, tx *sql.Tx, deliveryID agency.DeliveryID,
	routeID agency.RouteID, signature []byte,
) bool {
	var storedRoute string
	var stored []byte
	if err := tx.QueryRowContext(ctx, `SELECT route_id, delivery_signature FROM peer_inbox
		WHERE delivery_id = ?`, deliveryID.String()).Scan(&storedRoute, &stored); err != nil {
		return false
	}
	return storedRoute == routeID.String() && bytes.Equal(stored, signature)
}

func peerInboxResultTx(ctx context.Context, tx *sql.Tx,
	deliveryID agency.DeliveryID,
) (PeerAdmissionResult, bool, error) {
	var routeValue, envelopeValue, state, expiresValue string
	var canonical, receiptJSON []byte
	var inReplyTo, localEventID, receiptDigest sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT route_id, in_reply_to_delivery_id, envelope_digest,
		delivery_json, state, expires_at, local_event_id, receipt_digest, receipt_json
		FROM peer_inbox WHERE delivery_id = ?`, deliveryID.String()).Scan(&routeValue,
		&inReplyTo, &envelopeValue, &canonical, &state, &expiresValue, &localEventID,
		&receiptDigest, &receiptJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return PeerAdmissionResult{}, false, nil
	}
	if err != nil {
		return PeerAdmissionResult{}, false, fmt.Errorf("inspect peer inbox: %w", err)
	}
	routeID, err := agency.NewRouteID(routeValue)
	if err != nil {
		return PeerAdmissionResult{}, false, errors.New("inspect peer inbox: corrupt RouteID")
	}
	parsed, err := agency.ParsePeerDeliveryCanonicalJSON(canonical, routeID)
	if err != nil || parsed.ID() != deliveryID || parsed.EnvelopeDigest().String() != envelopeValue {
		return PeerAdmissionResult{}, false, errors.New("inspect peer inbox: corrupt envelope authority")
	}
	if !peerInboxReplyMatches(parsed.Delivery(), inReplyTo) {
		return PeerAdmissionResult{}, false, errors.New("inspect peer inbox: reply binding diverges from signed envelope")
	}
	expiresAt, err := parseTime(expiresValue)
	if err != nil || !expiresAt.Equal(parsed.Delivery().ExpiresAt()) {
		return PeerAdmissionResult{}, false, errors.New("inspect peer inbox: expiry column diverges")
	}
	result, err := parsePeerInboxStateTx(ctx, tx, parsed.Delivery(), state, localEventID,
		receiptDigest, receiptJSON)
	if err != nil {
		return PeerAdmissionResult{}, false, err
	}
	return result, true, nil
}

func peerInboxReplyMatches(delivery agency.PeerDelivery, stored sql.NullString) bool {
	inReplyTo, present := delivery.InReplyToDelivery()
	return stored.Valid == present && (!present || stored.String == inReplyTo.String())
}

func parsePeerInboxStateTx(ctx context.Context, tx *sql.Tx, delivery agency.PeerDelivery,
	state string, localEventID, receiptDigest sql.NullString, receiptJSON []byte,
) (PeerAdmissionResult, error) {
	result := PeerAdmissionResult{delivery: delivery}
	switch state {
	case "staged":
		if localEventID.Valid || receiptDigest.Valid || len(receiptJSON) != 0 {
			return PeerAdmissionResult{}, errors.New("inspect peer inbox: staged row carries Receipt")
		}
		result.state = PeerAdmissionStateStaged
	case "settled":
		receipt, err := agency.ParsePeerAdmissionReceiptCanonicalJSON(receiptJSON, delivery)
		if err != nil || !receiptDigest.Valid || receipt.Digest().String() != receiptDigest.String {
			return PeerAdmissionResult{}, errors.New("inspect peer inbox: corrupt Receipt")
		}
		if err := validatePeerInboxReceiptEventTx(ctx, tx, receipt, localEventID); err != nil {
			return PeerAdmissionResult{}, err
		}
		result.receipt = receipt
		if receipt.Outcome() == agency.PeerAdmissionOutcomeAccepted {
			result.state = PeerAdmissionStateAccepted
		} else {
			result.state = PeerAdmissionStateRejected
		}
	case "expired":
		if localEventID.Valid || receiptDigest.Valid || len(receiptJSON) != 0 {
			return PeerAdmissionResult{}, errors.New("inspect peer inbox: expired row carries Receipt")
		}
		result.state = PeerAdmissionStateExpired
	default:
		return PeerAdmissionResult{}, errors.New("inspect peer inbox: invalid state")
	}
	return result, nil
}

func validatePeerInboxReceiptEventTx(ctx context.Context, tx *sql.Tx,
	receipt agency.PeerAdmissionReceipt, storedID sql.NullString,
) error {
	localEvent, accepted := receipt.LocalEvent()
	if !accepted {
		if storedID.Valid {
			return errors.New("inspect peer inbox: rejected Receipt names a local Event")
		}
		return nil
	}
	if !storedID.Valid || storedID.String != localEvent.ID().String() {
		return errors.New("inspect peer inbox: accepted Receipt local Event mismatch")
	}
	storedEvent, _, _, _, err := loadStoredEventTx(ctx, tx, storedID.String)
	if err != nil {
		return fmt.Errorf("inspect peer inbox: load accepted local Event: %w", err)
	}
	if storedEvent != localEvent {
		return errors.New("inspect peer inbox: accepted local Event digest mismatch")
	}
	return nil
}

func routeForInboxTx(ctx context.Context, tx *sql.Tx,
	deliveryID agency.DeliveryID,
) (PeerRouteProjection, bool, error) {
	var value string
	if err := tx.QueryRowContext(ctx, `SELECT route_id FROM peer_inbox WHERE delivery_id = ?`,
		deliveryID.String()).Scan(&value); errors.Is(err, sql.ErrNoRows) {
		return PeerRouteProjection{}, false, nil
	} else if err != nil {
		return PeerRouteProjection{}, false, err
	}
	routeID, err := agency.NewRouteID(value)
	if err != nil {
		return PeerRouteProjection{}, false, errors.New("peer inbox: corrupt RouteID")
	}
	return peerRouteByIDTx(ctx, tx, routeID)
}
