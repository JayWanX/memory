package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

// AdmitPeerDelivery performs receiver-local admission. Missing Artifact bytes
// leave the candidate staged; final admission re-hashes every exact CAS object
// outside SQLite and then rechecks all mutable authority in one transaction.
func (s *Store) AdmitPeerDelivery(ctx context.Context,
	deliveryID agency.DeliveryID,
) (PeerAdmissionResult, error) {
	checks, early, done, err := s.preflightPeerAdmission(ctx, deliveryID)
	if err != nil || done {
		return early, err
	}
	for _, check := range checks {
		if s.artifactVerifier == nil {
			return early, ErrArtifactUnavailable
		}
		if err := s.artifactVerifier.VerifyArtifact(ctx, check.digest, check.size); err != nil {
			return early, ErrArtifactUnavailable
		}
	}
	// An inbound candidate can create at most one local Handling. The exact
	// cardinality is selected later by the single decidedPeerEffect; terminal
	// observations simply leave this preallocated ID unused.
	handlingIDs, err := newHandlingIDs(1)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	return s.commitPeerAdmission(ctx, deliveryID, checks, handlingIDs)
}

func (s *Store) preflightPeerAdmission(ctx context.Context, deliveryID agency.DeliveryID) (
	[]artifactCheck, PeerAdmissionResult, bool, error,
) {
	if ctx == nil || deliveryID.IsZero() {
		return nil, PeerAdmissionResult{}, false, errors.New("admit PeerDelivery: DeliveryID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, PeerAdmissionResult{}, false, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return nil, PeerAdmissionResult{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, PeerAdmissionResult{}, false, fmt.Errorf("admit PeerDelivery preflight: begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := expirePeerInboxDeliveryTx(ctx, tx, deliveryID, now); err != nil {
		return nil, PeerAdmissionResult{}, false, err
	}
	result, found, err := peerInboxResultTx(ctx, tx, deliveryID)
	if err != nil || !found {
		if !found && err == nil {
			err = ErrPeerDeliveryUnavailable
		}
		return nil, PeerAdmissionResult{}, false, err
	}
	if result.state != PeerAdmissionStateStaged {
		result.replayed = true
		if err := tx.Commit(); err != nil {
			return nil, PeerAdmissionResult{}, false,
				fmt.Errorf("admit PeerDelivery preflight: commit expiry: %w", err)
		}
		return nil, result, true, nil
	}
	checks, err := artifactChecksTx(ctx, tx, result.delivery.Artifacts())
	if errors.Is(err, ErrArtifactUnavailable) {
		if err := tx.Commit(); err != nil {
			return nil, PeerAdmissionResult{}, false,
				fmt.Errorf("admit PeerDelivery preflight: commit expiry: %w", err)
		}
		return nil, result, true, nil
	}
	if err == nil {
		err = tx.Commit()
	}
	return checks, result, false, err
}

func expirePeerInboxDeliveryTx(ctx context.Context, tx *sql.Tx, deliveryID agency.DeliveryID,
	now time.Time,
) (bool, error) {
	result, err := tx.ExecContext(ctx, `UPDATE peer_inbox SET state = 'expired', settled_at = ?
		WHERE delivery_id = ? AND state = 'staged' AND expires_at <= ?`, formatTime(now),
		deliveryID.String(), formatTime(now))
	if err != nil {
		return false, fmt.Errorf("expire PeerDelivery preflight: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("expire PeerDelivery preflight: count update: %w", err)
	}
	return changed == 1, nil
}

func (s *Store) commitPeerAdmission(ctx context.Context, deliveryID agency.DeliveryID,
	checks []artifactCheck, handlingIDs []agency.HandlingID,
) (PeerAdmissionResult, error) {
	eventID, err := newEventID()
	if err != nil {
		return PeerAdmissionResult{}, err
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
		return PeerAdmissionResult{}, fmt.Errorf("admit PeerDelivery: begin: %w", err)
	}
	defer tx.Rollback()
	prepared, done, err := preparePeerAdmissionTx(ctx, tx, deliveryID, checks, now)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	result := prepared.result
	if !done {
		result, err = commitAcceptedPeerAdmissionTx(ctx, tx, prepared, eventID, handlingIDs, now)
		if err != nil {
			return PeerAdmissionResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("admit PeerDelivery: commit: %w", err)
	}
	return result, nil
}

type preparedPeerAdmission struct {
	result   PeerAdmissionResult
	verified agency.VerifiedPeerDelivery
}

func preparePeerAdmissionTx(ctx context.Context, tx *sql.Tx, deliveryID agency.DeliveryID,
	checks []artifactCheck, now time.Time,
) (preparedPeerAdmission, bool, error) {
	result, found, err := peerInboxResultTx(ctx, tx, deliveryID)
	if err != nil || !found {
		if !found && err == nil {
			err = ErrPeerDeliveryUnavailable
		}
		return preparedPeerAdmission{}, false, err
	}
	if result.state != PeerAdmissionStateStaged {
		result.replayed = true
		return preparedPeerAdmission{result: result}, true, nil
	}
	route, found, err := routeForInboxTx(ctx, tx, deliveryID)
	if err != nil || !found {
		return preparedPeerAdmission{}, false, errors.New("admit PeerDelivery: route unavailable")
	}
	if !route.Active() {
		rejected, err := settleRejectedPeerInboxTx(ctx, tx, result.delivery,
			"peer.route_revoked", "route was revoked before local admission", now)
		return preparedPeerAdmission{result: rejected}, true, err
	}
	if !now.Before(result.delivery.ExpiresAt()) {
		expired, err := expirePeerInboxTx(ctx, tx, result.delivery, now)
		return preparedPeerAdmission{result: expired}, true, err
	}
	latest, err := artifactChecksTx(ctx, tx, result.delivery.Artifacts())
	if err != nil || !sameArtifactChecks(checks, latest) {
		if errors.Is(err, ErrArtifactUnavailable) {
			return preparedPeerAdmission{result: result}, true, nil
		}
		return preparedPeerAdmission{}, false, errors.New("admit PeerDelivery: Artifact catalog changed")
	}
	verifiedArtifacts, err := peerVerifiedArtifacts(latest, now)
	if err != nil {
		return preparedPeerAdmission{}, false, err
	}
	parsed, err := agency.ParsePeerDeliveryCanonicalJSON(result.delivery.CanonicalJSON(), route.RouteID())
	if err != nil {
		return preparedPeerAdmission{}, false, errors.New("admit PeerDelivery: corrupt staged envelope")
	}
	if !storedPeerInboxSignatureValidTx(ctx, tx, deliveryID, route, parsed.SigningMessage()) {
		return preparedPeerAdmission{}, false, errors.New("admit PeerDelivery: corrupt staged signature")
	}
	verified, err := agency.NewVerifiedPeerDelivery(parsed, route.SurrogateSourcePrincipal(),
		route.LocalTargetPrincipal(), verifiedArtifacts)
	if err != nil {
		return preparedPeerAdmission{}, false, err
	}
	if verified.Delivery().RequiresTerminalReplyMatch() {
		_, matched, err := matchOpenTerminalReplyAnchorTx(ctx, tx, verified, route.RouteID())
		if err != nil {
			return preparedPeerAdmission{}, false, err
		}
		if !matched {
			rejected, err := settleRejectedPeerInboxTx(ctx, tx, result.delivery,
				"peer.terminal_reply_unmatched",
				"terminal reply does not match an open local responsibility", now)
			return preparedPeerAdmission{result: rejected}, true, err
		}
	}
	return preparedPeerAdmission{result: result, verified: verified}, false, nil
}

func storedPeerInboxSignatureValidTx(ctx context.Context, tx *sql.Tx,
	deliveryID agency.DeliveryID, route PeerRouteProjection, message []byte,
) bool {
	var signature []byte
	if err := tx.QueryRowContext(ctx, `SELECT delivery_signature FROM peer_inbox
		WHERE delivery_id = ? AND route_id = ?`, deliveryID.String(), route.RouteID().String()).
		Scan(&signature); err != nil {
		return false
	}
	return verifyPeerSignature(route.RemotePublicKey(), message, signature)
}

func peerVerifiedArtifacts(checks []artifactCheck,
	now time.Time,
) ([]agency.VerifiedPeerArtifact, error) {
	result := make([]agency.VerifiedPeerArtifact, 0, len(checks))
	for _, check := range checks {
		verified, err := agency.NewVerifiedPeerArtifact(check.digest, int64(check.size), now)
		if err != nil {
			return nil, err
		}
		result = append(result, verified)
	}
	return result, nil
}

func commitAcceptedPeerAdmissionTx(ctx context.Context, tx *sql.Tx,
	prepared preparedPeerAdmission, eventID agency.EventID, handlingIDs []agency.HandlingID,
	now time.Time,
) (PeerAdmissionResult, error) {
	delivery := prepared.result.delivery
	effect, err := decidePeerEffect(prepared.verified)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	if len(handlingIDs) != 1 || len(effect.targets) > len(handlingIDs) {
		return PeerAdmissionResult{}, errors.New("admit PeerDelivery: Handling ID pool is invalid")
	}
	handlingIDs = handlingIDs[:len(effect.targets)]
	event, err := commitDomainAdmissionTx(ctx, tx,
		peerDomainAdmission(prepared.verified, effect), eventID, handlingIDs, now)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	receipt, err := agency.NewAcceptedPeerAdmissionReceipt(delivery, event.Ref(), now)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	update, err := tx.ExecContext(ctx, `UPDATE peer_inbox SET state = 'settled', local_event_id = ?,
		receipt_digest = ?, receipt_json = ?, settled_at = ?
		WHERE delivery_id = ? AND state = 'staged'`, event.ID().String(), receipt.Digest().String(),
		receipt.CanonicalJSON(), formatTime(now), delivery.ID().String())
	if err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("admit PeerDelivery: settle inbox: %w", err)
	}
	if err := requireOneRow(update, "peer inbox admission"); err != nil {
		return PeerAdmissionResult{}, err
	}
	return PeerAdmissionResult{state: PeerAdmissionStateAccepted, delivery: delivery,
		receipt: receipt}, nil
}

func sameArtifactChecks(left, right []artifactCheck) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func settleRejectedPeerInboxTx(ctx context.Context, tx *sql.Tx, delivery agency.PeerDelivery,
	codeValue, diagnostic string, now time.Time,
) (PeerAdmissionResult, error) {
	code, err := agency.NewSemanticLabel(codeValue)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	receipt, err := agency.NewRejectedPeerAdmissionReceipt(delivery, code, diagnostic, now)
	if err != nil {
		return PeerAdmissionResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE peer_inbox SET state = 'settled',
		receipt_digest = ?, receipt_json = ?, settled_at = ?
		WHERE delivery_id = ? AND state = 'staged'`, receipt.Digest().String(),
		receipt.CanonicalJSON(), formatTime(now), delivery.ID().String())
	if err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("admit PeerDelivery: persist rejection: %w", err)
	}
	if err := requireOneRow(result, "peer inbox rejection"); err != nil {
		return PeerAdmissionResult{}, err
	}
	return PeerAdmissionResult{state: PeerAdmissionStateRejected, delivery: delivery,
		receipt: receipt}, nil
}

func expirePeerInboxTx(ctx context.Context, tx *sql.Tx, delivery agency.PeerDelivery,
	now time.Time,
) (PeerAdmissionResult, error) {
	result, err := tx.ExecContext(ctx, `UPDATE peer_inbox SET state = 'expired', settled_at = ?
		WHERE delivery_id = ? AND state = 'staged'`, formatTime(now), delivery.ID().String())
	if err != nil {
		return PeerAdmissionResult{}, fmt.Errorf("admit PeerDelivery: persist expiry: %w", err)
	}
	if err := requireOneRow(result, "peer inbox expiry"); err != nil {
		return PeerAdmissionResult{}, err
	}
	return PeerAdmissionResult{state: PeerAdmissionStateExpired, delivery: delivery}, nil
}
