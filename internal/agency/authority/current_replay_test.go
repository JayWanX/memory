package authority

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestCurrentOperationReplaysFrozenViewAfterRestartAndExpiry(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-replay")
	root := rootRequest(t, fixture.current(t), "operation:current-replay-root", "durable work")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}

	currentOperation := mustCurrentOperation(t, "operation:current-response-loss")
	first, err := fixture.store.Current(fixture.ctx, fixture.proof, currentOperation)
	if err != nil {
		t.Fatal(err)
	}
	firstPublic := first.public.CanonicalJSON()
	firstAuthority := first.authority.CanonicalJSON()
	advance := subjectRequest(t, first, "operation:current-replay-advance",
		agency.ConsequenceAdvanceHandling, "state changed after the response was lost", nil)
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, advance); err != nil {
		t.Fatal(err)
	}

	*fixture.now = fixture.proof.ExpiresAt().Add(time.Second)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := open(fixture.ctx, fixture.path, func() time.Time { return *fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	reopened.artifactVerifier = fixture.verifier
	fixture.store = reopened

	replayed, err := reopened.Current(fixture.ctx, fixture.proof, currentOperation)
	if err != nil {
		t.Fatalf("Current replay after restart and expiry: %v", err)
	}
	if !bytes.Equal(replayed.public.CanonicalJSON(), firstPublic) ||
		!bytes.Equal(replayed.authority.CanonicalJSON(), firstAuthority) {
		t.Fatal("Current replay did not return the frozen byte-identical View")
	}
	var claimAttachment any
	var fence uint64
	if err := reopened.db.QueryRow(`SELECT claim_attachment_id, claim_fence FROM handlings LIMIT 1`).
		Scan(&claimAttachment, &fence); err != nil {
		t.Fatal(err)
	}
	if claimAttachment != nil || fence != 1 {
		t.Fatalf("Current replay changed claim occupancy: attachment=%v fence=%d", claimAttachment, fence)
	}
	if got := countCurrentOperations(t, reopened, currentOperation); got != 1 {
		t.Fatalf("Current operation rows = %d, want 1", got)
	}
}

func TestCurrentOperationAuthenticatesBeforeReplayAndRejectsDigestConflict(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-auth-replay")
	operation := mustCurrentOperation(t, "operation:current-auth-replay")
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, operation); err != nil {
		t.Fatal(err)
	}

	wrong := fixture.proof
	wrong.credential[0] ^= 0xff
	if _, err := fixture.store.Current(fixture.ctx, wrong, operation); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("wrong credential replay = %v, want ErrAttachmentAuth", err)
	}
	conflict := operation
	conflict.requestDigest = agency.Sum([]byte("different-current-request"))
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("Current digest conflict = %v, want ErrOperationConflict", err)
	}
}

func TestResolveCurrentArtifactRequiresLiveExactViewAuthority(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-artifact-read")
	digest := fixture.catalog(t, "bounded playbook bytes")
	published := referenceRequest(t, fixture.current(t), "operation:publish-readable-reference",
		agency.ConsequencePublishReference, "readable-playbook", &digest)
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, published); err != nil {
		t.Fatal(err)
	}
	operation := mustCurrentOperation(t, "operation:current-artifact-read")
	view, err := fixture.store.Current(fixture.ctx, fixture.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	handle := projectedArtifactHandle(t, view)
	resolved, byteSize, err := fixture.store.ResolveCurrentArtifact(fixture.ctx,
		fixture.proof, operation, handle)
	if err != nil || resolved != digest || byteSize != int64(len("bounded playbook bytes")) {
		t.Fatalf("ResolveCurrentArtifact() = (%s, %d, %v)", resolved, byteSize, err)
	}
	if _, _, err := fixture.store.ResolveCurrentArtifact(fixture.ctx, fixture.proof, operation,
		mustHandle(t, "artifact:not-offered")); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("unoffered Artifact read = %v, want ErrInvariant", err)
	}
	wrong := fixture.proof
	wrong.credential[0] ^= 0xff
	if _, _, err := fixture.store.ResolveCurrentArtifact(fixture.ctx, wrong, operation,
		handle); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("wrong credential Artifact read = %v, want ErrAttachmentAuth", err)
	}
	*fixture.now = fixture.proof.ExpiresAt().Add(time.Second)
	if _, _, err := fixture.store.ResolveCurrentArtifact(fixture.ctx, fixture.proof, operation,
		handle); !errors.Is(err, ErrAttachmentExpired) {
		t.Fatalf("expired Artifact read = %v, want ErrAttachmentExpired", err)
	}
	if _, err := fixture.store.ReplayCurrent(fixture.ctx, fixture.proof, operation); err != nil {
		t.Fatalf("expired Current response replay changed semantics: %v", err)
	}
}

func projectedArtifactHandle(t *testing.T, view BoundView) agency.OpaqueHandle {
	t.Helper()
	var wire struct {
		References []struct {
			Facts struct {
				Artifact *struct {
					Handle string `json:"handle"`
				} `json:"artifact"`
			} `json:"facts"`
		} `json:"references"`
	}
	if err := json.Unmarshal(view.AgentView().CanonicalJSON(), &wire); err != nil ||
		len(wire.References) != 1 || wire.References[0].Facts.Artifact == nil {
		t.Fatalf("Artifact View projection = %v\n%s", err, view.AgentView().CanonicalJSON())
	}
	return mustHandle(t, wire.References[0].Facts.Artifact.Handle)
}

func TestCurrentOperationAndClaimRollbackTogether(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-rollback")
	root := rootRequest(t, fixture.current(t), "operation:current-rollback-root", "claim me")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`CREATE TEMP TRIGGER fail_current_operation
		BEFORE INSERT ON current_operations BEGIN SELECT RAISE(ABORT, 'injected current fault'); END`); err != nil {
		t.Fatal(err)
	}
	operation := mustCurrentOperation(t, "operation:current-rollback-claim")
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, operation); err == nil {
		t.Fatal("faulted Current unexpectedly succeeded")
	}
	var claimAttachment any
	var fence uint64
	if err := fixture.store.db.QueryRow(`SELECT claim_attachment_id, claim_fence FROM handlings LIMIT 1`).
		Scan(&claimAttachment, &fence); err != nil {
		t.Fatal(err)
	}
	if claimAttachment != nil || fence != 0 {
		t.Fatalf("faulted Current retained claim: attachment=%v fence=%d", claimAttachment, fence)
	}
	if got := countCurrentOperations(t, fixture.store, operation); got != 0 {
		t.Fatalf("faulted Current retained %d operation rows", got)
	}
	if _, err := fixture.store.db.Exec("DROP TRIGGER fail_current_operation"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, operation); err != nil {
		t.Fatalf("Current retry after rolled-back fault: %v", err)
	}
}

func TestConcurrentCurrentReplayCreatesOneClaimAndOneOperation(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-concurrent")
	root := rootRequest(t, fixture.current(t), "operation:current-concurrent-root", "claim once")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	operation := mustCurrentOperation(t, "operation:current-concurrent-claim")
	type outcome struct {
		view BoundView
		err  error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			view, err := fixture.store.Current(fixture.ctx, fixture.proof, operation)
			results <- outcome{view: view, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var views [][]byte
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		views = append(views, result.view.public.CanonicalJSON())
	}
	if len(views) != 2 || !bytes.Equal(views[0], views[1]) {
		t.Fatal("concurrent Current retry did not replay one exact View")
	}
	if got := countCurrentOperations(t, fixture.store, operation); got != 1 {
		t.Fatalf("concurrent Current operation rows = %d, want 1", got)
	}
	var fence uint64
	if err := fixture.store.db.QueryRow("SELECT claim_fence FROM handlings LIMIT 1").Scan(&fence); err != nil {
		t.Fatal(err)
	}
	if fence != 1 {
		t.Fatalf("concurrent Current claim fence = %d, want 1", fence)
	}
}

func TestCurrentNeverClaimsAnotherPrincipalsHandling(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:claim-owner")
	root := rootRequest(t, fixture.current(t), "operation:claim-owner-root", "private responsibility")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}

	otherPrincipal := mustPrincipal(t, "principal:claim-outsider")
	if err := fixture.store.EnrollPrincipal(fixture.ctx, otherPrincipal); err != nil {
		t.Fatal(err)
	}
	otherProof, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, otherPrincipal,
		nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := fixture.store.Current(fixture.ctx, otherProof,
		mustCurrentOperation(t, "operation:claim-outsider-current"))
	if err != nil {
		t.Fatal(err)
	}
	if decodePublicView(t, outsider).Current != nil {
		t.Fatal("another Principal received a private Handling")
	}
	var target, state string
	var claimAttachment any
	var fence uint64
	if err := fixture.store.db.QueryRow(`SELECT target_principal_id, state,
		claim_attachment_id, claim_fence FROM handlings LIMIT 1`).
		Scan(&target, &state, &claimAttachment, &fence); err != nil {
		t.Fatal(err)
	}
	if target != fixture.principal.String() || state != "open" || claimAttachment != nil || fence != 0 {
		t.Fatalf("private Handling changed: target=%s state=%s attachment=%v fence=%d",
			target, state, claimAttachment, fence)
	}

	owner, err := fixture.store.Current(fixture.ctx, fixture.proof,
		mustCurrentOperation(t, "operation:claim-owner-current"))
	if err != nil {
		t.Fatal(err)
	}
	if decodePublicView(t, owner).Current == nil {
		t.Fatal("owner Principal could not claim its unchanged Handling")
	}
}

func TestConcurrentDifferentCurrentOperationsCreateOneLiveClaim(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-distinct-race")
	root := rootRequest(t, fixture.current(t), "operation:current-distinct-root", "claim once")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	operations := []CurrentOperation{
		mustCurrentOperation(t, "operation:current-distinct-a"),
		mustCurrentOperation(t, "operation:current-distinct-b"),
	}
	type outcome struct {
		view BoundView
		err  error
	}
	start := make(chan struct{})
	results := make(chan outcome, len(operations))
	var workers sync.WaitGroup
	for _, operation := range operations {
		operation := operation
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			view, err := fixture.store.Current(fixture.ctx, fixture.proof, operation)
			results <- outcome{view: view, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if decodePublicView(t, result.view).Current == nil {
			t.Fatal("distinct Current operation did not project the shared live claim")
		}
	}
	for _, operation := range operations {
		if got := countCurrentOperations(t, fixture.store, operation); got != 1 {
			t.Fatalf("Current operation %s rows = %d, want 1", operation.key.String(), got)
		}
	}
	var claimed int
	var attachment string
	var fence uint64
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*), MIN(claim_attachment_id),
		MAX(claim_fence) FROM handlings WHERE claim_attachment_id IS NOT NULL`).
		Scan(&claimed, &attachment, &fence); err != nil {
		t.Fatal(err)
	}
	if claimed != 1 || attachment != fixture.proof.ID().String() || fence != 1 {
		t.Fatalf("live claims = %d attachment=%s fence=%d", claimed, attachment, fence)
	}
}

func TestCurrentReplayRejectsCorruptStoredProjection(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-corruption")
	operation := mustCurrentOperation(t, "operation:current-corruption")
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, operation); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE current_operations SET agent_view_json = ?
		WHERE attachment_id = ? AND operation_key = ?`, []byte(`{"schema":"corrupt"}`),
		fixture.proof.ID().String(), operation.key.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof, operation); err == nil ||
		!strings.Contains(err.Error(), "corrupt Agent projection") {
		t.Fatalf("corrupt Current replay = %v", err)
	}
	if got := countCurrentOperations(t, fixture.store, operation); got != 1 {
		t.Fatalf("corrupt replay created %d Current rows, want 1", got)
	}
}

func TestCurrentRejectsEventAuthorityColumnDivergence(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:event-column-corruption")
	root := rootRequest(t, fixture.current(t), "operation:event-column-root", "durable work")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec("UPDATE events SET request_digest = ?",
		agency.Sum([]byte("different request")).String()); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.store.Current(fixture.ctx, fixture.proof,
		mustCurrentOperation(t, "operation:event-column-current"))
	if err == nil || !strings.Contains(err.Error(), "authority columns diverge") {
		t.Fatalf("Current with divergent Event columns = %v", err)
	}
}

func TestStoredEventInspectionRejectsUnknownCanonicalField(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:event-strict-parser")
	root := rootRequest(t, fixture.current(t), "operation:event-strict-parser", "durable work")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	var idValue, sourceValue, requestValue, acceptedValue string
	var originSequence uint64
	var causalDepth uint16
	var canonical []byte
	if err := fixture.store.db.QueryRow(`SELECT event_id, origin_sequence, causal_depth,
		source_principal_id, request_digest, accepted_at, canonical_json FROM events LIMIT 1`).
		Scan(&idValue, &originSequence, &causalDepth, &sourceValue, &requestValue,
			&acceptedValue, &canonical); err != nil {
		t.Fatal(err)
	}
	withUnknown := bytes.Replace(canonical, []byte(`"machine":{`),
		[]byte(`"machine":{"unknown":true,`), 1)
	_, _, err := inspectStoredEventDetails(idValue, agency.Sum(withUnknown).String(),
		originSequence, causalDepth, sourceValue, requestValue, acceptedValue, withUnknown)
	if err == nil || !strings.Contains(err.Error(), "invalid Event projection") {
		t.Fatalf("Event projection with unknown machine field = %v", err)
	}
}

func TestCurrentRejectsEventArtifactPinDivergence(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:event-pin-corruption")
	root := rootRequest(t, fixture.current(t), "operation:event-pin-root", "durable work")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	digest := fixture.catalog(t, "unrelated Artifact pin")
	var eventID string
	if err := fixture.store.db.QueryRow("SELECT event_id FROM events LIMIT 1").Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`INSERT INTO event_artifacts(event_id, artifact_digest)
		VALUES(?, ?)`, eventID, digest.String()); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.store.Current(fixture.ctx, fixture.proof,
		mustCurrentOperation(t, "operation:event-pin-current"))
	if err == nil || !strings.Contains(err.Error(), "Artifact pins diverge") {
		t.Fatalf("Current with divergent Event pins = %v", err)
	}
}

func TestCurrentViewHandleBindsAttachmentOperationAndAuthority(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:current-handle")
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(fixture.ctx, tx, fixture.proof)
	if err != nil {
		t.Fatal(err)
	}
	first, err := projectBoundViewTx(fixture.ctx, tx, authenticated.value, nil,
		mustCurrentOperation(t, "operation:current-handle-one").key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := projectBoundViewTx(fixture.ctx, tx, authenticated.value, nil,
		mustCurrentOperation(t, "operation:current-handle-two").key)
	if err != nil {
		t.Fatal(err)
	}
	if first.authority.Digest() != second.authority.Digest() {
		t.Fatal("identical world state unexpectedly changed authority digest")
	}
	if first.public.Handle() == second.public.Handle() {
		t.Fatal("different Current operations reused one public View handle")
	}
}

func mustCurrentOperation(t *testing.T, value string) CurrentOperation {
	t.Helper()
	operation, err := NewCurrentOperation(mustOperation(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func countCurrentOperations(t *testing.T, store *Store, operation CurrentOperation) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM current_operations WHERE operation_key = ?`,
		operation.key.String()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
