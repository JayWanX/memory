package authority

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestReferenceCitationRecordsExactHeadWithoutMutatingLineage(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-citation")
	first := fixture.catalog(t, "citation guide v1")
	publish := referenceRequest(t, fixture.current(t), "operation:citation-publish",
		agency.ConsequencePublishReference, "guide.citation", &first)
	publishResult, err := fixture.store.Admit(fixture.ctx, fixture.proof, publish)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, publishResult, agency.ReceiptOutcomeAccepted)
	publishReceipt, err := agency.ParseReceiptCanonicalJSON(publishResult.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	citedHead, ok := publishReceipt.Event()
	if !ok {
		t.Fatal("Reference publish did not create an Event")
	}
	view := fixture.current(t)
	head := referenceHeadHandle(t, view, "guide.citation")
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "work.guided"),
		Payload: mustPayload(t, "use the cited guide"), Consequence: agency.ConsequenceCreateHandlings,
		Successors:       []agency.TargetRef{agency.SelfTarget()},
		CausationHandles: []agency.OpaqueHandle{head}})
	request, err := view.Bind(intent, mustOperation(t, "operation:citation-use"), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	event, ok := receipt.Event()
	if !ok {
		t.Fatal("citation admission did not create an Event")
	}
	var canonical []byte
	if err := fixture.store.db.QueryRow(`SELECT canonical_json FROM events WHERE event_id = ?`,
		event.ID().String()).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Evidence struct {
			Causation []struct {
				ID     string `json:"id"`
				Digest string `json:"digest"`
			} `json:"causation"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(canonical, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Evidence.Causation) != 1 ||
		wire.Evidence.Causation[0].ID != citedHead.ID().String() ||
		wire.Evidence.Causation[0].Digest != citedHead.Digest().String() {
		t.Fatalf("persisted Reference citation = %#v, want %v", wire.Evidence.Causation, citedHead)
	}
	if countRows(t, fixture.store, "reference_lineage") != 1 ||
		countRows(t, fixture.store, "active_references") != 1 {
		t.Fatal("citation mutated Reference lineage")
	}
}

func TestReferenceCitationRejectsAHeadSupersededAfterView(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:stale-reference-citation")
	first := fixture.catalog(t, "citation guide v1")
	publish := referenceRequest(t, fixture.current(t), "operation:stale-citation-publish",
		agency.ConsequencePublishReference, "guide.stale-citation", &first)
	if _, err := fixture.store.Admit(fixture.ctx, fixture.proof, publish); err != nil {
		t.Fatal(err)
	}
	view := fixture.current(t)
	head := referenceHeadHandle(t, view, "guide.stale-citation")
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "work.guided"),
		Payload: mustPayload(t, "use a now-stale guide"), Consequence: agency.ConsequenceCreateHandlings,
		Successors:        []agency.TargetRef{agency.SelfTarget()},
		CorrelationHandle: head})
	stale, err := view.Bind(intent, mustOperation(t, "operation:stale-citation-use"), nil)
	if err != nil {
		t.Fatal(err)
	}
	second := fixture.catalog(t, "citation guide v2")
	supersede := referenceRequest(t, view, "operation:stale-citation-supersede",
		agency.ConsequenceSupersedeReference, "guide.stale-citation", &second)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, supersede); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, stale)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Code() != rejectionStaleReference {
		t.Fatalf("stale citation code = %s, want %s", receipt.Code().String(), rejectionStaleReference.String())
	}
}

func TestConcurrentReferenceCASAcceptsExactlyOneCandidate(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-cas")
	view := fixture.current(t)
	firstDigest := fixture.catalog(t, "review playbook v1-a")
	secondDigest := fixture.catalog(t, "review playbook v1-b")
	first := referenceRequest(t, view, "operation:publish-a", agency.ConsequencePublishReference,
		"playbook.review", &firstDigest)
	second := referenceRequest(t, view, "operation:publish-b", agency.ConsequencePublishReference,
		"playbook.review", &secondDigest)

	results := make(chan AdmissionResult, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for _, request := range []agency.BoundIntent{first, second} {
		wait.Add(1)
		go func(candidate agency.BoundIntent) {
			defer wait.Done()
			result, err := fixture.store.Admit(fixture.ctx, fixture.proof, candidate)
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}(request)
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	accepted, rejected := 0, 0
	for result := range results {
		switch result.Outcome() {
		case agency.ReceiptOutcomeAccepted:
			accepted++
		case agency.ReceiptOutcomeRejected:
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("Reference race = accepted:%d rejected:%d", accepted, rejected)
	}
	if countRows(t, fixture.store, "active_references") != 1 ||
		countRows(t, fixture.store, "reference_lineage") != 1 {
		t.Fatal("Reference CAS created multiple accepted heads")
	}
}

func TestReferenceExistingHeadCASRejectsStaleMutation(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-stale-cas")
	first := fixture.catalog(t, "playbook exact head v1")
	publish := referenceRequest(t, fixture.current(t), "operation:stale-cas-publish",
		agency.ConsequencePublishReference, "playbook.stale-cas", &first)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, publish); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}

	sharedView := fixture.current(t)
	second := fixture.catalog(t, "playbook exact head v2")
	supersede := referenceRequest(t, sharedView, "operation:stale-cas-supersede",
		agency.ConsequenceSupersedeReference, "playbook.stale-cas", &second)
	staleRetract := referenceRequest(t, sharedView, "operation:stale-cas-retract",
		agency.ConsequenceRetractReference, "playbook.stale-cas", nil)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, supersede); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, staleRetract)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Code() != rejectionStaleReference {
		t.Fatalf("stale Reference mutation code = %s, want %s", receipt.Code().String(),
			rejectionStaleReference.String())
	}
	if got := countRows(t, fixture.store, "reference_lineage"); got != 2 {
		t.Fatalf("stale Reference mutation created %d lineage rows, want 2", got)
	}
}

func TestConcurrentExistingReferenceHeadCASAcceptsExactlyOneMutation(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-existing-cas")
	first := fixture.catalog(t, "playbook shared head")
	publish := referenceRequest(t, fixture.current(t), "operation:existing-cas-publish",
		agency.ConsequencePublishReference, "playbook.existing-cas", &first)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, publish); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}

	view := fixture.current(t)
	left := fixture.catalog(t, "playbook branch left")
	right := fixture.catalog(t, "playbook branch right")
	requests := []agency.BoundIntent{
		referenceRequest(t, view, "operation:existing-cas-left",
			agency.ConsequenceSupersedeReference, "playbook.existing-cas", &left),
		referenceRequest(t, view, "operation:existing-cas-right",
			agency.ConsequenceSupersedeReference, "playbook.existing-cas", &right),
	}
	results := make(chan AdmissionResult, len(requests))
	errors := make(chan error, len(requests))
	var wait sync.WaitGroup
	for _, request := range requests {
		request := request
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	accepted, stale := 0, 0
	for result := range results {
		switch result.Outcome() {
		case agency.ReceiptOutcomeAccepted:
			accepted++
		case agency.ReceiptOutcomeRejected:
			receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Code() == rejectionStaleReference {
				stale++
			}
		}
	}
	if accepted != 1 || stale != 1 {
		t.Fatalf("existing-head race = accepted:%d stale:%d", accepted, stale)
	}
	if countRows(t, fixture.store, "active_references") != 1 ||
		countRows(t, fixture.store, "reference_lineage") != 2 {
		t.Fatal("existing-head CAS created multiple accepted branches")
	}
}

func TestReferenceCanRetractThenSupersedeTombstone(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-lineage")
	firstDigest := fixture.catalog(t, "playbook v1")
	publish := referenceRequest(t, fixture.current(t), "operation:publish-v1",
		agency.ConsequencePublishReference, "playbook.review", &firstDigest)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, publish); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}

	retractView := fixture.current(t)
	retract := referenceRequest(t, retractView, "operation:retract-v1",
		agency.ConsequenceRetractReference, "playbook.review", nil)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, retract); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	var state string
	if err := fixture.store.db.QueryRow(`SELECT state FROM active_references
		WHERE reference_key='playbook.review'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "retracted" {
		t.Fatalf("Reference state = %q, want retracted", state)
	}

	secondDigest := fixture.catalog(t, "playbook v2")
	supersedeView := fixture.current(t)
	supersede := referenceRequest(t, supersedeView, "operation:supersede-v2",
		agency.ConsequenceSupersedeReference, "playbook.review", &secondDigest)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, supersede); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	var artifact string
	if err := fixture.store.db.QueryRow(`SELECT state, artifact_digest FROM active_references
		WHERE reference_key='playbook.review'`).Scan(&state, &artifact); err != nil {
		t.Fatal(err)
	}
	if state != "active" || artifact != secondDigest.String() {
		t.Fatalf("revived Reference = %s/%s", state, artifact)
	}
	if got := countRows(t, fixture.store, "reference_lineage"); got != 3 {
		t.Fatalf("Reference lineage rows = %d, want 3", got)
	}
}

func TestReferenceRejectsForwardHead(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-forward-head")
	attachment := fixture.current(t).authority.Attachment()
	key := mustReferenceKey(t, "playbook.forward-head")
	headHandle := mustHandle(t, "reference:forward-head")
	futureHead, err := agency.NewEventRef(mustEventID(t, "event:forward-head"),
		agency.Sum([]byte("future Reference Event")))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := agency.ExpectReferenceHead(headHandle, key, futureHead)
	if err != nil {
		t.Fatal(err)
	}
	authorityView, err := agency.NewViewAuthority(agency.MachineViewSpec{
		Attachment:   attachment,
		Consequences: []agency.Consequence{agency.ConsequenceSupersedeReference},
		References:   []agency.ReferenceExpectation{expected},
	})
	if err != nil {
		t.Fatal(err)
	}
	currentOperation := mustCurrentOperation(t, "operation:forward-head-current")
	viewHandle, err := currentViewHandle(attachment, currentOperation.key, authorityView.Digest())
	if err != nil {
		t.Fatal(err)
	}
	agentView, err := agency.NewAgentView(agency.AgentViewSpec{Handle: viewHandle,
		Authority: authorityView, References: []agency.AgentViewReferenceSpec{{
			Head: headHandle, State: agency.AgentViewReferenceStateRetracted,
		}}})
	if err != nil {
		t.Fatal(err)
	}
	view := BoundView{authority: authorityView, public: agentView}
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := insertCurrentOperationTx(fixture.ctx, tx, attachment, currentOperation, view); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	replacement := fixture.catalog(t, "replacement for unseen future head")
	request := referenceRequest(t, view, "operation:forward-head-supersede",
		agency.ConsequenceSupersedeReference, key.String(), &replacement)
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeRejected)
	receipt, err := agency.ParseReceiptCanonicalJSON(result.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Code() != rejectionStaleReference {
		t.Fatalf("forward-head rejection code = %q, want %q", receipt.Code(), rejectionStaleReference)
	}
	for _, table := range []string{"events", "active_references", "reference_lineage"} {
		if got := countRows(t, fixture.store, table); got != 0 {
			t.Fatalf("%s rows after forward-head rejection = %d, want 0", table, got)
		}
	}
}

func TestReferenceTombstoneRejectsFreshRetractAndReplaysOriginal(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-double-retract")
	firstDigest := fixture.catalog(t, "double-retract v1")
	publish := referenceRequest(t, fixture.current(t), "operation:double-retract-publish",
		agency.ConsequencePublishReference, "playbook.double-retract", &firstDigest)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, publish); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}

	retractView := fixture.current(t)
	retract := referenceRequest(t, retractView, "operation:double-retract-first",
		agency.ConsequenceRetractReference, "playbook.double-retract", nil)
	accepted, err := fixture.store.Admit(fixture.ctx, fixture.proof, retract)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, accepted, agency.ReceiptOutcomeAccepted)

	guardDigest := fixture.catalog(t, "active guard Reference")
	guard := referenceRequest(t, fixture.current(t), "operation:double-retract-guard",
		agency.ConsequencePublishReference, "playbook.double-retract-guard", &guardDigest)
	if result, err := fixture.store.Admit(fixture.ctx, fixture.proof, guard); err != nil {
		t.Fatal(err)
	} else {
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}

	fresh := referenceRequest(t, fixture.current(t), "operation:double-retract-fresh",
		agency.ConsequenceRetractReference, "playbook.double-retract", nil)
	rejected, err := fixture.store.Admit(fixture.ctx, fixture.proof, fresh)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, rejected, agency.ReceiptOutcomeRejected)
	receipt, err := agency.ParseReceiptCanonicalJSON(rejected.ReceiptJSON())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Code() != rejectionStaleReference {
		t.Fatalf("tombstone retract rejection code = %q, want %q", receipt.Code(), rejectionStaleReference)
	}

	replayed, err := fixture.store.Admit(fixture.ctx, fixture.proof, retract)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed() || replayed.ReceiptDigest() != accepted.ReceiptDigest() ||
		!bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()) {
		t.Fatalf("original retract replay changed: replay=%v digest=%s bytes_equal=%v",
			replayed.Replayed(), replayed.ReceiptDigest().String(),
			bytes.Equal(replayed.ReceiptJSON(), accepted.ReceiptJSON()))
	}
	if got := countRows(t, fixture.store, "reference_lineage"); got != 3 {
		t.Fatalf("Reference lineage rows = %d, want 3", got)
	}
}

func TestReferenceProjectionBoundDoesNotOfferPublishAboveLimit(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:reference-bound")
	for index := 0; index < maxProjectedReferences; index++ {
		digest := fixture.catalog(t, fmt.Sprintf("playbook %d", index))
		request := referenceRequest(t, fixture.current(t), fmt.Sprintf("operation:bound-%d", index),
			agency.ConsequencePublishReference, fmt.Sprintf("playbook.bound-%d", index), &digest)
		result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
		if err != nil {
			t.Fatal(err)
		}
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}
	view := fixture.current(t)
	if strings.Contains(string(view.AgentView().CanonicalJSON()), "reference.publish") {
		t.Fatal("Reference publish remained offered at the projection bound")
	}
	extraDigest := fixture.catalog(t, "must not exceed the reference bound")
	handle := mustHandle(t, "candidate:operation:bound-extra")
	input, err := agency.NewArtifactCandidate(handle)
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "knowledge.playbook"),
		Payload: mustPayload(t, "extra"), Consequence: agency.ConsequencePublishReference,
		ReferenceKey: mustReferenceKey(t, "playbook.bound-extra"),
		Artifacts:    []agency.ArtifactInput{input}})
	operation := mustOperation(t, "operation:bound-extra")
	candidate, err := agency.NewCapturedCandidate(operation, input, extraDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.Bind(intent, operation, []agency.CapturedCandidate{candidate}); err == nil {
		t.Fatal("Reference publish above the projection bound succeeded")
	}
	if got := countRows(t, fixture.store, "active_references"); got != maxProjectedReferences {
		t.Fatalf("active References = %d, want %d", got, maxProjectedReferences)
	}
}
