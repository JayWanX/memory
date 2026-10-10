package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

// AdmissionResult carries exact durable Receipt bytes. Replay returns the same
// bytes and digest; it is not a third outcome and creates no new Event.
type AdmissionResult struct {
	receiptJSON   []byte
	receiptDigest agency.Digest
	outcome       agency.ReceiptOutcome
	replayed      bool
}

func (result AdmissionResult) ReceiptJSON() []byte {
	return append([]byte(nil), result.receiptJSON...)
}
func (result AdmissionResult) ReceiptDigest() agency.Digest   { return result.receiptDigest }
func (result AdmissionResult) Outcome() agency.ReceiptOutcome { return result.outcome }
func (result AdmissionResult) Replayed() bool                 { return result.replayed }

type artifactCheck struct {
	digest agency.Digest
	size   int64
}

// Admit is the one local authority entrance for a BoundIntent. It first proves
// actor identity and operation namespace, returns exact replay before checking
// mutable authority, verifies immutable Artifact bytes outside the SQLite
// transaction, then repeats auth/replay and commits one atomic effect.
func (s *Store) Admit(ctx context.Context, proof AttachmentProof,
	request agency.BoundIntent,
) (AdmissionResult, error) {
	if ctx == nil || request.OperationKey().IsZero() || request.RequestDigest().IsZero() {
		return AdmissionResult{}, errors.New("admit Intent: complete BoundIntent is required")
	}
	checks, replay, found, err := s.preflightAdmission(ctx, proof, request)
	if err != nil || found {
		return replay, err
	}
	artifactFailure, err := s.verifyAdmissionArtifacts(ctx, checks)
	if err != nil {
		return AdmissionResult{}, err
	}
	return s.commitAdmission(ctx, proof, request, artifactFailure)
}

func (s *Store) verifyAdmissionArtifacts(ctx context.Context,
	checks []artifactCheck,
) (bool, error) {
	if len(checks) == 0 {
		return false, nil
	}
	if s.artifactVerifier == nil {
		return true, nil
	}
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := s.artifactVerifier.VerifyArtifact(ctx, check.digest, check.size); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return false, ctxErr
			}
			return true, nil
		}
	}
	return false, nil
}

// preflightAdmission performs no effect. Its only purpose is preserving P-07
// ordering and moving CAS I/O outside the authority transaction.
func (s *Store) preflightAdmission(ctx context.Context, proof AttachmentProof,
	request agency.BoundIntent,
) ([]artifactCheck, AdmissionResult, bool, error) {
	now, err := s.trustedNow()
	if err != nil {
		return nil, AdmissionResult{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, AdmissionResult{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, AdmissionResult{}, false, fmt.Errorf("admit Intent preflight: begin: %w", err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(ctx, tx, proof)
	if err != nil {
		return nil, AdmissionResult{}, false, err
	}
	if replay, found, err := operationReplayTx(ctx, tx, authenticated.value.Principal(), request); err != nil || found {
		return nil, replay, found, err
	}
	if err := requireBoundAttachment(authenticated, request); err != nil {
		return nil, AdmissionResult{}, false, err
	}
	if err := requireLiveAttachment(authenticated, now); err != nil {
		return nil, AdmissionResult{}, false, nil
	}
	if rejection, err := validateMutableAuthorityTx(ctx, tx, authenticated.value, request, now); err != nil {
		return nil, AdmissionResult{}, false, err
	} else if rejection != nil {
		return nil, AdmissionResult{}, false, nil
	}
	checks, err := artifactChecksTx(ctx, tx, request.Artifacts())
	if errors.Is(err, ErrArtifactUnavailable) {
		return nil, AdmissionResult{}, false, nil
	}
	if err != nil {
		return nil, AdmissionResult{}, false, err
	}
	return checks, AdmissionResult{}, false, nil
}

func (s *Store) commitAdmission(ctx context.Context, proof AttachmentProof,
	request agency.BoundIntent, artifactFailure bool,
) (AdmissionResult, error) {
	now, err := s.trustedNow()
	if err != nil {
		return AdmissionResult{}, err
	}
	eventID, err := newEventID()
	if err != nil {
		return AdmissionResult{}, err
	}
	handlingIDs, err := newHandlingIDs(localTargetCount(request.Targets()))
	if err != nil {
		return AdmissionResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return AdmissionResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("admit Intent: begin: %w", err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(ctx, tx, proof)
	if err != nil {
		return AdmissionResult{}, err
	}
	if replay, found, err := operationReplayTx(ctx, tx, authenticated.value.Principal(), request); err != nil || found {
		return replay, err
	}
	if err := requireBoundAttachment(authenticated, request); err != nil {
		return AdmissionResult{}, err
	}
	rejection, err := freshAdmissionRejectionTx(ctx, tx, authenticated, request, artifactFailure, now)
	if err != nil {
		return AdmissionResult{}, err
	}
	if rejection != nil {
		return persistRejectionTx(ctx, tx, authenticated.value.Principal(), request,
			rejection.code, rejection.diagnostic, now)
	}
	return commitAcceptedAdmissionTx(ctx, tx, authenticated.value.Principal(), request,
		eventID, handlingIDs, now)
}

func freshAdmissionRejectionTx(ctx context.Context, tx *sql.Tx,
	authenticated authenticatedAttachment, request agency.BoundIntent,
	artifactFailure bool, now time.Time,
) (*admissionRejection, error) {
	if err := requireLiveAttachment(authenticated, now); err != nil {
		return reject(rejectionAttachmentExpired, "attachment expired"), nil
	}
	if artifactFailure {
		return reject(rejectionArtifactUnavailable, "Artifact bytes unavailable or invalid"), nil
	}
	if err := requireArtifacts(ctx, tx, request.Artifacts()); err != nil {
		if errors.Is(err, ErrArtifactUnavailable) {
			return reject(rejectionArtifactUnavailable, "Artifact is not in the verified catalog"), nil
		}
		return nil, err
	}
	return validateMutableAuthorityTx(ctx, tx, authenticated.value, request, now)
}

func commitAcceptedAdmissionTx(ctx context.Context, tx *sql.Tx,
	principal agency.AgentPrincipalID, request agency.BoundIntent,
	eventID agency.EventID, handlingIDs []agency.HandlingID, now time.Time,
) (AdmissionResult, error) {
	event, err := commitDomainAdmissionTx(ctx, tx, localDomainAdmission(request),
		eventID, handlingIDs, now)
	if err != nil {
		return AdmissionResult{}, err
	}
	receipt, err := agency.NewAcceptedReceipt(request, event, now)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("admit Intent: construct Receipt: %w", err)
	}
	if err := insertOperationTx(ctx, tx, principal, receipt, event.ID()); err != nil {
		return AdmissionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdmissionResult{}, fmt.Errorf("admit Intent: commit: %w", err)
	}
	return resultFromReceipt(receipt, false), nil
}

func requireBoundAttachment(authenticated authenticatedAttachment, request agency.BoundIntent) error {
	bound := request.Attachment()
	stored := authenticated.value
	if bound.ID() != stored.ID() || bound.Principal() != stored.Principal() ||
		bound.MayInitiate() != stored.MayInitiate() || !bound.IssuedAt().Equal(stored.IssuedAt()) ||
		!bound.ExpiresAt().Equal(stored.ExpiresAt()) {
		return ErrAttachmentAuth
	}
	return nil
}

func operationReplayTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	request agency.BoundIntent,
) (AdmissionResult, bool, error) {
	var requestDigestValue, outcomeValue, receiptDigestValue string
	var eventValue sql.NullString
	var recordedAtValue string
	var receiptJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT request_digest, outcome, event_id, receipt_digest, receipt_json, recorded_at
		FROM operations WHERE actor_principal_id = ? AND operation_key = ?`,
		principal.String(), request.OperationKey().String()).
		Scan(&requestDigestValue, &outcomeValue, &eventValue, &receiptDigestValue, &receiptJSON, &recordedAtValue)
	if errors.Is(err, sql.ErrNoRows) {
		return AdmissionResult{}, false, nil
	}
	if err != nil {
		return AdmissionResult{}, false, fmt.Errorf("admit Intent: inspect operation: %w", err)
	}
	if requestDigestValue != request.RequestDigest().String() {
		return AdmissionResult{}, false, ErrOperationConflict
	}
	receiptDigest, err := agency.ParseDigest(receiptDigestValue)
	parsedReceipt, parseErr := agency.ParseReceiptCanonicalJSON(receiptJSON)
	if err != nil || parseErr != nil || parsedReceipt.Digest() != receiptDigest ||
		parsedReceipt.OperationKey() != request.OperationKey() ||
		parsedReceipt.RequestDigest() != request.RequestDigest() {
		return AdmissionResult{}, false, errors.New("admit Intent: corrupt stored Receipt")
	}
	outcome := parsedReceipt.Outcome()
	if outcomeValue != outcome.String() {
		return AdmissionResult{}, false, errors.New("admit Intent: corrupt operation outcome")
	}
	if parsedReceipt.RecordedAt().Format(storeTimeLayout) != recordedAtValue {
		return AdmissionResult{}, false, errors.New("admit Intent: corrupt operation timestamp")
	}
	if err := validateReplayEventTx(ctx, tx, parsedReceipt, eventValue); err != nil {
		return AdmissionResult{}, false, err
	}
	return AdmissionResult{receiptJSON: append([]byte(nil), receiptJSON...), receiptDigest: receiptDigest,
		outcome: outcome, replayed: true}, true, nil
}

func validateReplayEventTx(ctx context.Context, tx *sql.Tx, receipt agency.Receipt,
	storedEvent sql.NullString,
) error {
	event, accepted := receipt.Event()
	if !accepted {
		if storedEvent.Valid {
			return errors.New("admit Intent: rejected operation names an Event")
		}
		return nil
	}
	if !storedEvent.Valid || storedEvent.String != event.ID().String() {
		return errors.New("admit Intent: accepted operation Event mismatch")
	}
	storedRef, _, _, _, err := loadStoredEventTx(ctx, tx, storedEvent.String)
	if err != nil {
		return fmt.Errorf("admit Intent: inspect replay Event: %w", err)
	}
	if storedRef != event {
		return errors.New("admit Intent: replay Event digest mismatch")
	}
	return nil
}

func artifactChecksTx(ctx context.Context, tx *sql.Tx,
	digests []agency.Digest,
) ([]artifactCheck, error) {
	checks := make([]artifactCheck, 0, len(digests))
	for _, digest := range digests {
		var byteSize int64
		if err := tx.QueryRowContext(ctx, `SELECT byte_size FROM verified_artifacts WHERE digest = ?`,
			digest.String()).Scan(&byteSize); errors.Is(err, sql.ErrNoRows) {
			return nil, ErrArtifactUnavailable
		} else if err != nil {
			return nil, fmt.Errorf("admit Intent: inspect Artifact catalog: %w", err)
		}
		checks = append(checks, artifactCheck{digest: digest, size: byteSize})
	}
	return checks, nil
}

func newHandlingIDs(count int) ([]agency.HandlingID, error) {
	result := make([]agency.HandlingID, count)
	for index := range result {
		id, err := newHandlingID()
		if err != nil {
			return nil, err
		}
		result[index] = id
	}
	return result, nil
}

func nextOriginSequenceTx(ctx context.Context, tx *sql.Tx) (uint64, error) {
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `UPDATE authority_clock
		SET origin_sequence = origin_sequence + 1 WHERE singleton = 1
		RETURNING origin_sequence`).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("admit Intent: advance origin sequence: %w", err)
	}
	if sequence == 0 {
		return 0, errors.New("admit Intent: invalid origin sequence")
	}
	return sequence, nil
}

func resultFromReceipt(receipt agency.Receipt, replayed bool) AdmissionResult {
	return AdmissionResult{receiptJSON: receipt.CanonicalJSON(), receiptDigest: receipt.Digest(),
		outcome: receipt.Outcome(), replayed: replayed}
}
