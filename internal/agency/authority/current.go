package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	maxProjectedReferences = agency.MaxAgentViewReferences
	currentRequestWire     = `{"schema":"memory.current","version":1}`
)

// CurrentOperation is machine-held replay authority for one Current request.
// Its key and digest never enter the Agent-facing View.
type CurrentOperation struct {
	key           agency.OperationKey
	requestDigest agency.Digest
}

func NewCurrentOperation(key agency.OperationKey) (CurrentOperation, error) {
	if key.IsZero() {
		return CurrentOperation{}, errors.New("current operation: stable key is required")
	}
	return CurrentOperation{key: key, requestDigest: agency.Sum([]byte(currentRequestWire))}, nil
}

func (operation CurrentOperation) valid() bool {
	return !operation.key.IsZero() && !operation.requestDigest.IsZero()
}

// BoundView keeps the machine authority private while exposing only the
// bounded Agent projection and one binding operation. Raw ViewAuthority bytes
// and digest never cross this seam.
type BoundView struct {
	authority agency.ViewAuthority
	public    agency.AgentView
}

func (view BoundView) AgentView() agency.AgentView { return view.public }

// ResolveOfferedArtifact exposes only the digest behind a handle sealed into
// this exact Current operation. It does not accept raw digests or mutate
// authority state.
func (view BoundView) ResolveOfferedArtifact(handle agency.OpaqueHandle) (agency.Digest, error) {
	return view.authority.ResolveOfferedArtifact(handle)
}

func (view BoundView) Bind(intent agency.AgentIntent, operation agency.OperationKey,
	candidates []agency.CapturedCandidate,
) (agency.BoundIntent, error) {
	return bindIntent(view.authority, intent, operation, candidates)
}

type projectedClaim struct {
	handlingID          agency.HandlingID
	head                agency.EventRef
	fence               uint64
	observationRevision uint64
	kind                agency.SemanticLabel
	payload             agency.SemanticPayload
	artifacts           []agency.Digest
}

type projectedReference struct {
	key      agency.ReferenceKey
	head     agency.EventRef
	state    string
	artifact agency.Digest
}

// Current authenticates one eligible boundary, atomically acquires at most
// one least-attended local responsibility, and returns a bounded View. It is a
// durable operation even when no Handling is available. Only a fresh call may
// claim; replay returns its frozen View without renewing or recomputing it.
func (s *Store) Current(ctx context.Context, proof AttachmentProof,
	operation CurrentOperation,
) (BoundView, error) {
	if ctx == nil {
		return BoundView{}, errors.New("current View: nil context")
	}
	if !operation.valid() {
		return BoundView{}, errors.New("current View: valid machine operation is required")
	}
	now, err := s.trustedNow()
	if err != nil {
		return BoundView{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return BoundView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BoundView{}, fmt.Errorf("current View: begin: %w", err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(ctx, tx, proof)
	if err != nil {
		return BoundView{}, err
	}
	if replay, found, err := currentReplayTx(ctx, tx, authenticated.value, operation); err != nil || found {
		return replay, err
	}
	if err := requireLiveAttachment(authenticated, now); err != nil {
		return BoundView{}, err
	}
	if err := settleExpiredClaimsTx(ctx, tx, authenticated.value.Principal(), now,
		MaxClaimExpirySettlementsPerCurrent); err != nil {
		return BoundView{}, err
	}
	claim, err := existingClaimTx(ctx, tx, authenticated.value)
	if errors.Is(err, sql.ErrNoRows) {
		claim, err = claimNextTx(ctx, tx, authenticated.value)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BoundView{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		claim = nil
	}
	view, err := projectBoundViewTx(ctx, tx, authenticated.value, claim, operation.key)
	if err != nil {
		return BoundView{}, err
	}
	if err := insertCurrentOperationTx(ctx, tx, authenticated.value, operation, view); err != nil {
		return BoundView{}, err
	}
	if err := tx.Commit(); err != nil {
		return BoundView{}, fmt.Errorf("current View: commit claim: %w", err)
	}
	return view, nil
}

// ReplayCurrent authenticates the exact attachment and returns only a View
// already frozen by Current. Absence fails closed: this path never claims a
// Handling, settles expiry, inserts an operation, or renews any authority.
func (s *Store) ReplayCurrent(ctx context.Context, proof AttachmentProof,
	operation CurrentOperation,
) (BoundView, error) {
	if ctx == nil {
		return BoundView{}, errors.New("replay current View: nil context")
	}
	if !operation.valid() {
		return BoundView{}, errors.New("replay current View: valid machine operation is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return BoundView{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoundView{}, fmt.Errorf("replay current View: begin: %w", err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(ctx, tx, proof)
	if err != nil {
		return BoundView{}, err
	}
	view, found, err := currentReplayTx(ctx, tx, authenticated.value, operation)
	if err != nil {
		return BoundView{}, err
	}
	if !found {
		return BoundView{}, ErrCurrentUnavailable
	}
	return view, nil
}

// ResolveCurrentArtifact authorizes a new information disclosure against one
// exact frozen Current View. Unlike response replay, Artifact read requires a
// still-live Attachment. Authentication, liveness, Current reconstruction,
// handle resolution, and catalog lookup share one read transaction.
func (s *Store) ResolveCurrentArtifact(ctx context.Context, proof AttachmentProof,
	operation CurrentOperation, handle agency.OpaqueHandle,
) (agency.Digest, int64, error) {
	if ctx == nil || !operation.valid() || handle.IsZero() {
		return agency.Digest{}, 0, errors.New("resolve current Artifact: complete authority is required")
	}
	now, err := s.trustedNow()
	if err != nil {
		return agency.Digest{}, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return agency.Digest{}, 0, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return agency.Digest{}, 0, fmt.Errorf("resolve current Artifact: begin: %w", err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(ctx, tx, proof)
	if err != nil {
		return agency.Digest{}, 0, err
	}
	if err := requireLiveAttachment(authenticated, now); err != nil {
		return agency.Digest{}, 0, err
	}
	view, found, err := currentReplayTx(ctx, tx, authenticated.value, operation)
	if err != nil {
		return agency.Digest{}, 0, err
	}
	if !found {
		return agency.Digest{}, 0, ErrCurrentUnavailable
	}
	digest, err := view.ResolveOfferedArtifact(handle)
	if err != nil {
		return agency.Digest{}, 0, err
	}
	var byteSize int64
	if err := tx.QueryRowContext(ctx,
		"SELECT byte_size FROM verified_artifacts WHERE digest = ?", digest.String()).Scan(&byteSize); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return agency.Digest{}, 0, ErrArtifactUnavailable
		}
		return agency.Digest{}, 0, fmt.Errorf("resolve current Artifact: catalog: %w", err)
	}
	if byteSize < 0 || byteSize > MaxArtifactBytes {
		return agency.Digest{}, 0, errors.New("resolve current Artifact: corrupt catalog size")
	}
	return digest, byteSize, nil
}

func currentReplayTx(ctx context.Context, tx *sql.Tx, attachment agency.Attachment,
	operation CurrentOperation,
) (BoundView, bool, error) {
	var requestValue, handleValue, authorityDigestValue, agentDigestValue string
	var authorityJSON, agentJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT request_digest, view_handle, authority_digest,
		authority_json, agent_view_digest, agent_view_json
		FROM current_operations WHERE attachment_id = ? AND operation_key = ?`,
		attachment.ID().String(), operation.key.String()).
		Scan(&requestValue, &handleValue, &authorityDigestValue, &authorityJSON,
			&agentDigestValue, &agentJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return BoundView{}, false, nil
	}
	if err != nil {
		return BoundView{}, false, fmt.Errorf("current View: inspect operation: %w", err)
	}
	if requestValue != operation.requestDigest.String() {
		return BoundView{}, false, ErrOperationConflict
	}
	view, err := reconstructCurrentView(attachment, operation.key, handleValue,
		authorityDigestValue, authorityJSON, agentDigestValue, agentJSON)
	if err != nil {
		return BoundView{}, false, err
	}
	return view, true, nil
}

func reconstructCurrentView(attachment agency.Attachment, operation agency.OperationKey,
	handleValue, authorityDigestValue string, authorityJSON []byte,
	agentDigestValue string, agentJSON []byte,
) (BoundView, error) {
	authorityDigest, err := agency.ParseDigest(authorityDigestValue)
	if err != nil {
		return BoundView{}, errors.New("current View: corrupt authority digest")
	}
	authorityView, err := agency.ParseViewAuthorityCanonicalJSON(authorityJSON, attachment)
	if err != nil || authorityView.Digest() != authorityDigest {
		return BoundView{}, errors.New("current View: corrupt stored authority")
	}
	expectedHandle, err := currentViewHandle(attachment, operation, authorityDigest)
	if err != nil || handleValue != expectedHandle.String() {
		return BoundView{}, errors.New("current View: corrupt stored handle")
	}
	agentDigest, err := agency.ParseDigest(agentDigestValue)
	if err != nil || agency.Sum(agentJSON) != agentDigest {
		return BoundView{}, errors.New("current View: corrupt Agent projection digest")
	}
	publicView, err := agency.ParseAgentViewCanonicalJSON(agentJSON, authorityView)
	if err != nil || publicView.Handle() != expectedHandle {
		return BoundView{}, errors.New("current View: corrupt Agent projection")
	}
	return BoundView{authority: authorityView, public: publicView}, nil
}

func insertCurrentOperationTx(ctx context.Context, tx *sql.Tx, attachment agency.Attachment,
	operation CurrentOperation, view BoundView,
) error {
	agentJSON := view.public.CanonicalJSON()
	_, err := tx.ExecContext(ctx, `INSERT INTO current_operations(
		attachment_id, operation_key, request_digest, view_handle, authority_digest,
		authority_json, agent_view_digest, agent_view_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		attachment.ID().String(), operation.key.String(), operation.requestDigest.String(),
		view.public.Handle().String(), view.authority.Digest().String(), view.authority.CanonicalJSON(),
		agency.Sum(agentJSON).String(), agentJSON)
	if err != nil {
		return fmt.Errorf("current View: persist operation: %w", err)
	}
	return nil
}

func existingClaimTx(ctx context.Context, tx *sql.Tx,
	attachment agency.Attachment,
) (*projectedClaim, error) {
	row := tx.QueryRowContext(ctx, `SELECT h.handling_id, h.claim_fence, h.head_event_id
		FROM handlings h
		WHERE h.claim_attachment_id = ? AND h.target_principal_id = ? AND h.state = 'open'`,
		attachment.ID().String(), attachment.Principal().String())
	return scanProjectedClaim(ctx, tx, row)
}

// claimNextTx treats the monotonically increasing claim fence as the count of
// prior successful attention opportunities. Creation order is only a stable
// tie-break, so one old Handling cannot monopolize every fresh Host boundary.
// The fence remains authoritative for stale-writer rejection; scheduling never
// mutates or resets it outside the existing successful-claim update below.
func claimNextTx(ctx context.Context, tx *sql.Tx,
	attachment agency.Attachment,
) (*projectedClaim, error) {
	var handlingValue string
	err := tx.QueryRowContext(ctx, `SELECT handling_id FROM handlings
		WHERE target_principal_id = ? AND state = 'open' AND claim_attachment_id IS NULL
		ORDER BY claim_fence, created_sequence, handling_id LIMIT 1`,
		attachment.Principal().String()).Scan(&handlingValue)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE handlings
		SET claim_attachment_id = ?, claim_fence = claim_fence + 1, claim_until = ?
		WHERE handling_id = ? AND state = 'open' AND claim_attachment_id IS NULL`,
		attachment.ID().String(), formatTime(attachment.ExpiresAt()), handlingValue)
	if err != nil {
		return nil, fmt.Errorf("current View: claim next: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("current View: claim cardinality: %w", err)
	}
	if rows != 1 {
		return nil, errors.New("current View: claim cardinality violated")
	}
	row := tx.QueryRowContext(ctx, `SELECT h.handling_id, h.claim_fence, h.head_event_id
		FROM handlings h
		WHERE h.handling_id = ?`, handlingValue)
	return scanProjectedClaim(ctx, tx, row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProjectedClaim(ctx context.Context, tx *sql.Tx, row rowScanner) (*projectedClaim, error) {
	var handlingValue, eventValue string
	var fence uint64
	if err := row.Scan(&handlingValue, &fence, &eventValue); err != nil {
		return nil, err
	}
	handlingID, err := agency.NewHandlingID(handlingValue)
	if err != nil {
		return nil, errors.New("current View: corrupt Handling ID")
	}
	eventRef, kind, payload, artifacts, err := loadStoredEventTx(ctx, tx, eventValue)
	if err != nil {
		return nil, err
	}
	observationRevision, err := terminalObservationRevisionTx(ctx, tx, handlingID)
	if err != nil {
		return nil, err
	}
	return &projectedClaim{handlingID: handlingID, head: eventRef, fence: fence,
		observationRevision: observationRevision,
		kind:                kind, payload: payload, artifacts: artifacts}, nil
}
