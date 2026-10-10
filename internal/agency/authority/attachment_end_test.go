package authority

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestEndInteractiveAttachmentReleasesClaimWithoutDomainEffect(t *testing.T) {
	fixture, root, currentOperation, claimed := claimedBoundaryFixture(t)
	beforeView := claimed.AgentView().CanonicalJSON()
	freshAfterEnd := subjectRequest(t, claimed, "operation:boundary-fresh-admission",
		agency.ConsequenceAdvanceHandling, "must not advance after end", nil)
	beforeHead := handlingHead(t, fixture)
	beforeEvents := countRows(t, fixture.store, "events")

	ended, err := fixture.store.EndInteractiveAttachment(fixture.ctx, fixture.proof)
	if err != nil || ended.Replayed() || !ended.ReleasedClaim() {
		t.Fatalf("EndInteractiveAttachment = (replayed=%t released=%t, %v)",
			ended.Replayed(), ended.ReleasedClaim(), err)
	}
	assertBoundaryEndPreservedDomain(t, fixture, beforeHead, beforeEvents)
	assertEndedBoundaryReplay(t, fixture, root, currentOperation, beforeView,
		freshAfterEnd, beforeEvents)
	assertImmediateBoundaryReclaim(t, fixture)
}

func TestFreshBoundaryAtomicallyReplacesPredecessorWithoutDomainEffect(t *testing.T) {
	fixture, _, _, _ := claimedBoundaryFixture(t)
	beforeHead := handlingHead(t, fixture)
	beforeEvents := countRows(t, fixture.store, "events")
	boundary := nextAttachmentBoundary(t)

	replacement, err := fixture.store.IssueInteractiveAttachment(fixture.ctx,
		fixture.principal, boundary)
	if err != nil {
		t.Fatal(err)
	}
	assertBoundaryEndPreservedDomain(t, fixture, beforeHead, beforeEvents)
	var live int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM attachments
		WHERE principal_id = ? AND ended_at IS NULL`, fixture.principal.String()).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live attachments = %d, want 1", live)
	}
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof,
		mustCurrentOperation(t, "operation:replaced-boundary-current")); !errors.Is(err, ErrAttachmentEnded) {
		t.Fatalf("replaced boundary Current = %v, want ErrAttachmentEnded", err)
	}
	replayed, err := fixture.store.IssueInteractiveAttachment(fixture.ctx,
		fixture.principal, boundary)
	if err != nil || replayed.ID() != replacement.ID() ||
		!replayed.ExpiresAt().Equal(replacement.ExpiresAt()) ||
		!bytes.Equal(replayed.Credential(), replacement.Credential()) {
		t.Fatalf("replacement replay = (%s, %v)", replayed.ID().String(), err)
	}
	if countRows(t, fixture.store, "events") != beforeEvents ||
		countRows(t, fixture.store, "claim_dispositions") != 1 {
		t.Fatal("replacement replay created a domain effect or second disposition")
	}
}

func claimedBoundaryFixture(t *testing.T) (*authorityFixture, agency.BoundIntent,
	CurrentOperation, BoundView,
) {
	t.Helper()
	fixture := newAuthorityFixture(t, "principal:boundary-end")
	root := rootRequest(t, fixture.current(t), "operation:boundary-root", "continue after a new Host turn")
	accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, root)
	if err != nil || accepted.Outcome() != agency.ReceiptOutcomeAccepted {
		t.Fatalf("root admission = (%v, %v)", accepted.Outcome(), err)
	}
	operation := mustCurrentOperation(t, "operation:boundary-current")
	claimed, err := fixture.store.Current(fixture.ctx, fixture.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, root, operation, claimed
}

func handlingHead(t *testing.T, fixture *authorityFixture) string {
	t.Helper()
	var head string
	if err := fixture.store.db.QueryRow("SELECT head_event_id FROM handlings LIMIT 1").Scan(&head); err != nil {
		t.Fatal(err)
	}
	return head
}

func assertBoundaryEndPreservedDomain(t *testing.T, fixture *authorityFixture,
	beforeHead string, beforeEvents int,
) {
	t.Helper()
	var state, afterHead string
	var outcome, claimAttachment, claimUntil any
	var fence uint64
	if err := fixture.store.db.QueryRow(`SELECT state, outcome, head_event_id,
		claim_attachment_id, claim_fence, claim_until FROM handlings LIMIT 1`).
		Scan(&state, &outcome, &afterHead, &claimAttachment, &fence, &claimUntil); err != nil {
		t.Fatal(err)
	}
	if state != "open" || outcome != nil || beforeHead != afterHead || claimAttachment != nil ||
		claimUntil != nil || fence != 1 || countRows(t, fixture.store, "events") != beforeEvents {
		t.Fatalf("boundary end changed domain state: state=%s outcome=%v head=%s claim=%v fence=%d until=%v",
			state, outcome, afterHead, claimAttachment, fence, claimUntil)
	}
	var kind string
	if err := fixture.store.db.QueryRow("SELECT disposition_kind FROM claim_dispositions").
		Scan(&kind); err != nil || kind != "boundary_end" {
		t.Fatalf("claim disposition = %q, %v", kind, err)
	}
}

func assertEndedBoundaryReplay(t *testing.T, fixture *authorityFixture,
	root agency.BoundIntent, operation CurrentOperation, beforeView []byte,
	fresh agency.BoundIntent, beforeEvents int,
) {
	t.Helper()
	replayedView, err := fixture.store.Current(fixture.ctx, fixture.proof, operation)
	if err != nil || !bytes.Equal(beforeView, replayedView.AgentView().CanonicalJSON()) {
		t.Fatalf("exact Current replay after end = %v", err)
	}
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof,
		mustCurrentOperation(t, "operation:boundary-current-fresh")); !errors.Is(err, ErrAttachmentEnded) {
		t.Fatalf("fresh Current after end = %v, want ErrAttachmentEnded", err)
	}
	replayed, err := fixture.store.Admit(fixture.ctx, fixture.proof, root)
	if err != nil || !replayed.Replayed() || replayed.Outcome() != agency.ReceiptOutcomeAccepted {
		t.Fatalf("exact admission replay after end = (replayed=%t, %v)", replayed.Replayed(), err)
	}
	rejected, err := fixture.store.Admit(fixture.ctx, fixture.proof, fresh)
	if err != nil || rejected.Outcome() != agency.ReceiptOutcomeRejected || rejected.Replayed() {
		t.Fatalf("fresh admission after end = (outcome=%v replayed=%t, %v)",
			rejected.Outcome(), rejected.Replayed(), err)
	}
	if countRows(t, fixture.store, "events") != beforeEvents {
		t.Fatal("fresh admission after end created an Event")
	}
}

func assertImmediateBoundaryReclaim(t *testing.T, fixture *authorityFixture) {
	t.Helper()
	replacement, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, fixture.principal,
		nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := fixture.store.Current(fixture.ctx, replacement,
		mustCurrentOperation(t, "operation:boundary-reclaim"))
	if err != nil || decodePublicView(t, reclaimed).Current == nil {
		t.Fatalf("replacement Current = (%v, %v)", reclaimed.AgentView().Handle(), err)
	}
	var replacementID string
	var fence uint64
	if err := fixture.store.db.QueryRow(`SELECT claim_attachment_id, claim_fence
		FROM handlings LIMIT 1`).Scan(&replacementID, &fence); err != nil ||
		replacementID != replacement.ID().String() || fence != 2 {
		t.Fatalf("replacement claim = %q fence=%d, %v", replacementID, fence, err)
	}
}

func TestEndInteractiveAttachmentWithoutClaimIsIdempotent(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:boundary-end-empty")
	first, err := fixture.store.EndInteractiveAttachment(fixture.ctx, fixture.proof)
	if err != nil || first.Replayed() || first.ReleasedClaim() {
		t.Fatalf("first end = (replayed=%t released=%t, %v)",
			first.Replayed(), first.ReleasedClaim(), err)
	}
	second, err := fixture.store.EndInteractiveAttachment(fixture.ctx, fixture.proof)
	if err != nil || !second.Replayed() || second.ReleasedClaim() {
		t.Fatalf("replayed end = (replayed=%t released=%t, %v)",
			second.Replayed(), second.ReleasedClaim(), err)
	}
	if countRows(t, fixture.store, "claim_dispositions") != 0 {
		t.Fatal("claim-free boundary end created a claim disposition")
	}
	wrong := fixture.proof
	wrong.credential[0] ^= 0xff
	if _, err := fixture.store.EndInteractiveAttachment(fixture.ctx, wrong); !errors.Is(err, ErrAttachmentAuth) {
		t.Fatalf("wrong credential replay = %v, want ErrAttachmentAuth", err)
	}
}

func TestEndInteractiveAttachmentFaultRollsBackEndAndClaim(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:boundary-end-rollback")
	root := rootRequest(t, fixture.current(t), "operation:boundary-rollback-root", "survive end fault")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Current(fixture.ctx, fixture.proof,
		mustCurrentOperation(t, "operation:boundary-rollback-current")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`CREATE TEMP TRIGGER fail_boundary_disposition
		BEFORE INSERT ON claim_dispositions BEGIN SELECT RAISE(ABORT, 'injected boundary fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EndInteractiveAttachment(fixture.ctx, fixture.proof); err == nil {
		t.Fatal("faulted boundary end unexpectedly succeeded")
	}
	var endedAt, claimUntil any
	var claimAttachment string
	if err := fixture.store.db.QueryRow(`SELECT a.ended_at, h.claim_attachment_id, h.claim_until
		FROM attachments a JOIN handlings h ON h.claim_attachment_id = a.attachment_id
		WHERE a.attachment_id = ?`, fixture.proof.ID().String()).
		Scan(&endedAt, &claimAttachment, &claimUntil); err != nil {
		t.Fatal(err)
	}
	if endedAt != nil || claimAttachment != fixture.proof.ID().String() || claimUntil == nil ||
		countRows(t, fixture.store, "claim_dispositions") != 0 {
		t.Fatalf("fault retained partial end: ended=%v claim=%q until=%v", endedAt, claimAttachment, claimUntil)
	}
}

func TestEndInteractiveAttachmentRacesAdmissionWithoutPartialEffect(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:boundary-end-race")
	root := rootRequest(t, fixture.current(t), "operation:boundary-race-root", "race safely")
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, root); err != nil {
		t.Fatal(err)
	}
	claimed := fixture.current(t)
	advance := subjectRequest(t, claimed, "operation:boundary-race-advance",
		agency.ConsequenceAdvanceHandling, "one serialized winner", nil)
	var beforeHead string
	if err := fixture.store.db.QueryRow("SELECT head_event_id FROM handlings LIMIT 1").Scan(&beforeHead); err != nil {
		t.Fatal(err)
	}
	beforeEvents := countRows(t, fixture.store, "events")

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var endResult AttachmentEndResult
	var endErr error
	var admission AdmissionResult
	var admissionErr error
	go func() {
		defer wait.Done()
		<-start
		endResult, endErr = fixture.store.EndInteractiveAttachment(fixture.ctx, fixture.proof)
	}()
	go func() {
		defer wait.Done()
		<-start
		admission, admissionErr = fixture.store.Admit(fixture.ctx, fixture.proof, advance)
	}()
	close(start)
	wait.Wait()
	if endErr != nil || admissionErr != nil || endResult.Replayed() {
		t.Fatalf("raced end/admission = end(%#v, %v) admission(%v, %v)",
			endResult, endErr, admission.Outcome(), admissionErr)
	}
	var state, afterHead string
	var outcome, claimAttachment, claimUntil any
	if err := fixture.store.db.QueryRow(`SELECT state, outcome, head_event_id,
		claim_attachment_id, claim_until FROM handlings LIMIT 1`).
		Scan(&state, &outcome, &afterHead, &claimAttachment, &claimUntil); err != nil {
		t.Fatal(err)
	}
	if state != "open" || outcome != nil || claimAttachment != nil || claimUntil != nil {
		t.Fatalf("raced lifecycle retained partial state: state=%s outcome=%v claim=%v until=%v",
			state, outcome, claimAttachment, claimUntil)
	}
	switch admission.Outcome() {
	case agency.ReceiptOutcomeAccepted:
		if afterHead == beforeHead || countRows(t, fixture.store, "events") != beforeEvents+1 ||
			endResult.ReleasedClaim() {
			t.Fatal("accepted admission and boundary end did not serialize as whole effects")
		}
	case agency.ReceiptOutcomeRejected:
		if afterHead != beforeHead || countRows(t, fixture.store, "events") != beforeEvents ||
			!endResult.ReleasedClaim() {
			t.Fatal("ended boundary and rejected admission did not serialize as whole effects")
		}
	default:
		t.Fatalf("raced admission outcome = %v", admission.Outcome())
	}
}
