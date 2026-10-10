package authority

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestClaimExpiryReplaySurvivesRestartAndRejectsDigestConflict(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:expiry-replay")
	claim, key, requestDigest := settleOneExpiredClaim(t, fixture)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := open(fixture.ctx, fixture.path, func() time.Time { return *fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = reopened
	tx, err := reopened.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	found, err := replayClaimExpiryTx(fixture.ctx, tx, claim, key, requestDigest)
	if err != nil || !found {
		t.Fatalf("claim disposition replay after restart = (%v, %v)", found, err)
	}
	conflict := agency.Sum([]byte("different claim expiry request"))
	if _, err := replayClaimExpiryTx(fixture.ctx, tx, claim, key, conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("claim disposition digest conflict = %v, want ErrOperationConflict", err)
	}
}

func TestClaimExpiryReplayFailsClosedOnDurableDivergence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *authorityFixture, expiredClaim, agency.OperationKey, agency.Digest)
	}{
		{name: "request", mutate: func(t *testing.T, fixture *authorityFixture, _ expiredClaim,
			_ agency.OperationKey, _ agency.Digest,
		) {
			if _, err := fixture.store.db.Exec("UPDATE claim_dispositions SET request_digest = ?",
				agency.Sum([]byte("divergent request")).String()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "outcome", mutate: func(t *testing.T, fixture *authorityFixture, _ expiredClaim,
			_ agency.OperationKey, _ agency.Digest,
		) {
			value := []byte(`{}`)
			if _, err := fixture.store.db.Exec(`UPDATE claim_dispositions
				SET outcome_json = ?, outcome_digest = ?`, value, agency.Sum(value).String()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "recorded-time", mutate: func(t *testing.T, fixture *authorityFixture,
			claim expiredClaim, key agency.OperationKey, requestDigest agency.Digest,
		) {
			recordedAt := claim.claimUntil.Add(-time.Nanosecond)
			value, err := json.Marshal(claimExpiryOutcomeWire{Schema: "memory.claim-expiry-outcome",
				OperationKey: key.String(), RequestDigest: requestDigest.String(),
				Outcome: "claim_released", RecordedAt: formatTime(recordedAt)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec(`UPDATE claim_dispositions SET recorded_at = ?,
				outcome_json = ?, outcome_digest = ?`, formatTime(recordedAt), value,
				agency.Sum(value).String()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "tuple", mutate: func(t *testing.T, fixture *authorityFixture, _ expiredClaim,
			_ agency.OperationKey, _ agency.Digest,
		) {
			proof, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, fixture.principal,
				nextAttachmentBoundary(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.db.Exec("UPDATE claim_dispositions SET attachment_id = ?",
				proof.ID().String()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorityFixture(t, "principal:expiry-corrupt-"+test.name)
			claim, key, requestDigest := settleOneExpiredClaim(t, fixture)
			test.mutate(t, fixture, claim, key, requestDigest)
			tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if found, err := replayClaimExpiryTx(fixture.ctx, tx, claim, key, requestDigest); err == nil || found {
				t.Fatalf("divergent claim disposition replay = (%v, %v)", found, err)
			}
		})
	}
}

func settleOneExpiredClaim(t *testing.T,
	fixture *authorityFixture,
) (expiredClaim, agency.OperationKey, agency.Digest) {
	t.Helper()
	root := rootRequest(t, fixture.current(t), "operation:expiry-helper-root", "expire exactly")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	fixture.current(t)
	*fixture.now = fixture.proof.ExpiresAt().Add(time.Second)
	claim := loadOnlyExpiredClaim(t, fixture)
	key, digest, err := claimExpiryIdentity(claim)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := settleClaimExpiryTx(fixture.ctx, tx, *fixture.now, claim); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := settleClaimExpiryTx(fixture.ctx, tx, *fixture.now, claim); err != nil {
		_ = tx.Rollback()
		t.Fatalf("same claim disposition replay: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, fixture.store, "claim_dispositions"); got != 1 {
		t.Fatalf("claim dispositions = %d, want 1", got)
	}
	return claim, key, digest
}

func loadOnlyExpiredClaim(t *testing.T, fixture *authorityFixture) expiredClaim {
	t.Helper()
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	claims, err := loadExpiredClaimsTx(fixture.ctx, tx, fixture.principal, *fixture.now, 2)
	if err != nil || len(claims) != 1 {
		t.Fatalf("load one expired claim = (%d, %v)", len(claims), err)
	}
	return claims[0]
}
