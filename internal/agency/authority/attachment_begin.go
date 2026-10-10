package authority

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	attachmentBeginOperationHead    = "attach:"
	attachmentBeginOperationDomain  = "memory.attachment-begin.operation.v1\x00"
	attachmentBeginIDDomain         = "memory.attachment-begin.id.v1\x00"
	attachmentBeginCredentialDomain = "memory.attachment-begin.credential.v1\x00"
)

type attachmentBeginRequestWire struct {
	BoundaryDigest string `json:"boundary_digest"`
	Mode           string `json:"mode"`
	Principal      string `json:"principal"`
	Schema         string `json:"schema"`
	Version        int    `json:"version"`
}

type attachmentBeginMaterial struct {
	attachment   agency.AttachmentID
	credential   [attachmentCredentialBytes]byte
	operationKey agency.OperationKey
	request      agency.Digest
}

// IssueInteractiveAttachment creates or exactly replays one short-lived,
// initiation-capable Host boundary. A fresh boundary atomically replaces the
// Principal's prior live boundary and releases only its claim occupancy.
func (s *Store) IssueInteractiveAttachment(ctx context.Context,
	principal agency.AgentPrincipalID, boundary agency.Digest,
) (AttachmentProof, error) {
	if ctx == nil || principal.IsZero() || boundary.IsZero() {
		return AttachmentProof{}, errors.New("issue attachment: Principal and Host boundary are required")
	}
	material, err := deriveAttachmentBegin(principal, boundary)
	if err != nil {
		return AttachmentProof{}, err
	}
	defer clear(material.credential[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return AttachmentProof{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttachmentProof{}, fmt.Errorf("issue attachment: begin: %w", err)
	}
	defer tx.Rollback()
	proof, found, err := replayAttachmentBeginTx(ctx, tx, principal, material)
	if err != nil || found {
		return proof, err
	}
	if err := requirePrincipalTx(ctx, tx, principal); err != nil {
		return AttachmentProof{}, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return AttachmentProof{}, err
	}
	if err := replaceLiveAttachmentTx(ctx, tx, now, principal); err != nil {
		return AttachmentProof{}, err
	}
	proof, err = insertAttachmentBeginTx(ctx, tx, now, principal, material)
	if err != nil {
		return AttachmentProof{}, err
	}
	if err := tx.Commit(); err != nil {
		return AttachmentProof{}, fmt.Errorf("issue attachment: commit: %w", err)
	}
	return proof, nil
}

func requirePrincipalTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID) error {
	var exists int
	if err := tx.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM principals WHERE principal_id = ?)", principal.String()).Scan(&exists); err != nil {
		return fmt.Errorf("issue attachment: inspect Principal: %w", err)
	}
	if exists != 1 {
		return ErrPrincipalUnavailable
	}
	return nil
}

func replaceLiveAttachmentTx(ctx context.Context, tx *sql.Tx, now time.Time,
	principal agency.AgentPrincipalID,
) error {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT attachment_id FROM attachments
		WHERE principal_id = ? AND ended_at IS NULL`, principal.String()).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("issue attachment: inspect predecessor: %w", err)
	}
	id, err := agency.NewAttachmentID(value)
	if err != nil {
		return errors.New("issue attachment: corrupt predecessor")
	}
	_, err = endLiveAttachmentTx(ctx, tx, now, id)
	return err
}

func insertAttachmentBeginTx(ctx context.Context, tx *sql.Tx, now time.Time,
	principal agency.AgentPrincipalID, material attachmentBeginMaterial,
) (AttachmentProof, error) {
	expiresAt := now.Add(interactiveAttachmentLifetime)
	credentialDigest := agency.Sum(material.credential[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO attachments(
		attachment_id, principal_id, mode, credential_digest, begin_operation_key,
		begin_request_digest, issued_at, expires_at)
		VALUES(?, ?, 'interactive', ?, ?, ?, ?, ?)`, material.attachment.String(),
		principal.String(), credentialDigest.String(), material.operationKey.String(),
		material.request.String(), formatTime(now), formatTime(expiresAt)); err != nil {
		return AttachmentProof{}, fmt.Errorf("issue attachment: persist: %w", err)
	}
	proof, err := NewAttachmentProof(material.attachment, material.credential[:])
	if err != nil {
		return AttachmentProof{}, err
	}
	proof.expiresAt = expiresAt
	return proof, nil
}

func deriveAttachmentBegin(principal agency.AgentPrincipalID,
	boundary agency.Digest,
) (attachmentBeginMaterial, error) {
	wire := attachmentBeginRequestWire{BoundaryDigest: boundary.String(), Mode: "interactive",
		Principal: principal.String(), Schema: "memory.attachment-begin", Version: 1}
	request, err := json.Marshal(wire)
	if err != nil {
		return attachmentBeginMaterial{}, fmt.Errorf("issue attachment: encode request: %w", err)
	}
	requestDigest := agency.Sum(request)
	operationDigest := sumAttachmentBeginDomain(attachmentBeginOperationDomain, boundary[:])
	operation, err := agency.NewOperationKey(attachmentBeginOperationHead +
		base64.RawURLEncoding.EncodeToString(operationDigest[:]))
	if err != nil {
		return attachmentBeginMaterial{}, fmt.Errorf("issue attachment: operation key: %w", err)
	}
	idDigest := sumAttachmentBeginDomain(attachmentBeginIDDomain, request)
	id, err := agency.NewAttachmentID("attachment:" +
		base64.RawURLEncoding.EncodeToString(idDigest[:randomIdentifierBytes]))
	if err != nil {
		return attachmentBeginMaterial{}, fmt.Errorf("issue attachment: attachment ID: %w", err)
	}
	credential := sumAttachmentBeginDomain(attachmentBeginCredentialDomain, request)
	return attachmentBeginMaterial{attachment: id, credential: credential,
		operationKey: operation, request: requestDigest}, nil
}

func sumAttachmentBeginDomain(domain string, value []byte) agency.Digest {
	material := make([]byte, 0, len(domain)+len(value))
	material = append(material, domain...)
	material = append(material, value...)
	digest := agency.Sum(material)
	clear(material)
	return digest
}

func replayAttachmentBeginTx(ctx context.Context, tx *sql.Tx,
	principal agency.AgentPrincipalID, material attachmentBeginMaterial,
) (AttachmentProof, bool, error) {
	var attachmentValue, principalValue, mode, credentialDigest, requestDigest string
	var issuedValue, expiresValue string
	var endedValue sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT attachment_id, principal_id, mode,
		credential_digest, begin_request_digest, issued_at, expires_at, ended_at
		FROM attachments WHERE begin_operation_key = ?`, material.operationKey.String()).
		Scan(&attachmentValue, &principalValue, &mode, &credentialDigest, &requestDigest,
			&issuedValue, &expiresValue, &endedValue)
	if errors.Is(err, sql.ErrNoRows) {
		return AttachmentProof{}, false, nil
	}
	if err != nil {
		return AttachmentProof{}, false, fmt.Errorf("issue attachment: replay lookup: %w", err)
	}
	if requestDigest != material.request.String() {
		return AttachmentProof{}, false, ErrOperationConflict
	}
	if endedValue.Valid {
		return AttachmentProof{}, false, ErrAttachmentEnded
	}
	issuedAt, issuedErr := parseTime(issuedValue)
	expiresAt, expiresErr := parseTime(expiresValue)
	wantCredentialDigest := agency.Sum(material.credential[:]).String()
	if attachmentValue != material.attachment.String() || principalValue != principal.String() ||
		mode != "interactive" || credentialDigest != wantCredentialDigest || issuedErr != nil ||
		expiresErr != nil || !expiresAt.After(issuedAt) {
		return AttachmentProof{}, false, errors.New("issue attachment: corrupt replay outcome")
	}
	proof, err := NewAttachmentProof(material.attachment, material.credential[:])
	if err != nil {
		return AttachmentProof{}, false, err
	}
	proof.expiresAt = expiresAt
	return proof, true, nil
}
