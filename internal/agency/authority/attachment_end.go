package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

// AttachmentEndResult reports the durable lifecycle outcome without exposing
// the ended Attachment's proof or claim authority.
type AttachmentEndResult struct {
	replayed      bool
	releasedClaim bool
}

func (result AttachmentEndResult) Replayed() bool      { return result.replayed }
func (result AttachmentEndResult) ReleasedClaim() bool { return result.releasedClaim }

// EndInteractiveAttachment closes one exact Host boundary. It may release the
// claim occupied by that attachment, but it never advances or terminates the
// Handling and never creates an Event. Exact replay is idempotent.
func (s *Store) EndInteractiveAttachment(ctx context.Context,
	proof AttachmentProof,
) (AttachmentEndResult, error) {
	if ctx == nil {
		return AttachmentEndResult{}, errors.New("end attachment: nil context")
	}
	now, err := s.trustedNow()
	if err != nil {
		return AttachmentEndResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return AttachmentEndResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttachmentEndResult{}, fmt.Errorf("end attachment: begin: %w", err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(ctx, tx, proof)
	if err != nil {
		return AttachmentEndResult{}, err
	}
	if authenticated.endedAt != nil {
		released, err := replayBoundaryEndTx(ctx, tx, authenticated.value.ID())
		if err != nil {
			return AttachmentEndResult{}, err
		}
		return AttachmentEndResult{replayed: true, releasedClaim: released}, nil
	}
	released, err := endLiveAttachmentTx(ctx, tx, now, authenticated.value.ID())
	if err != nil {
		return AttachmentEndResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AttachmentEndResult{}, fmt.Errorf("end attachment: commit: %w", err)
	}
	return AttachmentEndResult{releasedClaim: released}, nil
}

// endLiveAttachmentTx is the one machine lifecycle transition shared by an
// explicit Host end and a fresh-boundary replacement. It releases occupancy
// but cannot create an Event or alter Handling domain state.
func endLiveAttachmentTx(ctx context.Context, tx *sql.Tx, now time.Time,
	attachment agency.AttachmentID,
) (bool, error) {
	claim, found, err := attachmentClaimTx(ctx, tx, attachment)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE attachments SET ended_at = ?
		WHERE attachment_id = ? AND ended_at IS NULL`, formatTime(now), attachment.String())
	if err != nil {
		return false, fmt.Errorf("end attachment: persist end: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return false, errors.New("end attachment: exact lifecycle changed")
	}
	if found {
		if err := settleClaimBoundaryEndTx(ctx, tx, now, claim); err != nil {
			return false, err
		}
	}
	return found, nil
}

func attachmentClaimTx(ctx context.Context, tx *sql.Tx,
	attachment agency.AttachmentID,
) (expiredClaim, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT handling_id, claim_attachment_id,
		claim_fence, claim_until FROM handlings WHERE claim_attachment_id = ?`, attachment.String())
	claim, err := scanExpiredClaim(row)
	if errors.Is(err, sql.ErrNoRows) {
		return expiredClaim{}, false, nil
	}
	if err != nil {
		return expiredClaim{}, false, fmt.Errorf("end attachment: inspect claim: %w", err)
	}
	return claim, true, nil
}

func replayBoundaryEndTx(ctx context.Context, tx *sql.Tx,
	attachment agency.AttachmentID,
) (bool, error) {
	var occupied int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM handlings
		WHERE claim_attachment_id = ?)`, attachment.String()).Scan(&occupied); err != nil {
		return false, fmt.Errorf("end attachment: inspect replay occupancy: %w", err)
	}
	if occupied != 0 {
		return false, errors.New("end attachment: ended boundary still occupies a claim")
	}
	var dispositions int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM claim_dispositions
		WHERE attachment_id = ? AND disposition_kind = 'boundary_end'`, attachment.String()).
		Scan(&dispositions); err != nil {
		return false, fmt.Errorf("end attachment: inspect replay disposition: %w", err)
	}
	if dispositions < 0 || dispositions > 1 {
		return false, errors.New("end attachment: invalid replay disposition cardinality")
	}
	return dispositions == 1, nil
}
