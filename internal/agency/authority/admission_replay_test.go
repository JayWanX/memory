package authority

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestAdmissionReplaysExactReceiptBeforeExpiredMutableAuthority(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:replay")
	request := rootRequest(t, fixture.current(t), "operation:root-replay", "implement feature")
	accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, accepted, agency.ReceiptOutcomeAccepted)
	firstBytes := accepted.ReceiptJSON()

	*fixture.now = fixture.proof.ExpiresAt().Add(time.Second)
	replayed, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatalf("replay after expiry: %v", err)
	}
	if !replayed.Replayed() || replayed.ReceiptDigest() != accepted.ReceiptDigest() ||
		!bytes.Equal(replayed.ReceiptJSON(), firstBytes) {
		t.Fatalf("replay changed Receipt: replayed=%v digest=%s bytesEqual=%v",
			replayed.Replayed(), replayed.ReceiptDigest().String(),
			bytes.Equal(replayed.ReceiptJSON(), firstBytes))
	}

	wrongProof := fixture.proof
	wrongProof.credential[0] ^= 0xff
	if _, err := fixture.store.Admit(fixture.ctx, wrongProof, request); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("wrong credential replay = %v, want ErrAttachmentAuth", err)
	}
}

func TestAcceptedOperationReplayPrecedesStaleSubjectAndReferenceAuthority(t *testing.T) {
	t.Run("subject", func(t *testing.T) {
		fixture := newAuthorityFixture(t, "principal:subject-stale-replay")
		root := rootRequest(t, fixture.current(t), "operation:subject-stale-root", "advance once")
		if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
			t.Fatal(err)
		}
		request := subjectRequest(t, fixture.current(t), "operation:subject-stale-advance",
			agency.ConsequenceAdvanceHandling, "advance the exact head", nil)
		accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
		if err != nil {
			t.Fatalf("accepted subject replay after its fence/head became stale: %v", err)
		}
		if !replayed.Replayed() || replayed.ReceiptDigest() != accepted.ReceiptDigest() ||
			!bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()) {
			t.Fatal("accepted subject replay did not return the exact original Receipt")
		}
		if got := countRows(t, fixture.store, "events"); got != 2 {
			t.Fatalf("subject replay created %d Events, want 2", got)
		}
	})

	t.Run("reference", func(t *testing.T) {
		fixture := newAuthorityFixture(t, "principal:reference-stale-replay")
		digest := fixture.catalog(t, "replayable playbook")
		request := referenceRequest(t, fixture.current(t), "operation:reference-stale-publish",
			agency.ConsequencePublishReference, "playbook.replay", &digest)
		accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
		if err != nil {
			t.Fatalf("accepted first-publish replay after expected-absent became stale: %v", err)
		}
		if !replayed.Replayed() || replayed.ReceiptDigest() != accepted.ReceiptDigest() ||
			!bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()) {
			t.Fatal("accepted Reference replay did not return the exact original Receipt")
		}
		if countRows(t, fixture.store, "events") != 1 ||
			countRows(t, fixture.store, "reference_lineage") != 1 {
			t.Fatal("Reference replay mutated accepted lineage")
		}
	})
}

func TestAdmissionReplayUsesStablePrincipalAcrossAttachments(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:attachment-replay")
	request := rootRequest(t, fixture.current(t), "operation:attachment-replay", "survive attachment change")
	accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}

	*fixture.now = fixture.proof.ExpiresAt().Add(time.Second)
	replacement, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, fixture.principal,
		nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := fixture.store.Admit(fixture.ctx, replacement, request)
	if err != nil {
		t.Fatalf("replay through replacement attachment: %v", err)
	}
	if !replayed.Replayed() || replayed.ReceiptDigest() != accepted.ReceiptDigest() ||
		!bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()) {
		t.Fatalf("replacement attachment changed replay: replayed=%v digest=%s bytesEqual=%v",
			replayed.Replayed(), replayed.ReceiptDigest().String(),
			bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()))
	}
}

func TestAdmissionRejectsSameOperationKeyWithDifferentRequestDigest(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:operation-conflict")
	view := fixture.current(t)
	first := rootRequest(t, view, "operation:shared", "first meaning")
	second := rootRequest(t, view, "operation:shared", "second meaning")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, first); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, second); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("different digest = %v, want ErrOperationConflict", err)
	}
	if got := countRows(t, fixture.store, "operations"); got != 1 {
		t.Fatalf("operations = %d, want 1", got)
	}
}

func TestAdmissionReplayRejectsDifferentPrincipal(t *testing.T) {
	first := newAuthorityFixture(t, "principal:first")
	request := rootRequest(t, first.current(t), "operation:wrong-principal", "private work")
	if _, err := first.store.Admit(first.ctx, first.proof, request); err != nil {
		t.Fatal(err)
	}
	secondPrincipal := mustPrincipal(t, "principal:second")
	if err := first.store.EnrollPrincipal(first.ctx, secondPrincipal); err != nil {
		t.Fatal(err)
	}
	secondProof, err := first.store.IssueInteractiveAttachment(first.ctx, secondPrincipal,
		nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.store.Admit(first.ctx, secondProof, request); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("wrong Principal = %v, want ErrAttachmentAuth", err)
	}
	if got := countRows(t, first.store, "operations"); got != 1 {
		t.Fatalf("cross-Principal replay changed operation count to %d", got)
	}
}

func TestRejectedReceiptIsDurableAndReplaysAfterArtifactAppears(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:rejected-replay")
	view := fixture.current(t)
	content := []byte("not cataloged yet")
	digest := agency.Sum(content)
	request := referenceRequest(t, view, "operation:missing-artifact",
		agency.ConsequencePublishReference, "playbook.review", &digest)
	rejected, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, rejected, agency.ReceiptOutcomeRejected)

	artifact, err := VerifyArtifact(content, *fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.CatalogArtifact(fixture.ctx, artifact); err != nil {
		t.Fatal(err)
	}
	fixture.verifier.mu.Lock()
	fixture.verifier.content[digest] = content
	fixture.verifier.mu.Unlock()
	replayed, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed() || replayed.Outcome() != agency.ReceiptOutcomeRejected ||
		!bytes.Equal(replayed.ReceiptJSON(), rejected.ReceiptJSON()) {
		t.Fatalf("rejected replay changed: replay=%v outcome=%s", replayed.Replayed(),
			replayed.Outcome().String())
	}
}

func TestRestartPreservesOpenResponsibilityAndOperationReplay(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:restart")
	request := rootRequest(t, fixture.current(t), "operation:restart-root", "survive restart")
	accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := open(context.Background(), fixture.path, func() time.Time { return *fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	reopened.artifactVerifier = fixture.verifier
	fixture.store = reopened

	replayed, err := reopened.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed() || !bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()) {
		t.Fatal("restart did not preserve exact operation replay")
	}
	current, err := reopened.Current(fixture.ctx, fixture.proof,
		mustCurrentOperation(t, "operation:restart-current"))
	if err != nil {
		t.Fatal(err)
	}
	if decodePublicView(t, current).Current == nil {
		t.Fatal("restart lost open responsibility")
	}
}
