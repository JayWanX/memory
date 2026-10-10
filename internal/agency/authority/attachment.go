package authority

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	interactiveAttachmentLifetime = 15 * time.Minute
	attachmentCredentialBytes     = 32
	randomIdentifierBytes         = 16
)

// AttachmentProof is the exact short-lived capability returned by machine
// issuance. Callers may persist it only in an owner-private Runtime surface;
// the authority store retains only its digest.
type AttachmentProof struct {
	id         agency.AttachmentID
	credential [attachmentCredentialBytes]byte
	expiresAt  time.Time
}

// NewAttachmentProof reconstructs a transported capability at the daemon
// boundary. The caller supplies no Principal, mode, timestamps, or authority;
// exact credential verification resolves all of them from the private store.
func NewAttachmentProof(id agency.AttachmentID, credential []byte) (AttachmentProof, error) {
	if id.IsZero() || len(credential) != attachmentCredentialBytes {
		return AttachmentProof{}, errors.New("attachment proof: exact ID and credential are required")
	}
	proof := AttachmentProof{id: id}
	copy(proof.credential[:], credential)
	return proof, nil
}

func (proof AttachmentProof) ID() agency.AttachmentID { return proof.id }
func (proof AttachmentProof) ExpiresAt() time.Time    { return proof.expiresAt }
func (proof AttachmentProof) Credential() []byte {
	return append([]byte(nil), proof.credential[:]...)
}

// EnrollPrincipal is setup authority, not Agent authentication. It creates
// only the stable local Principal that later machine-issued attachments may
// authenticate as.
func (s *Store) EnrollPrincipal(ctx context.Context, principal agency.AgentPrincipalID) error {
	if ctx == nil || principal.IsZero() {
		return errors.New("enroll Principal: Principal is required")
	}
	now, err := s.trustedNow()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO principals(principal_id, created_at)
		VALUES(?, ?) ON CONFLICT(principal_id) DO NOTHING`, principal.String(), formatTime(now)); err != nil {
		return fmt.Errorf("enroll Principal: %w", err)
	}
	return nil
}

// RequirePrincipal proves that setup enrolled the exact stable Principal. It
// is read-only and is used by strict daemon composition after opening an
// existing authority database.
func (s *Store) RequirePrincipal(ctx context.Context, principal agency.AgentPrincipalID) error {
	if ctx == nil || principal.IsZero() {
		return errors.New("require Principal: Principal is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM principals WHERE principal_id = ?)", principal.String()).Scan(&exists); err != nil {
		return fmt.Errorf("require Principal: %w", err)
	}
	if exists != 1 {
		return ErrPrincipalUnavailable
	}
	return nil
}

type authenticatedAttachment struct {
	value   agency.Attachment
	endedAt *time.Time
}

// ProvisionInitialPrincipal is the setup-only initializer for one R7 local
// authority. An empty authority accepts exactly one stable Principal. An exact
// replay may also contain only route-derived surrogate Principals, with every
// route targeting that same local Principal. Orphan or second-local Principals
// fail closed instead of turning setup into a Principal registry.
func (s *Store) ProvisionInitialPrincipal(ctx context.Context,
	principal agency.AgentPrincipalID,
) (replayed bool, err error) {
	if ctx == nil || principal.IsZero() {
		return false, errors.New("provision initial Principal: Principal is required")
	}
	now, err := s.trustedNow()
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("provision initial Principal: begin: %w", err)
	}
	defer tx.Rollback()
	var total, exact int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN principal_id = ? THEN 1 ELSE 0 END), 0)
		FROM principals`, principal.String()).Scan(&total, &exact); err != nil {
		return false, fmt.Errorf("provision initial Principal: inspect: %w", err)
	}
	switch {
	case total == 0 && exact == 0:
		if _, err := tx.ExecContext(ctx, `INSERT INTO principals(principal_id, created_at)
			VALUES(?, ?)`, principal.String(), formatTime(now)); err != nil {
			return false, fmt.Errorf("provision initial Principal: persist: %w", err)
		}
	case exact == 1:
		if err := requireProvisionedPrincipalShapeTx(ctx, tx, principal, total); err != nil {
			return false, err
		}
		replayed = true
	default:
		return false, ErrPrincipalConflict
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("provision initial Principal: commit: %w", err)
	}
	return replayed, nil
}

// RequireProvisionedPrincipalShape is the strict daemon-open verifier for the
// identity-derived local Principal and route-derived surrogate Principals. It
// is read-only and never enrolls or repairs authority.
func (s *Store) RequireProvisionedPrincipalShape(ctx context.Context,
	principal agency.AgentPrincipalID,
) error {
	if ctx == nil || principal.IsZero() {
		return errors.New("require provisioned Principal: Principal is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("require provisioned Principal: begin: %w", err)
	}
	defer tx.Rollback()
	var total, exact int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN principal_id = ? THEN 1 ELSE 0 END), 0)
		FROM principals`, principal.String()).Scan(&total, &exact); err != nil {
		return fmt.Errorf("require provisioned Principal: inspect: %w", err)
	}
	if exact != 1 {
		return ErrPrincipalConflict
	}
	return requireProvisionedPrincipalShapeTx(ctx, tx, principal, total)
}

func requireProvisionedPrincipalShapeTx(ctx context.Context, tx *sql.Tx,
	principal agency.AgentPrincipalID, total int,
) error {
	routes, err := requireProvisionedRoutesTx(ctx, tx, principal)
	if err != nil {
		return err
	}
	if total != routes+1 {
		return ErrPrincipalConflict
	}
	return nil
}

func requireProvisionedRoutesTx(ctx context.Context, tx *sql.Tx,
	principal agency.AgentPrincipalID,
) (int, error) {
	rows, err := tx.QueryContext(ctx, peerRouteColumns+` ORDER BY route_id`)
	if err != nil {
		return 0, fmt.Errorf("require provisioned Principal: inspect peer routes: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		route, err := scanPeerRoute(rows)
		if err != nil {
			return 0, fmt.Errorf("%w: invalid peer route: %v", ErrPrincipalConflict, err)
		}
		if route.LocalTargetPrincipal() != principal ||
			route.SurrogateSourcePrincipal() == principal {
			return 0, ErrPrincipalConflict
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("require provisioned Principal: iterate peer routes: %w", err)
	}
	return count, nil
}

// authenticateAttachmentTx verifies a fixed-size proof in constant comparison
// time. Expiry is deliberately not checked here: replay lookup must precede
// mutable lifecycle checks.
func authenticateAttachmentTx(ctx context.Context, tx *sql.Tx,
	proof AttachmentProof,
) (authenticatedAttachment, error) {
	if proof.id.IsZero() {
		return authenticatedAttachment{}, ErrAttachmentAuth
	}
	var principalValue, mode, storedDigest, issuedValue, expiresValue string
	var endedValue sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT principal_id, mode, credential_digest, issued_at, expires_at, ended_at
		FROM attachments WHERE attachment_id = ?`, proof.id.String()).
		Scan(&principalValue, &mode, &storedDigest, &issuedValue, &expiresValue, &endedValue)
	if errors.Is(err, sql.ErrNoRows) {
		return authenticatedAttachment{}, ErrAttachmentAuth
	}
	if err != nil {
		return authenticatedAttachment{}, fmt.Errorf("authenticate attachment: load: %w", err)
	}
	if mode != "interactive" {
		return authenticatedAttachment{}, errors.New("authenticate attachment: unsupported mode")
	}
	parsedStoredDigest, err := agency.ParseDigest(storedDigest)
	if err != nil {
		return authenticatedAttachment{}, errors.New("authenticate attachment: corrupt credential digest")
	}
	actualDigest := agency.Sum(proof.credential[:])
	if subtle.ConstantTimeCompare(actualDigest[:], parsedStoredDigest[:]) != 1 {
		return authenticatedAttachment{}, ErrAttachmentAuth
	}
	principal, err := agency.NewAgentPrincipalID(principalValue)
	if err != nil {
		return authenticatedAttachment{}, errors.New("authenticate attachment: corrupt Principal")
	}
	issuedAt, err := parseTime(issuedValue)
	if err != nil {
		return authenticatedAttachment{}, err
	}
	expiresAt, err := parseTime(expiresValue)
	if err != nil {
		return authenticatedAttachment{}, err
	}
	attachment, err := agency.NewAttachment(proof.id, principal, true, issuedAt, expiresAt)
	if err != nil {
		return authenticatedAttachment{}, fmt.Errorf("authenticate attachment: corrupt authority: %w", err)
	}
	var endedAt *time.Time
	if endedValue.Valid {
		parsed, err := parseTime(endedValue.String)
		if err != nil || parsed.Before(issuedAt) {
			return authenticatedAttachment{}, errors.New("authenticate attachment: corrupt end time")
		}
		endedAt = &parsed
	}
	return authenticatedAttachment{value: attachment, endedAt: endedAt}, nil
}

func requireLiveAttachment(attachment authenticatedAttachment, now time.Time) error {
	if attachment.endedAt != nil {
		return ErrAttachmentEnded
	}
	if !now.Before(attachment.value.ExpiresAt()) {
		return ErrAttachmentExpired
	}
	return nil
}

func newEventID() (agency.EventID, error) {
	token, err := randomIdentifier("event")
	if err != nil {
		return agency.EventID{}, err
	}
	return agency.NewEventID(token)
}

func newHandlingID() (agency.HandlingID, error) {
	token, err := randomIdentifier("handling")
	if err != nil {
		return agency.HandlingID{}, err
	}
	return agency.NewHandlingID(token)
}

func randomIdentifier(prefix string) (string, error) {
	var entropy [randomIdentifierBytes]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("authority: random %s ID: %w", prefix, err)
	}
	return prefix + ":" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}
