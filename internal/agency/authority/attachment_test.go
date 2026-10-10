package authority

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestInteractiveAttachmentBeginExactlyReplaysAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := testDatabasePath(t)
	now := time.Date(2026, 8, 3, 0, 1, 2, 3, time.UTC)
	store, err := open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	principal := mustPrincipal(t, "principal:attachment-restart")
	if err := store.EnrollPrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	boundary := agency.Sum([]byte("private Host boundary replay"))
	first, err := store.IssueInteractiveAttachment(ctx, principal, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	reopened, err := open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.IssueInteractiveAttachment(ctx, principal, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID() != first.ID() || replayed.ExpiresAt() != first.ExpiresAt() ||
		!bytes.Equal(replayed.Credential(), first.Credential()) {
		t.Fatalf("attachment replay changed proof: first=%q/%s replay=%q/%s",
			first.ID().String(), first.ExpiresAt(), replayed.ID().String(), replayed.ExpiresAt())
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("attachment rows after restart replay = %d, want 1", count)
	}
}

func TestInteractiveAttachmentBeginRejectsSameOperationDifferentPrincipal(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:attachment-begin-owner")
	other := mustPrincipal(t, "principal:attachment-begin-other")
	if err := fixture.store.EnrollPrincipal(fixture.ctx, other); err != nil {
		t.Fatal(err)
	}
	boundary := agency.Sum([]byte("one private Host boundary"))
	_, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, fixture.principal, boundary)
	if err != nil {
		t.Fatal(err)
	}
	var initialCount int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, other, boundary); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("same boundary with different Principal = %v, want ErrOperationConflict", err)
	}
	var count int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != initialCount {
		t.Fatalf("attachment rows after operation conflict = %d, want %d", count, initialCount)
	}
}

func TestInteractiveAttachmentBeginCannotReviveEndedBoundary(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:attachment-ended-boundary")
	boundary := agency.Sum([]byte("ended private Host boundary"))
	proof, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, fixture.principal, boundary)
	if err != nil {
		t.Fatal(err)
	}
	var initialCount int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EndInteractiveAttachment(fixture.ctx, proof); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, fixture.principal, boundary); !errors.Is(err, ErrAttachmentEnded) {
		t.Fatalf("ended boundary attachment replay = %v, want ErrAttachmentEnded", err)
	}
	var count int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != initialCount {
		t.Fatalf("attachment rows after ended replay = %d, want %d", count, initialCount)
	}
}

func TestEnrollmentAndMachineIssuedAttachmentStaySeparate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 1, 2, 3, 4, time.UTC)
	store, err := open(ctx, testDatabasePath(t), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := mustPrincipal(t, "principal:attachment")
	boundary := nextAttachmentBoundary(t)
	if _, err := store.IssueInteractiveAttachment(ctx, principal, boundary); !errors.Is(err, ErrPrincipalUnavailable) {
		t.Fatalf("Issue before enrollment = %v, want ErrPrincipalUnavailable", err)
	}
	if err := store.EnrollPrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollPrincipal(ctx, principal); err != nil {
		t.Fatalf("repeat enrollment: %v", err)
	}
	proof, err := store.IssueInteractiveAttachment(ctx, principal, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if proof.ID().IsZero() || len(proof.Credential()) != attachmentCredentialBytes ||
		proof.ExpiresAt() != now.Add(interactiveAttachmentLifetime) {
		t.Fatalf("invalid issued proof: id=%q bytes=%d expiry=%s", proof.ID().String(),
			len(proof.Credential()), proof.ExpiresAt())
	}
	var mode, credentialDigest string
	if err := store.db.QueryRow(`SELECT mode, credential_digest FROM attachments
		WHERE attachment_id = ?`, proof.ID().String()).Scan(&mode, &credentialDigest); err != nil {
		t.Fatal(err)
	}
	if mode != "interactive" || credentialDigest == string(proof.Credential()) {
		t.Fatalf("stored attachment = mode:%q credential:%q", mode, credentialDigest)
	}
	if credentialDigest != agency.Sum(proof.Credential()).String() {
		t.Fatalf("stored digest = %q, want digest of proof", credentialDigest)
	}
	reconstructed, err := NewAttachmentProof(proof.ID(), proof.Credential())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := authenticateAttachmentTx(ctx, tx, reconstructed); err != nil {
		t.Fatalf("transported proof did not authenticate: %v", err)
	}
	if _, err := NewAttachmentProof(proof.ID(), proof.Credential()[:8]); err == nil {
		t.Fatal("short transported credential constructed a proof")
	}
}

func TestAttachmentCredentialDoesNotAuthenticateByIDAlone(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 2, 3, 4, 5, time.UTC)
	store, err := open(ctx, testDatabasePath(t), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal := mustPrincipal(t, "principal:auth")
	if err := store.EnrollPrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	proof, err := store.IssueInteractiveAttachment(ctx, principal, nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
	wrong := proof
	wrong.credential[0] ^= 0xff
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := authenticateAttachmentTx(ctx, tx, wrong); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("wrong credential = %v, want ErrAttachmentAuth", err)
	}
	authenticated, err := authenticateAttachmentTx(ctx, tx, proof)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.value.Principal() != principal || !authenticated.value.MayInitiate() {
		t.Fatalf("authenticated attachment = %#v", authenticated.value)
	}
}

func TestProvisionInitialPrincipalCreatesExactReplayAndRejectsConflict(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal := mustPrincipal(t, "principal:initial")
	if replayed, err := store.ProvisionInitialPrincipal(ctx, principal); err != nil || replayed {
		t.Fatalf("initial provision = (replayed=%t, %v)", replayed, err)
	}
	if replayed, err := store.ProvisionInitialPrincipal(ctx, principal); err != nil || !replayed {
		t.Fatalf("exact replay = (replayed=%t, %v)", replayed, err)
	}
	other := mustPrincipal(t, "principal:other")
	if replayed, err := store.ProvisionInitialPrincipal(ctx, other); replayed ||
		!errors.Is(err, ErrPrincipalConflict) {
		t.Fatalf("conflicting provision = (replayed=%t, %v)", replayed, err)
	}
	if err := store.RequirePrincipal(ctx, principal); err != nil {
		t.Fatalf("initial Principal disappeared: %v", err)
	}
	if err := store.RequirePrincipal(ctx, other); !errors.Is(err, ErrPrincipalUnavailable) {
		t.Fatalf("conflicting Principal was inserted: %v", err)
	}
}

func TestProvisionInitialPrincipalRejectsAnyNonSingletonAuthority(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	initial := mustPrincipal(t, "principal:initial")
	other := mustPrincipal(t, "principal:other")
	if err := store.EnrollPrincipal(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollPrincipal(ctx, other); err != nil {
		t.Fatal(err)
	}
	if replayed, err := store.ProvisionInitialPrincipal(ctx, initial); replayed ||
		!errors.Is(err, ErrPrincipalConflict) {
		t.Fatalf("non-singleton replay = (replayed=%t, %v)", replayed, err)
	}
}

func TestRequireProvisionedPrincipalShapeRecomputesRouteSurrogate(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	local := mustPrincipal(t, "principal:route-shape")
	if _, err := store.ProvisionInitialPrincipal(ctx, local); err != nil {
		t.Fatal(err)
	}
	spec := peerRouteSpec(t, local, "route-shape")
	route, err := store.EnrollPeerRoute(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	misbound := mustPrincipal(t, "principal:misbound-surrogate")
	if err := store.EnrollPrincipal(ctx, misbound); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE peer_routes
		SET surrogate_source_principal_id = ? WHERE route_id = ?`,
		misbound.String(), route.RouteID().String()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM principals WHERE principal_id = ?`,
		route.SurrogateSourcePrincipal().String()); err != nil {
		t.Fatal(err)
	}
	if err := store.RequireProvisionedPrincipalShape(ctx, local); !errors.Is(err, ErrPrincipalConflict) {
		t.Fatalf("misbound route surrogate shape error = %v", err)
	}
}

func TestAttachmentSchemaDoesNotReserveManagedWakeMode(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:interactive-only")
	credential := agency.Sum([]byte("managed mode must remain outside R7 T0"))
	_, err := fixture.store.db.Exec(`INSERT INTO attachments(
		attachment_id, principal_id, mode, credential_digest, issued_at, expires_at)
		VALUES('attachment:managed-forbidden', ?, 'managed', ?, ?, ?)`,
		fixture.principal.String(), credential.String(), formatTime(*fixture.now),
		formatTime(fixture.now.Add(time.Minute)))
	if err == nil {
		t.Fatal("R7 T0 schema accepted a managed-wake attachment")
	}
}

func TestAttachmentSchemaAllowsOneUnendedInteractiveBoundaryPerPrincipal(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:one-live-boundary")
	material, err := deriveAttachmentBegin(fixture.principal, nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(material.credential[:])
	credential := agency.Sum(material.credential[:])
	_, err = fixture.store.db.Exec(`INSERT INTO attachments(
		attachment_id, principal_id, mode, credential_digest, begin_operation_key,
		begin_request_digest, issued_at, expires_at)
		VALUES(?, ?, 'interactive', ?, ?, ?, ?, ?)`, material.attachment.String(),
		fixture.principal.String(), credential.String(), material.operationKey.String(),
		material.request.String(), formatTime(*fixture.now), formatTime(fixture.now.Add(time.Minute)))
	if err == nil {
		t.Fatal("schema accepted two unended interactive boundaries for one Principal")
	}
}

func mustPrincipal(t *testing.T, value string) agency.AgentPrincipalID {
	t.Helper()
	principal, err := agency.NewAgentPrincipalID(value)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}
