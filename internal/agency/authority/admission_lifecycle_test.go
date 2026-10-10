package authority

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestLocalHandlingLoopRejectsStaleFenceAndRequiresArtifactForCompleted(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:lifecycle")
	root := rootRequest(t, fixture.current(t), "operation:lifecycle-root", "ship a safe change")
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	claimed := fixture.current(t)
	advance := subjectRequest(t, claimed, "operation:advance", agency.ConsequenceAdvanceHandling,
		"tests are running", nil)
	stale := subjectRequest(t, claimed, "operation:stale", agency.ConsequenceAdvanceHandling,
		"stale duplicate progress", nil)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, advance); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	staleResult, err := fixture.store.Admit(fixture.ctx, fixture.proof, stale)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, staleResult, agency.ReceiptOutcomeRejected)

	reclaimed := fixture.current(t)
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "work.complete"),
		Payload: mustPayload(t, "done"), Consequence: agency.ConsequenceResolveCompleted,
		SubjectHandling: currentSubjectHandle(t, reclaimed)})
	if _, err := reclaimed.Bind(intent, mustOperation(t, "operation:complete-no-artifact"), nil); err == nil {
		t.Fatal("completed BoundIntent without Artifact succeeded")
	}
	digest := fixture.catalog(t, "verified test report")
	complete := subjectRequest(t, reclaimed, "operation:complete", agency.ConsequenceResolveCompleted,
		"verified complete", &digest)
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, complete)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	var state, outcome string
	if err := fixture.store.db.QueryRow(`SELECT state, outcome FROM handlings LIMIT 1`).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "terminal" || outcome != "completed" {
		t.Fatalf("Handling = %s/%s, want terminal/completed", state, outcome)
	}
}

func TestFinalAdmissionRehashesCASBytesAndFailsClosedWithoutVerifier(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:rehash")
	digest := fixture.catalog(t, "content whose bytes must still exist")
	request := referenceRequest(t, fixture.current(t), "operation:rehash",
		agency.ConsequencePublishReference, "playbook.rehash", &digest)
	fixture.verifier.mu.Lock()
	delete(fixture.verifier.content, digest)
	fixture.verifier.mu.Unlock()
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	if got := countRows(t, fixture.store, "active_references"); got != 0 {
		t.Fatalf("corrupt CAS admitted %d Reference heads", got)
	}

	second := newAuthorityFixture(t, "principal:no-verifier")
	secondDigest := second.catalog(t, "catalog alone is insufficient")
	second.store.artifactVerifier = nil
	secondRequest := referenceRequest(t, second.current(t), "operation:no-verifier",
		agency.ConsequencePublishReference, "playbook.no-verifier", &secondDigest)
	result, err = second.store.Admit(second.ctx, second.proof, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)

	third := newAuthorityFixture(t, "principal:typed-nil-verifier")
	thirdDigest := third.catalog(t, "typed nil must not panic")
	third.store.artifactVerifier = ArtifactVerifierFunc(nil)
	thirdRequest := referenceRequest(t, third.current(t), "operation:typed-nil-verifier",
		agency.ConsequencePublishReference, "playbook.typed-nil", &thirdDigest)
	result, err = third.store.Admit(third.ctx, third.proof, thirdRequest)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
}

func TestExpiredFreshAdmissionDoesNotReadArtifactCAS(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:expired-cas")
	digest := fixture.catalog(t, "bounded immutable object")
	request := referenceRequest(t, fixture.current(t), "operation:expired-cas",
		agency.ConsequencePublishReference, "playbook.expired", &digest)
	*fixture.now = fixture.proof.ExpiresAt()
	fixture.verifier.mu.Lock()
	before := fixture.verifier.calls
	fixture.verifier.mu.Unlock()

	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	fixture.verifier.mu.Lock()
	after := fixture.verifier.calls
	fixture.verifier.mu.Unlock()
	if after != before {
		t.Fatalf("expired fresh admission read CAS %d times", after-before)
	}
}

func TestUnselectedReferenceChangeDoesNotStaleSubjectIntent(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:intent-read-set")
	root := rootRequest(t, fixture.current(t), "operation:read-set-root", "continue independently")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	view := fixture.current(t)
	advance := subjectRequest(t, view, "operation:read-set-advance",
		agency.ConsequenceAdvanceHandling, "subject progress", nil)
	referenceDigest := fixture.catalog(t, "an unrelated playbook")
	reference := referenceRequest(t, view, "operation:read-set-reference",
		agency.ConsequencePublishReference, "playbook.unrelated", &referenceDigest)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, reference); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, advance)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
}

func TestAdmissionRequiresExactDurablyIssuedView(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:issued-view")
	view := fixture.current(t)
	request := rootRequest(t, view, "operation:issued-view", "must come from Current")
	if _, err := fixture.store.db.Exec("DELETE FROM current_operations"); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	if got := countRows(t, fixture.store, "events"); got != 0 {
		t.Fatalf("unissued View created %d Events", got)
	}

	second := newAuthorityFixture(t, "principal:corrupt-issued-view")
	secondView := second.current(t)
	secondRequest := rootRequest(t, secondView, "operation:corrupt-issued-view", "reject corruption")
	if _, err := second.store.db.Exec("UPDATE current_operations SET authority_json = ?",
		[]byte(`{"schema_version":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := second.store.Admit(second.ctx, second.proof, secondRequest); err == nil ||
		!strings.Contains(err.Error(), "corrupt issued View") {
		t.Fatalf("corrupt issued View admission = %v", err)
	}
	if got := countRows(t, second.store, "operations"); got != 0 {
		t.Fatalf("corrupt issued View persisted %d operations", got)
	}
}

func TestCurrentViewIsBoundedAndDoesNotExposeAuthorityOrCredential(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:clean-view")
	view := fixture.current(t)
	text := string(view.AgentView().CanonicalJSON())
	for _, forbidden := range []string{fixture.principal.String(), fixture.proof.ID().String(),
		string(fixture.proof.Credential()), "claim_fence", "credential", "operation_key", "view_digest"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Agent View leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"targets":["self"]`) || !strings.Contains(text, "handling.create") {
		t.Fatalf("Agent View omitted initiation affordance: %s", text)
	}
}

func TestAdmissionTransactionRollsBackEventClockAndOperationOnEffectFault(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:rollback")
	request := rootRequest(t, fixture.current(t), "operation:rollback", "atomic work")
	if _, err := fixture.store.db.Exec(`CREATE TEMP TRIGGER fail_handling_insert
		BEFORE INSERT ON handlings BEGIN SELECT RAISE(ABORT, 'injected handling fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, request); err == nil {
		t.Fatal("faulted admission succeeded")
	}
	for _, table := range []string{"events", "operations", "handlings"} {
		if got := countRows(t, fixture.store, table); got != 0 {
			t.Fatalf("%s retained %d rows after rollback", table, got)
		}
	}
	var sequence uint64
	if err := fixture.store.db.QueryRow("SELECT origin_sequence FROM authority_clock WHERE singleton=1").Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if sequence != 0 {
		t.Fatalf("origin sequence = %d after rollback", sequence)
	}
	if _, err := fixture.store.db.Exec("DROP TRIGGER fail_handling_insert"); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(context.Background(), fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
}

func TestCurrentRejectsExpiredAndWrongAttachmentProof(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-auth")
	wrong := fixture.proof
	wrong.credential[1] ^= 0xff
	wrongOperation, err := NewCurrentOperation(mustOperation(t, "operation:current-wrong-proof"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Current(fixture.ctx, wrong, wrongOperation); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("Current(wrong credential) = %v", err)
	}
	*fixture.now = fixture.proof.ExpiresAt()
	expiredOperation, err := NewCurrentOperation(mustOperation(t, "operation:current-expired"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, expiredOperation); !errors.Is(err, ErrAttachmentExpired) {
		t.Fatalf("Current(expired) = %v", err)
	}
}

func TestOpenHandlingBoundRejectsAdditionalSuccessor(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:handling-bound")
	root := rootRequest(t, fixture.current(t), "operation:bound-root", "bounded responsibility")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	var headEvent string
	if err := fixture.store.db.QueryRow("SELECT event_id FROM events LIMIT 1").Scan(&headEvent); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < MaxOpenHandlingsPerPrincipal; index++ {
		if _, err := fixture.store.db.Exec(`INSERT INTO handlings(
			handling_id, target_principal_id, head_event_id, state, created_sequence)
			VALUES(?, ?, ?, 'open', 1)`, fmt.Sprintf("handling:bound-%d", index),
			fixture.principal.String(), headEvent); err != nil {
			t.Fatal(err)
		}
	}
	view := fixture.current(t)
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "work.expand"),
		Payload: mustPayload(t, "one more"), Consequence: agency.ConsequenceAdvanceHandling,
		SubjectHandling: currentSubjectHandle(t, view), Successors: []agency.TargetRef{agency.SelfTarget()}})
	request, err := view.Bind(intent, mustOperation(t, "operation:bound-expand"), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	if got := countRows(t, fixture.store, "handlings"); got != MaxOpenHandlingsPerPrincipal {
		t.Fatalf("Handlings = %d, want %d", got, MaxOpenHandlingsPerPrincipal)
	}
}
