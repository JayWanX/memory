package authority

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func mustRejectionCode(value string) agency.SemanticLabel {
	code, err := agency.NewSemanticLabel(value)
	if err != nil {
		panic(err)
	}
	return code
}

var (
	rejectionAttachmentExpired   = mustRejectionCode("authority.attachment_expired")
	rejectionArtifactUnavailable = mustRejectionCode("authority.artifact_unavailable")
	rejectionStaleView           = mustRejectionCode("authority.stale_view")
	rejectionStaleSubject        = mustRejectionCode("authority.stale_subject")
	rejectionStaleReference      = mustRejectionCode("authority.stale_reference")
	rejectionStaleRoute          = mustRejectionCode("authority.stale_route")
	rejectionResourceBound       = mustRejectionCode("authority.resource_bound")
)

func persistRejectionTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	request agency.BoundIntent, code agency.SemanticLabel, diagnostic string, now time.Time,
) (AdmissionResult, error) {
	receipt, err := agency.NewRejectedReceipt(request, code, diagnostic, now)
	if err != nil {
		return AdmissionResult{}, fmt.Errorf("admit Intent: construct rejected Receipt: %w", err)
	}
	if err := insertOperationTx(ctx, tx, principal, receipt, agency.EventID{}); err != nil {
		return AdmissionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdmissionResult{}, fmt.Errorf("admit Intent: commit rejection: %w", err)
	}
	return resultFromReceipt(receipt, false), nil
}

func insertOperationTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	receipt agency.Receipt, eventID agency.EventID,
) error {
	var eventValue any
	if !eventID.IsZero() {
		eventValue = eventID.String()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO operations(
		actor_principal_id, operation_key, request_digest, outcome, event_id,
		receipt_digest, receipt_json, recorded_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		principal.String(), receipt.OperationKey().String(), receipt.RequestDigest().String(),
		receipt.Outcome().String(), eventValue, receipt.Digest().String(), receipt.CanonicalJSON(),
		formatTime(receipt.RecordedAt()))
	if err != nil {
		return fmt.Errorf("admit Intent: persist operation: %w", err)
	}
	return nil
}
