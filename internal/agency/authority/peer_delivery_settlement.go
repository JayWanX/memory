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

// SettlePeerDelivery accepts only a signed receiver-local admission Receipt.
// Transport ACK and stream success have no representation here.
func (s *Store) SettlePeerDelivery(ctx context.Context, remotePeer agency.OpaqueHandle,
	deliveryID agency.DeliveryID, canonical, signature []byte,
) (agency.PeerAdmissionReceipt, bool, error) {
	if ctx == nil || remotePeer.IsZero() || deliveryID.IsZero() || len(canonical) == 0 || len(signature) == 0 {
		return agency.PeerAdmissionReceipt{}, false,
			errors.New("settle PeerDelivery: exact signed Receipt is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return agency.PeerAdmissionReceipt{}, false, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return agency.PeerAdmissionReceipt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agency.PeerAdmissionReceipt{}, false, fmt.Errorf("settle PeerDelivery: begin: %w", err)
	}
	defer tx.Rollback()
	receipt, replayed, err := settlePeerDeliveryTx(ctx, tx, remotePeer, deliveryID,
		canonical, signature, now)
	if err != nil {
		return agency.PeerAdmissionReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return agency.PeerAdmissionReceipt{}, false, fmt.Errorf("settle PeerDelivery: commit: %w", err)
	}
	return receipt, replayed, nil
}

func settlePeerDeliveryTx(ctx context.Context, tx *sql.Tx, remotePeer agency.OpaqueHandle,
	deliveryID agency.DeliveryID, canonical, signature []byte, now time.Time,
) (agency.PeerAdmissionReceipt, bool, error) {
	pending, state, storedDigest, storedReceipt, storedSignature, err :=
		outboxSettlementAuthorityTx(ctx, tx, deliveryID)
	if err != nil {
		return agency.PeerAdmissionReceipt{}, false, err
	}
	if state == "settled" {
		return replayPeerSettlement(pending, remotePeer, storedDigest, storedReceipt,
			storedSignature, canonical, signature)
	}
	receipt, err := validateFreshPeerSettlement(pending, state, remotePeer, canonical, signature, now)
	if err != nil {
		return agency.PeerAdmissionReceipt{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE peer_outbox SET state = 'settled',
		receipt_digest = ?, receipt_json = ?, receipt_signature = ?, settled_at = ?
		WHERE delivery_id = ? AND state = 'pending'`, receipt.Digest().String(),
		receipt.CanonicalJSON(), append([]byte(nil), signature...), formatTime(now), deliveryID.String())
	if err != nil {
		return agency.PeerAdmissionReceipt{}, false, fmt.Errorf("settle PeerDelivery: update: %w", err)
	}
	if err := requireOneRow(result, "peer outbox settlement"); err != nil {
		return agency.PeerAdmissionReceipt{}, false, err
	}
	return receipt, false, nil
}

func replayPeerSettlement(pending PendingPeerDelivery, remotePeer agency.OpaqueHandle,
	storedDigest sql.NullString, storedReceipt, storedSignature, canonical, signature []byte,
) (agency.PeerAdmissionReceipt, bool, error) {
	if pending.route.RemotePeerID() != remotePeer {
		return agency.PeerAdmissionReceipt{}, false, ErrPeerAuthentication
	}
	presented, err := agency.ParsePeerAdmissionReceiptCanonicalJSON(canonical, pending.delivery)
	if err != nil || !verifyPeerSignature(pending.route.RemotePublicKey(),
		presented.SigningMessage(), signature) {
		return agency.PeerAdmissionReceipt{}, false, ErrPeerAuthentication
	}
	stored, err := agency.ParsePeerAdmissionReceiptCanonicalJSON(storedReceipt, pending.delivery)
	if err != nil || !storedDigest.Valid || stored.Digest().String() != storedDigest.String {
		return agency.PeerAdmissionReceipt{}, false, errors.New("settle PeerDelivery: corrupt stored Receipt")
	}
	if !bytes.Equal(storedReceipt, canonical) || !bytes.Equal(storedSignature, signature) {
		return agency.PeerAdmissionReceipt{}, false, ErrPeerDeliveryConflict
	}
	return stored, true, nil
}

func validateFreshPeerSettlement(pending PendingPeerDelivery, state string,
	remotePeer agency.OpaqueHandle, canonical, signature []byte, now time.Time,
) (agency.PeerAdmissionReceipt, error) {
	if state != "pending" || !pending.route.Active() || !now.Before(pending.delivery.ExpiresAt()) {
		return agency.PeerAdmissionReceipt{}, ErrPeerDeliveryExpired
	}
	if pending.route.RemotePeerID() != remotePeer {
		return agency.PeerAdmissionReceipt{}, ErrPeerAuthentication
	}
	receipt, err := agency.ParsePeerAdmissionReceiptCanonicalJSON(canonical, pending.delivery)
	if err != nil || !verifyPeerSignature(pending.route.RemotePublicKey(), receipt.SigningMessage(), signature) {
		return agency.PeerAdmissionReceipt{}, ErrPeerAuthentication
	}
	return receipt, nil
}

func outboxSettlementAuthorityTx(ctx context.Context, tx *sql.Tx,
	deliveryID agency.DeliveryID,
) (PendingPeerDelivery, string, sql.NullString, []byte, []byte, error) {
	var row pendingPeerDeliveryRow
	var state string
	var receiptDigest sql.NullString
	var receiptJSON, signature []byte
	err := tx.QueryRowContext(ctx, `SELECT delivery_id, route_id, origin_event_id,
		envelope_digest, delivery_json, expires_at, created_at, state,
		receipt_digest, receipt_json, receipt_signature FROM peer_outbox WHERE delivery_id = ?`,
		deliveryID.String()).
		Scan(&row.deliveryID, &row.routeID, &row.originEventID, &row.envelopeDigest,
			&row.deliveryJSON, &row.expiresAt, &row.createdAt, &state, &receiptDigest,
			&receiptJSON, &signature)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingPeerDelivery{}, "", sql.NullString{}, nil, nil, ErrPeerDeliveryUnavailable
	}
	if err != nil {
		return PendingPeerDelivery{}, "", sql.NullString{}, nil, nil,
			fmt.Errorf("settle PeerDelivery: inspect outbox: %w", err)
	}
	pending, err := parsePeerDeliveryRowTx(ctx, tx, row)
	return pending, state, receiptDigest, receiptJSON, signature, err
}
