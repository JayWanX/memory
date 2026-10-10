package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

// StagedPeerDeliveries is a bounded projection for one supervised worker. It
// does not claim, mutate, or infer admission; duplicate processing is safe.
func (s *Store) StagedPeerDeliveries(ctx context.Context,
	limit int,
) ([]StagedPeerDelivery, error) {
	if ctx == nil || limit < 1 || limit > MaxStagedPeerDeliveries {
		return nil, errors.New("project staged PeerDelivery: bounded positive limit is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("project staged PeerDelivery: begin: %w", err)
	}
	defer tx.Rollback()
	if err := expirePeerInboxRowsTx(ctx, tx, now); err != nil {
		return nil, err
	}
	ids, err := stagedPeerDeliveryIDsTx(ctx, tx, limit)
	if err != nil {
		return nil, err
	}
	result := make([]StagedPeerDelivery, 0, len(ids))
	for _, id := range ids {
		projected, found, err := peerInboxResultTx(ctx, tx, id)
		if err != nil || !found || projected.state != PeerAdmissionStateStaged {
			return nil, errors.New("project staged PeerDelivery: durable state changed or is corrupt")
		}
		route, found, err := routeForInboxTx(ctx, tx, id)
		if err != nil || !found {
			return nil, errors.New("project staged PeerDelivery: route unavailable")
		}
		result = append(result, StagedPeerDelivery{route: route, delivery: projected.delivery})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("project staged PeerDelivery: commit expiry: %w", err)
	}
	return result, nil
}

func stagedPeerDeliveryIDsTx(ctx context.Context, tx *sql.Tx,
	limit int,
) ([]agency.DeliveryID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT delivery_id FROM peer_inbox
		WHERE state = 'staged' ORDER BY received_at, delivery_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("project staged PeerDelivery: query: %w", err)
	}
	defer rows.Close()
	var ids []agency.DeliveryID
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("project staged PeerDelivery: scan: %w", err)
		}
		id, err := agency.ParseDeliveryID(value)
		if err != nil {
			return nil, errors.New("project staged PeerDelivery: corrupt ID")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project staged PeerDelivery: iterate: %w", err)
	}
	return ids, nil
}

func expirePeerInboxRowsTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE peer_inbox SET state = 'expired', settled_at = ?
		WHERE state = 'staged' AND expires_at <= ?`, formatTime(now), formatTime(now)); err != nil {
		return fmt.Errorf("expire staged PeerDelivery: %w", err)
	}
	return nil
}
