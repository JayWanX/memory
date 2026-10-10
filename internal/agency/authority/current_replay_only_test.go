package authority

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestReplayCurrentRequiresPriorIssueAndNeverClaims(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:replay-current-only")
	root := rootRequest(t, fixture.current(t), "operation:replay-only-root", "pending work")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	operation := mustCurrentOperation(t, "operation:replay-only-absent")
	if _, err := fixture.store.ReplayCurrent(fixture.ctx, fixture.proof, operation); !errors.Is(err, ErrCurrentUnavailable) {
		t.Fatalf("ReplayCurrent(absent) = %v, want ErrCurrentUnavailable", err)
	}
	assertNoCurrentClaim(t, fixture.store)
	if got := countCurrentOperations(t, fixture.store, operation); got != 0 {
		t.Fatalf("ReplayCurrent(absent) inserted %d operations", got)
	}

	issued, err := fixture.store.Current(fixture.ctx, fixture.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := fixture.store.ReplayCurrent(fixture.ctx, fixture.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayed.AgentView().CanonicalJSON(), issued.AgentView().CanonicalJSON()) {
		t.Fatal("ReplayCurrent did not return the frozen byte-identical Agent View")
	}
	if got := countCurrentOperations(t, fixture.store, operation); got != 1 {
		t.Fatalf("ReplayCurrent issued operation rows = %d, want 1", got)
	}
	claimUntil := currentClaimUntil(t, fixture.store)
	*fixture.now = fixture.proof.ExpiresAt().Add(time.Second)
	missing := mustCurrentOperation(t, "operation:replay-only-expired-absent")
	if _, err := fixture.store.ReplayCurrent(fixture.ctx, fixture.proof, missing); !errors.Is(err, ErrCurrentUnavailable) {
		t.Fatalf("ReplayCurrent(expired absent) = %v, want ErrCurrentUnavailable", err)
	}
	if got := currentClaimUntil(t, fixture.store); got != claimUntil {
		t.Fatalf("ReplayCurrent settled or renewed claim: got %q, want %q", got, claimUntil)
	}
	if _, err := fixture.store.ReplayCurrent(fixture.ctx, fixture.proof, operation); err != nil {
		t.Fatalf("ReplayCurrent(expired issued) = %v", err)
	}
}

func TestReplayCurrentAuthenticatesAndRejectsDigestOrStoredCorruption(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:replay-current-security")
	operation := mustCurrentOperation(t, "operation:replay-current-security")
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, operation); err != nil {
		t.Fatal(err)
	}
	wrong := fixture.proof
	wrong.credential[0] ^= 0xff
	if _, err := fixture.store.ReplayCurrent(fixture.ctx, wrong, operation); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("ReplayCurrent(wrong proof) = %v, want ErrAttachmentAuth", err)
	}
	conflict := operation
	conflict.requestDigest = agency.Sum([]byte("conflicting-current-request"))
	if _, err := fixture.store.ReplayCurrent(fixture.ctx, fixture.proof, conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("ReplayCurrent(conflict) = %v, want ErrOperationConflict", err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE current_operations SET agent_view_json = ?
		WHERE operation_key = ?`, []byte(`{"corrupt":true}`), operation.key.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReplayCurrent(fixture.ctx, fixture.proof, operation); err == nil {
		t.Fatal("ReplayCurrent accepted corrupt stored projection")
	}
}

func assertNoCurrentClaim(t *testing.T, store *Store) {
	t.Helper()
	var claimed int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM handlings
		WHERE claim_attachment_id IS NOT NULL`).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("ReplayCurrent claimed %d Handlings", claimed)
	}
}

func currentClaimUntil(t *testing.T, store *Store) string {
	t.Helper()
	var value string
	if err := store.db.QueryRow(`SELECT claim_until FROM handlings
		WHERE claim_attachment_id IS NOT NULL`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
