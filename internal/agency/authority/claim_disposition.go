package authority

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

// MaxClaimExpirySettlementsPerCurrent bounds maintenance work performed by one
// fresh interactive Current operation. Excess expired claims remain durable
// and are considered by later turns.
const MaxClaimExpirySettlementsPerCurrent = 16

const claimExpiryOperationDomain = "memory.claim-expiry.operation.v1\x00"
const claimBoundaryEndOperationDomain = "memory.claim-boundary-end.operation.v1\x00"

type expiredClaim struct {
	handling   agency.HandlingID
	attachment agency.AttachmentID
	fence      uint64
	claimUntil time.Time
}

type claimExpiryRequestWire struct {
	Schema     string `json:"schema"`
	Handling   string `json:"handling"`
	Attachment string `json:"attachment"`
	Fence      uint64 `json:"fence"`
	ClaimUntil string `json:"claim_until"`
}

type claimExpiryOutcomeWire struct {
	Schema        string `json:"schema"`
	OperationKey  string `json:"operation_key"`
	RequestDigest string `json:"request_digest"`
	Outcome       string `json:"outcome"`
	RecordedAt    string `json:"recorded_at"`
}

func settleExpiredClaimsTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	now time.Time, limit int,
) error {
	if limit <= 0 {
		return errors.New("claim expiry: positive settlement bound is required")
	}
	if principal.IsZero() {
		return errors.New("claim expiry: Principal is required")
	}
	claims, err := loadExpiredClaimsTx(ctx, tx, principal, now, limit)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		if err := settleClaimExpiryTx(ctx, tx, now, claim); err != nil {
			return err
		}
	}
	return nil
}

func loadExpiredClaimsTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	now time.Time, limit int,
) ([]expiredClaim, error) {
	rows, err := tx.QueryContext(ctx, `SELECT handling_id, claim_attachment_id, claim_fence, claim_until
		FROM handlings WHERE target_principal_id = ? AND state = 'open'
		AND claim_until IS NOT NULL AND claim_until <= ?
		ORDER BY claim_until, handling_id LIMIT ?`, principal.String(), formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("claim expiry: list: %w", err)
	}
	defer rows.Close()
	claims := make([]expiredClaim, 0, limit)
	for rows.Next() {
		claim, err := scanExpiredClaim(rows)
		if err != nil {
			return nil, err
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim expiry: list: %w", err)
	}
	return claims, nil
}

func scanExpiredClaim(row rowScanner) (expiredClaim, error) {
	var handlingValue, attachmentValue, untilValue string
	var fence uint64
	if err := row.Scan(&handlingValue, &attachmentValue, &fence, &untilValue); err != nil {
		return expiredClaim{}, fmt.Errorf("claim expiry: scan: %w", err)
	}
	handling, err := agency.NewHandlingID(handlingValue)
	if err != nil {
		return expiredClaim{}, errors.New("claim expiry: corrupt Handling ID")
	}
	attachment, err := agency.NewAttachmentID(attachmentValue)
	if err != nil || fence == 0 {
		return expiredClaim{}, errors.New("claim expiry: corrupt claim authority")
	}
	claimUntil, err := parseTime(untilValue)
	if err != nil {
		return expiredClaim{}, err
	}
	return expiredClaim{handling: handling, attachment: attachment, fence: fence,
		claimUntil: claimUntil}, nil
}

func settleClaimExpiryTx(ctx context.Context, tx *sql.Tx, now time.Time, claim expiredClaim) error {
	key, requestDigest, err := claimExpiryIdentity(claim)
	if err != nil {
		return err
	}
	if found, err := replayClaimExpiryTx(ctx, tx, claim, key, requestDigest); err != nil || found {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE handlings
		SET claim_attachment_id = NULL, claim_until = NULL
		WHERE handling_id = ? AND state = 'open' AND claim_attachment_id = ?
		AND claim_fence = ? AND claim_until = ? AND claim_until <= ?`,
		claim.handling.String(), claim.attachment.String(), claim.fence,
		formatTime(claim.claimUntil), formatTime(now))
	if err != nil {
		return fmt.Errorf("claim expiry: clear occupancy: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("claim expiry: inspect settlement cardinality: %w", err)
	}
	if rows != 1 {
		return errors.New("claim expiry: exact claim changed before settlement")
	}
	return insertClaimExpiryTx(ctx, tx, now, claim, key, requestDigest)
}

func claimExpiryIdentity(claim expiredClaim) (agency.OperationKey, agency.Digest, error) {
	request, err := json.Marshal(claimExpiryRequestWire{Schema: "memory.claim-expiry",
		Handling: claim.handling.String(), Attachment: claim.attachment.String(), Fence: claim.fence,
		ClaimUntil: formatTime(claim.claimUntil)})
	if err != nil {
		return agency.OperationKey{}, agency.Digest{}, fmt.Errorf("claim expiry: encode request: %w", err)
	}
	digest := agency.Sum(request)
	keyMaterial := make([]byte, len(claimExpiryOperationDomain)+len(request))
	copy(keyMaterial, claimExpiryOperationDomain)
	copy(keyMaterial[len(claimExpiryOperationDomain):], request)
	keyDigest := agency.Sum(keyMaterial)
	key, err := agency.NewOperationKey("claim-expiry:" + keyDigest.String())
	if err != nil {
		return agency.OperationKey{}, agency.Digest{}, fmt.Errorf("claim expiry: operation key: %w", err)
	}
	return key, digest, nil
}

func replayClaimExpiryTx(ctx context.Context, tx *sql.Tx, claim expiredClaim,
	key agency.OperationKey, requestDigest agency.Digest,
) (bool, error) {
	var storedRequest, handlingValue, attachmentValue, untilValue string
	var outcomeDigestValue, recordedValue string
	var fence uint64
	var outcome []byte
	err := tx.QueryRowContext(ctx, `SELECT request_digest, handling_id, attachment_id,
		claim_fence, claim_until, outcome_digest, outcome_json, recorded_at
		FROM claim_dispositions WHERE disposition_key = ? AND disposition_kind = 'expiry'`, key.String()).
		Scan(&storedRequest, &handlingValue, &attachmentValue, &fence, &untilValue,
			&outcomeDigestValue, &outcome, &recordedValue)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim expiry: inspect replay: %w", err)
	}
	if storedRequest != requestDigest.String() {
		return false, ErrOperationConflict
	}
	if handlingValue != claim.handling.String() || attachmentValue != claim.attachment.String() ||
		fence != claim.fence || untilValue != formatTime(claim.claimUntil) {
		return false, errors.New("claim expiry: replay authority diverges")
	}
	outcomeDigest, err := agency.ParseDigest(outcomeDigestValue)
	if err != nil || agency.Sum(outcome) != outcomeDigest ||
		validateClaimExpiryOutcome(outcome, claim, key, requestDigest, recordedValue) != nil {
		return false, errors.New("claim expiry: corrupt replay outcome")
	}
	var stillOccupied int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM handlings
		WHERE handling_id = ? AND state = 'open' AND claim_attachment_id = ?
		AND claim_fence = ? AND claim_until = ?)`, handlingValue, attachmentValue,
		fence, untilValue).Scan(&stillOccupied); err != nil {
		return false, fmt.Errorf("claim expiry: inspect replay effect: %w", err)
	}
	if stillOccupied != 0 {
		return false, errors.New("claim expiry: replay outcome exists without released claim")
	}
	return true, nil
}

func validateClaimExpiryOutcome(value []byte, claim expiredClaim, key agency.OperationKey,
	requestDigest agency.Digest, recordedValue string,
) error {
	var outcome claimExpiryOutcomeWire
	if err := json.Unmarshal(value, &outcome); err != nil {
		return err
	}
	canonical, err := json.Marshal(outcome)
	if err != nil || !bytes.Equal(canonical, value) {
		return errors.New("claim expiry: outcome is not canonical")
	}
	if outcome.Schema != "memory.claim-expiry-outcome" || outcome.OperationKey != key.String() ||
		outcome.RequestDigest != requestDigest.String() || outcome.Outcome != "claim_released" ||
		outcome.RecordedAt != recordedValue {
		return errors.New("claim expiry: outcome authority diverges")
	}
	recordedAt, err := parseTime(recordedValue)
	if err != nil {
		return err
	}
	if recordedAt.Before(claim.claimUntil) {
		return errors.New("claim expiry: outcome predates the expired claim")
	}
	return nil
}

func insertClaimExpiryTx(ctx context.Context, tx *sql.Tx, now time.Time, claim expiredClaim,
	key agency.OperationKey, requestDigest agency.Digest,
) error {
	outcome, err := json.Marshal(claimExpiryOutcomeWire{Schema: "memory.claim-expiry-outcome",
		OperationKey: key.String(), RequestDigest: requestDigest.String(),
		Outcome: "claim_released", RecordedAt: formatTime(now)})
	if err != nil {
		return fmt.Errorf("claim expiry: encode outcome: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO claim_dispositions(
		disposition_key, disposition_kind, request_digest, handling_id, attachment_id, claim_fence,
		claim_until, outcome_digest, outcome_json, recorded_at)
		VALUES(?, 'expiry', ?, ?, ?, ?, ?, ?, ?, ?)`, key.String(), requestDigest.String(),
		claim.handling.String(), claim.attachment.String(), claim.fence,
		formatTime(claim.claimUntil), agency.Sum(outcome).String(), outcome, formatTime(now))
	if err != nil {
		return fmt.Errorf("claim expiry: persist outcome: %w", err)
	}
	return nil
}

func settleClaimBoundaryEndTx(ctx context.Context, tx *sql.Tx, now time.Time,
	claim expiredClaim,
) error {
	request, err := json.Marshal(claimExpiryRequestWire{Schema: "memory.claim-boundary-end",
		Handling: claim.handling.String(), Attachment: claim.attachment.String(), Fence: claim.fence,
		ClaimUntil: formatTime(claim.claimUntil)})
	if err != nil {
		return fmt.Errorf("claim boundary end: encode request: %w", err)
	}
	requestDigest := agency.Sum(request)
	keyMaterial := append([]byte(claimBoundaryEndOperationDomain), request...)
	keyDigest := agency.Sum(keyMaterial)
	clear(keyMaterial)
	key, err := agency.NewOperationKey("claim-boundary-end:" + keyDigest.String())
	if err != nil {
		return fmt.Errorf("claim boundary end: operation key: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE handlings
		SET claim_attachment_id = NULL, claim_until = NULL
		WHERE handling_id = ? AND state = 'open' AND claim_attachment_id = ?
		AND claim_fence = ? AND claim_until = ?`, claim.handling.String(),
		claim.attachment.String(), claim.fence, formatTime(claim.claimUntil))
	if err != nil {
		return fmt.Errorf("claim boundary end: clear occupancy: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return errors.New("claim boundary end: exact claim changed before settlement")
	}
	outcome, err := json.Marshal(claimExpiryOutcomeWire{Schema: "memory.claim-boundary-end-outcome",
		OperationKey: key.String(), RequestDigest: requestDigest.String(),
		Outcome: "claim_released", RecordedAt: formatTime(now)})
	if err != nil {
		return fmt.Errorf("claim boundary end: encode outcome: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO claim_dispositions(
		disposition_key, disposition_kind, request_digest, handling_id, attachment_id,
		claim_fence, claim_until, outcome_digest, outcome_json, recorded_at)
		VALUES(?, 'boundary_end', ?, ?, ?, ?, ?, ?, ?, ?)`, key.String(),
		requestDigest.String(), claim.handling.String(), claim.attachment.String(), claim.fence,
		formatTime(claim.claimUntil), agency.Sum(outcome).String(), outcome, formatTime(now))
	if err != nil {
		return fmt.Errorf("claim boundary end: persist outcome: %w", err)
	}
	return nil
}
