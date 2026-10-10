package authority

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

func TestImportedCurrentProjectsOneAuthenticatedReplyTarget(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	distractor := peerRouteSpec(t, fixture.receiver.principal, "distractor")
	mustEnrollPeerRoute(t, fixture.receiver, distractor)

	requestDelivery := fixture.admitOrigin(t)
	local := decodeFocusView(t, fixture.origin.current(t))
	if local.Current == nil || local.Current.Facts.ReplyRequired || local.Current.Facts.ReplyTarget != "" {
		t.Fatalf("local current reply target = %#v, want omitted", local.Current)
	}
	fixture.admitReceiver(t, requestDelivery)
	operation, err := NewCurrentOperation(mustOperation(t, "operation:authenticated-reply-target"))
	if err != nil {
		t.Fatal(err)
	}
	receiverView, err := fixture.receiver.store.Current(fixture.receiver.ctx,
		fixture.receiver.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	public := decodeFocusView(t, receiverView)
	if public.Current == nil || !public.Current.Facts.ReplyRequired ||
		public.Current.Facts.ReplyTarget != fixture.receiverRoute.PublicAlias.String() {
		t.Fatalf("imported current reply target = %#v, want %s",
			public.Current, fixture.receiverRoute.PublicAlias.String())
	}
	if !containsString(public.Targets, fixture.receiverRoute.PublicAlias.String()) ||
		!containsString(public.Targets, distractor.PublicAlias.String()) {
		t.Fatalf("projected targets = %v, want reply and distractor aliases", public.Targets)
	}
	for _, private := range []string{fixture.receiverRoute.RouteID.String(),
		fixture.receiverRoute.RemotePeerID.String(), fixture.receiverRoute.RemoteTargetAlias.String()} {
		if bytes.Contains(receiverView.AgentView().CanonicalJSON(), []byte(private)) {
			t.Fatalf("public reply target projection exposed private route authority %q", private)
		}
	}

	mutated := bytes.Replace(receiverView.AgentView().CanonicalJSON(),
		[]byte(`"reply_target":"`+fixture.receiverRoute.PublicAlias.String()+`"`),
		[]byte(`"reply_target":"`+distractor.PublicAlias.String()+`"`), 1)
	if bytes.Equal(mutated, receiverView.AgentView().CanonicalJSON()) {
		t.Fatal("reply target mutation fixture did not change the public View")
	}
	if _, err := agency.ParseAgentViewCanonicalJSON(mutated, receiverView.authority); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("alternate offered reply target error = %v, want ErrInvariant", err)
	}
	replayed, err := fixture.receiver.store.ReplayCurrent(fixture.receiver.ctx,
		fixture.receiver.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeFocusView(t, replayed); got.Current == nil || !got.Current.Facts.ReplyRequired ||
		got.Current.Facts.ReplyTarget != fixture.receiverRoute.PublicAlias.String() {
		t.Fatalf("replayed imported reply target = %#v", got.Current)
	}

	t.Run("revoked route is not offered", func(t *testing.T) {
		fixture := newPeerRoundTripFixture(t)
		delivery := fixture.admitOrigin(t)
		fixture.admitReceiver(t, delivery)
		if _, err := fixture.receiver.store.RevokePeerRoute(fixture.receiver.ctx,
			fixture.receiverRoute.RouteID); err != nil {
			t.Fatal(err)
		}
		view := decodeFocusView(t, fixture.receiver.current(t))
		if view.Current == nil || view.Current.Facts.ReplyRequired || view.Current.Facts.ReplyTarget != "" ||
			containsString(view.Targets, fixture.receiverRoute.PublicAlias.String()) {
			t.Fatalf("revoked route projection = current:%#v targets:%v", view.Current, view.Targets)
		}
	})
}

func TestPeerRouteProjectionIsScopedToAttachmentPrincipal(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:route-projection-owner")
	route := peerRouteSpec(t, fixture.principal, "projection-scope")
	mustEnrollPeerRoute(t, fixture, route)

	owner := decodeFocusView(t, fixture.current(t))
	if !containsString(owner.Targets, route.PublicAlias.String()) {
		t.Fatalf("owner View targets = %v; want route %s", owner.Targets, route.PublicAlias.String())
	}

	other := mustPrincipal(t, "principal:route-projection-other")
	if err := fixture.store.EnrollPrincipal(fixture.ctx, other); err != nil {
		t.Fatal(err)
	}
	proof, err := fixture.store.IssueInteractiveAttachment(fixture.ctx, other,
		nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
	operation, err := NewCurrentOperation(mustOperation(t, "operation:other-route-view"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := fixture.store.Current(fixture.ctx, proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	projected := decodeFocusView(t, view)
	if containsString(projected.Targets, route.PublicAlias.String()) {
		t.Fatalf("other Principal View leaked route target: %v", projected.Targets)
	}
}

func TestOrdinaryImportedWorkCannotBecomeNoReplyBySemanticKind(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	original := fixture.admitOrigin(t)
	ordinary := rebuildPeerDelivery(t, fixture.originRoute.RouteID, original,
		original.OriginEvent(), agency.EventRef{}, agency.ConsequenceCreateHandlings, 2,
		mustLabel(t, "work.response"))
	fixture.admitReceiver(t, ordinary)
	view := decodeFocusView(t, fixture.receiver.current(t))
	if view.Current == nil || !view.Current.Facts.ReplyRequired ||
		view.Current.Facts.ReplyTarget != fixture.receiverRoute.PublicAlias.String() {
		t.Fatalf("ordinary imported work projection = %#v; semantic kind changed reply role",
			view.Current)
	}
}

func TestImportedHandlingKeepsReplyContextAcrossLocalAdvance(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	request := fixture.admitOrigin(t)
	fixture.admitReceiver(t, request)

	firstView := fixture.receiver.current(t)
	first := requireReplyContext(t, firstView, fixture.receiverRoute.PublicAlias.String(), "")
	advance := subjectRequest(t, firstView, "operation:reply-context-advance",
		agency.ConsequenceAdvanceHandling, "local work remains in progress", nil)
	result, err := fixture.receiver.store.Admit(fixture.receiver.ctx, fixture.receiver.proof, advance)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)

	continuedView := fixture.receiver.current(t)
	continued := requireReplyContext(t, continuedView, first.Current.Facts.ReplyTarget,
		"local work remains in progress")
	admitReplyFromContinuedHandling(t, fixture, continuedView, continued)
	reply := requireOnePendingDelivery(t, fixture.receiver)
	requireOriginCorrelation(t, reply, request.OriginEvent())
	if reply.CausalDepth() != request.CausalDepth()+1 {
		t.Fatalf("continued reply causal depth = %d; want %d",
			reply.CausalDepth(), request.CausalDepth()+1)
	}
	requireRevokedRouteOmitsReplyTarget(t, fixture)
	requireCorruptHandlingCreationFailsClosed(t, fixture)
}

func TestSubjectAdvanceUsesCurrentHandlingAsLocalAnchor(t *testing.T) {
	for _, test := range []struct {
		name               string
		addSelfSuccessor   bool
		wantLocalHandlings int
	}{
		{name: "remote successor only", wantLocalHandlings: 1},
		{name: "separate self responsibility", addSelfSuccessor: true, wantLocalHandlings: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPeerRoundTripFixture(t)
			delivery := fixture.admitOrigin(t)
			fixture.admitReceiver(t, delivery)

			view := fixture.receiver.current(t)
			public := requireReplyContext(t, view,
				fixture.receiverRoute.PublicAlias.String(), "")
			remote, err := agency.AliasTarget(mustHandle(t, public.Current.Facts.ReplyTarget))
			if err != nil {
				t.Fatal(err)
			}
			successors := []agency.TargetRef{remote}
			if test.addSelfSuccessor {
				successors = append([]agency.TargetRef{agency.SelfTarget()}, successors...)
			}
			intent := mustIntent(t, agency.IntentSpec{
				Kind:              mustLabel(t, "opaque.result"),
				Payload:           mustPayload(t, "bounded progress for the requester"),
				Consequence:       agency.ConsequenceAdvanceHandling,
				SubjectHandling:   mustHandle(t, public.Current.Facts.Handle),
				Successors:        successors,
				CorrelationHandle: mustHandle(t, public.Current.Facts.ReplyTo),
			})
			request, err := view.Bind(intent,
				mustOperation(t, "operation:advance-anchor-"+strings.ReplaceAll(test.name, " ", "-")), nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := fixture.receiver.store.Admit(fixture.receiver.ctx,
				fixture.receiver.proof, request)
			if err != nil {
				t.Fatal(err)
			}
			requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
			if got := countRows(t, fixture.receiver.store, "handlings"); got != test.wantLocalHandlings {
				t.Fatalf("local Handlings after advance = %d, want %d",
					got, test.wantLocalHandlings)
			}
			if got := countRows(t, fixture.receiver.store, "peer_outbox"); got != 1 {
				t.Fatalf("remote successors after advance = %d, want 1", got)
			}
		})
	}
}

func TestImportedHandlingCanResolveLocallyWithoutReplyDelivery(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	delivery := fixture.admitOrigin(t)
	fixture.admitReceiver(t, delivery)

	view := fixture.receiver.current(t)
	requireReplyContext(t, view, fixture.receiverRoute.PublicAlias.String(), "")
	beforeOutbox := countRows(t, fixture.receiver.store, "peer_outbox")
	request := subjectRequest(t, view, "operation:local-imported-decline",
		agency.ConsequenceResolveDeclined, "no further local work is justified", nil)
	result, err := fixture.receiver.store.Admit(fixture.receiver.ctx, fixture.receiver.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	if got := countRows(t, fixture.receiver.store, "peer_outbox"); got != beforeOutbox {
		t.Fatalf("local resolve created %d peer outbox rows; want %d", got, beforeOutbox)
	}
	assertHandlingOutcomeCount(t, fixture.receiver, "declined", 1)
	if next := decodeFocusView(t, fixture.receiver.current(t)); next.Current != nil {
		t.Fatalf("locally resolved imported Handling remained current: %#v", next.Current)
	}
	assertOriginAnchorOpen(t, fixture.origin)
}

func TestCorrelatedResponseCanBeConsumedWithoutReplyEcho(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	requestDelivery := fixture.admitOrigin(t)
	fixture.admitReceiver(t, requestDelivery)
	admitCorrelatedPeerResponse(t, &fixture, requestDelivery)

	anchorView := fixture.origin.current(t)
	anchor := decodeFocusView(t, anchorView)
	if anchor.Current == nil {
		t.Fatal("origin request anchor was not selected")
	}
	advance := subjectRequest(t, anchorView, "operation:consume-response-anchor",
		agency.ConsequenceAdvanceHandling, "request anchor considered", nil)
	result, err := fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, advance)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)

	responseView := fixture.origin.current(t)
	response := decodeFocusView(t, responseView)
	if response.Current == nil || response.Current.Semantic.Kind != "review.response" {
		t.Fatalf("correlated response was not selected: %#v", response.Current)
	}
	beforeOutbox := countRows(t, fixture.origin.store, "peer_outbox")
	resolve := subjectRequest(t, responseView, "operation:consume-response-locally",
		agency.ConsequenceResolveUnresolved, "response consumed without follow-up", nil)
	result, err = fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, resolve)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	if got := countRows(t, fixture.origin.store, "peer_outbox"); got != beforeOutbox {
		t.Fatalf("consuming a correlated response changed peer outbox rows from %d to %d",
			beforeOutbox, got)
	}
	assertHandlingOutcomeCount(t, fixture.origin, "unresolved", 1)
	assertHandlingStateCount(t, fixture.origin, "open", 1)
}

func assertHandlingOutcomeCount(t *testing.T, fixture *authorityFixture, outcome string, want int) {
	t.Helper()
	var got int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM handlings
		WHERE state = 'terminal' AND outcome = ?`, outcome).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("terminal/%s Handlings = %d; want %d", outcome, got, want)
	}
}

func assertHandlingStateCount(t *testing.T, fixture *authorityFixture, state string, want int) {
	t.Helper()
	var got int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM handlings
		WHERE state = ?`, state).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s Handlings = %d; want %d", state, got, want)
	}
}

func requireReplyContext(t *testing.T, view BoundView, wantTarget, wantPayload string) focusViewWire {
	t.Helper()
	result := decodeFocusView(t, view)
	if result.Current == nil || result.Current.Facts.ReplyTo == "" ||
		(result.Current.Facts.ReplyRequired != (wantTarget != "")) ||
		result.Current.Facts.ReplyTarget != wantTarget ||
		(wantPayload != "" && result.Current.Semantic.Payload != wantPayload) {
		t.Fatalf("imported reply context = %#v; want target %q payload %q",
			result.Current, wantTarget, wantPayload)
	}
	return result
}

func admitReplyFromContinuedHandling(t *testing.T, fixture peerRoundTripFixture,
	view BoundView, public focusViewWire,
) {
	t.Helper()
	target, err := agency.AliasTarget(mustHandle(t, public.Current.Facts.ReplyTarget))
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, agency.IntentSpec{
		Kind:              mustLabel(t, "opaque.result"),
		Payload:           mustPayload(t, "bounded work could not be completed"),
		Consequence:       agency.ConsequenceAdvanceHandling,
		SubjectHandling:   mustHandle(t, public.Current.Facts.Handle),
		Successors:        []agency.TargetRef{target},
		CorrelationHandle: mustHandle(t, public.Current.Facts.ReplyTo),
	})
	response, err := view.Bind(intent,
		mustOperation(t, "operation:reply-context-result"), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.receiver.store.Admit(fixture.receiver.ctx, fixture.receiver.proof, response)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
}

func requireRevokedRouteOmitsReplyTarget(t *testing.T, fixture peerRoundTripFixture) {
	t.Helper()
	if _, err := fixture.receiver.store.RevokePeerRoute(fixture.receiver.ctx,
		fixture.receiverRoute.RouteID); err != nil {
		t.Fatal(err)
	}
	revoked := decodeFocusView(t, fixture.receiver.current(t))
	if revoked.Current == nil || revoked.Current.Facts.ReplyTo == "" ||
		revoked.Current.Facts.ReplyTarget != "" ||
		containsString(revoked.Targets, fixture.receiverRoute.PublicAlias.String()) {
		t.Fatalf("reply context after route revocation = current:%#v targets:%v",
			revoked.Current, revoked.Targets)
	}
}

func requireCorruptHandlingCreationFailsClosed(t *testing.T, fixture peerRoundTripFixture) {
	t.Helper()
	resultSQL, err := fixture.receiver.store.db.Exec(`UPDATE handlings
		SET created_sequence = (SELECT MAX(origin_sequence) + 1 FROM events)
		WHERE state = 'open'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireOneRow(resultSQL, "corrupt Handling creation sequence"); err != nil {
		t.Fatal(err)
	}
	operation, err := NewCurrentOperation(mustOperation(t, "operation:corrupt-reply-context"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.receiver.store.Current(fixture.receiver.ctx, fixture.receiver.proof,
		operation); err == nil || !strings.Contains(err.Error(), "corrupt Handling creation authority") {
		t.Fatalf("Current with corrupt Handling creation sequence = %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCurrentKeepsOldestAnchorWritableAndProjectsCorrelatedPeerResult(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	requestDelivery := fixture.admitOrigin(t)
	fixture.admitReceiver(t, requestDelivery)
	frozenOperation, frozen := freezeOriginFocusView(t, &fixture)
	if got := decodeFocusView(t, frozen); len(got.Related) != 0 || got.Outstanding.OpenTotal != 1 {
		t.Fatalf("frozen origin View = %#v", got)
	}
	admitCorrelatedPeerResponse(t, &fixture, requestDelivery)

	replayed, err := fixture.origin.store.ReplayCurrent(fixture.origin.ctx,
		fixture.origin.proof, frozenOperation)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeFocusView(t, replayed); len(got.Related) != 0 ||
		got.Outstanding.OpenTotal != 1 {
		t.Fatalf("frozen Current absorbed a later Event: %#v", got)
	}

	fresh := fixture.origin.current(t)
	public := decodeFocusView(t, fresh)
	if public.Current == nil || public.Current.Semantic.Payload != "consider the bounded request" {
		t.Fatalf("oldest anchor was not retained as Current: %#v", public.Current)
	}
	if len(public.Related) != 1 ||
		public.Related[0].Semantic.Kind != "review.response" ||
		public.Related[0].Semantic.Payload != "the bounded request was reviewed" ||
		public.Related[0].Facts.Relation != "correlation" {
		t.Fatalf("correlated response projection = %#v", public.Related)
	}
	if public.Outstanding.OpenTotal != 2 || public.Outstanding.RelatedTotal != 1 ||
		public.Outstanding.RelatedProjected != 1 || public.Outstanding.Truncated {
		t.Fatalf("outstanding projection = %#v", public.Outstanding)
	}

	illegal := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "review.illegal"),
		Consequence:     agency.ConsequenceAdvanceHandling,
		SubjectHandling: mustHandle(t, public.Related[0].Facts.Event)})
	if _, err := fresh.Bind(illegal, mustOperation(t, "operation:focus-illegal"), nil); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("related Event became writable subject: %v", err)
	}
}

func TestCurrentSelectsCorrelatedResponseAfterAnchorAdvance(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	requestDelivery := fixture.admitOrigin(t)
	fixture.admitReceiver(t, requestDelivery)
	admitCorrelatedPeerResponse(t, &fixture, requestDelivery)

	fresh := fixture.origin.current(t)
	advance := subjectRequest(t, fresh, "operation:focus-advance-anchor",
		agency.ConsequenceAdvanceHandling, "anchor progress is durably recorded", nil)
	result, err := fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, advance)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	next := decodeFocusView(t, fixture.origin.current(t))
	if next.Current == nil || next.Current.Semantic.Kind != "review.response" ||
		next.Current.Semantic.Payload != "the bounded request was reviewed" {
		t.Fatalf("advanced anchor starved correlated response: %#v", next.Current)
	}
}

func TestCurrentLeastAttendedSelectionSurvivesBoundaryWithoutAdvance(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	requestDelivery := fixture.admitOrigin(t)
	fixture.admitReceiver(t, requestDelivery)
	admitCorrelatedPeerResponse(t, &fixture, requestDelivery)

	first := decodeFocusView(t, fixture.origin.current(t))
	if first.Current == nil || first.Current.Semantic.Payload != "consider the bounded request" {
		t.Fatalf("first Current = %#v, want original local anchor", first.Current)
	}
	replaceInteractiveBoundary(t, fixture.origin)

	second := decodeFocusView(t, fixture.origin.current(t))
	if second.Current == nil || second.Current.Semantic.Kind != "review.response" ||
		second.Current.Semantic.Payload != "the bounded request was reviewed" {
		t.Fatalf("unadvanced anchor starved peer response: %#v", second.Current)
	}
	replaceInteractiveBoundary(t, fixture.origin)

	third := decodeFocusView(t, fixture.origin.current(t))
	if third.Current == nil || third.Current.Semantic.Payload != "consider the bounded request" {
		t.Fatalf("least-attended tie did not return to older anchor: %#v", third.Current)
	}
}

func replaceInteractiveBoundary(t *testing.T, fixture *authorityFixture) {
	t.Helper()
	ended, err := fixture.store.EndInteractiveAttachment(fixture.ctx, fixture.proof)
	if err != nil || ended.Replayed() || !ended.ReleasedClaim() {
		t.Fatalf("end current boundary = (replayed=%t released=%t, %v)",
			ended.Replayed(), ended.ReleasedClaim(), err)
	}
	fixture.proof, err = fixture.store.IssueInteractiveAttachment(fixture.ctx,
		fixture.principal, nextAttachmentBoundary(t))
	if err != nil {
		t.Fatal(err)
	}
}

func TestFocusProjectionUsesDeterministicBoundedPrefixAndPayloadBudget(t *testing.T) {
	root := focusEventRef(t, "event:focus-budget-root", "root")
	first := focusStoredEvent(t, "event:focus-budget-first", root,
		strings.Repeat("a", agency.MaxAgentViewFocusPayloadBytes/2))
	second := focusStoredEvent(t, "event:focus-budget-second", root, "later")
	unrelated := focusStoredEvent(t, "event:focus-budget-unrelated",
		focusEventRef(t, "event:other-root", "other"), "ignore")
	current := mustPayload(t, strings.Repeat("c", agency.MaxAgentViewFocusPayloadBytes/2))

	projected, total := selectFocusRelated(current, root,
		[]storedEventDetails{unrelated, first, second})
	if total != 2 || len(projected) != 1 || projected[0].details.ref != first.ref {
		t.Fatalf("bounded focus = total %d projected %#v", total, projected)
	}
	overBudget, total := selectFocusRelated(
		mustPayload(t, strings.Repeat("c", agency.MaxAgentViewFocusPayloadBytes)), root,
		[]storedEventDetails{first})
	if total != 1 || len(overBudget) != 0 {
		t.Fatalf("over-budget focus = total %d projected %#v", total, overBudget)
	}
	escapedCurrent := mustPayload(t, strings.Repeat("\x01", 680))
	if len(escapedCurrent.String()) >= agency.MaxAgentViewFocusPayloadBytes ||
		jsonEncodedPayloadBytes(escapedCurrent) > agency.MaxAgentViewFocusPayloadBytes {
		t.Fatal("escaped Current fixture is not independently representable")
	}
	escapedBudget, total := selectFocusRelated(escapedCurrent, root,
		[]storedEventDetails{focusStoredEvent(t, "event:focus-budget-escaped", root,
			strings.Repeat("r", 32))})
	if total != 1 || len(escapedBudget) != 0 {
		t.Fatalf("escaped over-budget focus = total %d projected %#v", total, escapedBudget)
	}
}

func TestCurrentKeepsEscapedCurrentAndOmitsRelatedBeyondEncodedBudget(t *testing.T) {
	fixture := newPeerRoundTripFixture(t)
	view := fixture.origin.current(t)
	remote, err := agency.AliasTarget(fixture.originRoute.PublicAlias)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("\x01", 680)
	operation := mustOperation(t, "operation:focus-escaped-root")
	artifactInput, err := agency.NewArtifactCandidate(mustHandle(t, "candidate:focus-escaped-root"))
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, agency.IntentSpec{
		Kind:        mustLabel(t, "generic.escaped-request"),
		Payload:     mustPayload(t, payload),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget(), remote},
		Artifacts:   []agency.ArtifactInput{artifactInput},
	})
	candidate, err := agency.NewCapturedCandidate(operation, artifactInput, fixture.digest)
	if err != nil {
		t.Fatal(err)
	}
	request, err := view.Bind(intent, operation, []agency.CapturedCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.origin.store.Admit(fixture.origin.ctx, fixture.origin.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	delivery := requireOnePendingDelivery(t, fixture.origin)
	fixture.admitReceiver(t, delivery)
	admitCorrelatedPeerResponse(t, &fixture, delivery)

	fresh := fixture.origin.current(t)
	public := decodeFocusView(t, fresh)
	if public.Current == nil || public.Current.Semantic.Payload != payload {
		t.Fatalf("escaped Current was not readable: %#v", public.Current)
	}
	if len(public.Related) != 0 || public.Outstanding.OpenTotal != 2 ||
		public.Outstanding.RelatedTotal != 1 || public.Outstanding.RelatedProjected != 0 ||
		!public.Outstanding.Truncated {
		t.Fatalf("escaped focus projection = related %d outstanding %#v",
			len(public.Related), public.Outstanding)
	}
	if len(fresh.AgentView().CanonicalJSON()) > agency.MaxAgentViewCanonicalBytes {
		t.Fatalf("escaped Current View exceeds canonical bound: %d",
			len(fresh.AgentView().CanonicalJSON()))
	}
}

func focusStoredEvent(t *testing.T, id string, correlation agency.EventRef,
	payload string,
) storedEventDetails {
	t.Helper()
	return storedEventDetails{ref: focusEventRef(t, id, payload),
		kind: mustLabel(t, "generic.response"), payload: mustPayload(t, payload),
		correlation: correlation}
}

func focusEventRef(t *testing.T, id, body string) agency.EventRef {
	t.Helper()
	ref, err := agency.NewEventRef(mustEventID(t, id), agency.Sum([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func freezeOriginFocusView(t *testing.T, fixture *peerRoundTripFixture) (CurrentOperation, BoundView) {
	t.Helper()
	operation, err := NewCurrentOperation(mustOperation(t, "operation:focus-frozen"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := fixture.origin.store.Current(fixture.origin.ctx, fixture.origin.proof, operation)
	if err != nil {
		t.Fatal(err)
	}
	return operation, view
}

func admitCorrelatedPeerResponse(t *testing.T, fixture *peerRoundTripFixture,
	requestDelivery agency.PeerDelivery,
) {
	t.Helper()
	receiverView := fixture.receiver.current(t)
	receiverPublic := decodeFocusView(t, receiverView)
	if receiverPublic.Current == nil || receiverPublic.Current.Facts.ReplyTo == "" ||
		!receiverPublic.Current.Facts.ReplyRequired ||
		receiverPublic.Current.Facts.ReplyTo == receiverPublic.Current.Facts.Handle ||
		receiverPublic.Current.Facts.ReplyTarget != fixture.receiverRoute.PublicAlias.String() {
		t.Fatal("receiver did not claim the imported request")
	}
	remote, err := agency.AliasTarget(mustHandle(t, receiverPublic.Current.Facts.ReplyTarget))
	if err != nil {
		t.Fatal(err)
	}
	responseIntent := mustIntent(t, agency.IntentSpec{
		Kind:              mustLabel(t, "review.response"),
		Payload:           mustPayload(t, "the bounded request was reviewed"),
		Consequence:       agency.ConsequenceAdvanceHandling,
		SubjectHandling:   mustHandle(t, receiverPublic.Current.Facts.Handle),
		Successors:        []agency.TargetRef{remote},
		CorrelationHandle: mustHandle(t, receiverPublic.Current.Facts.ReplyTo),
	})
	response, err := receiverView.Bind(responseIntent,
		mustOperation(t, "operation:focus-response"), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.receiver.store.Admit(fixture.receiver.ctx, fixture.receiver.proof,
		response)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	responseDelivery := requireOnePendingDelivery(t, fixture.receiver)
	requireOriginCorrelation(t, responseDelivery, requestDelivery.OriginEvent())
	admitSignedResponse(t, fixture, responseDelivery)
}

func requireOnePendingDelivery(t *testing.T, node *authorityFixture) agency.PeerDelivery {
	t.Helper()
	pending, err := node.store.PendingPeerDeliveries(node.ctx, MaxPendingPeerDeliveries)
	if err != nil || len(pending) != 1 {
		t.Fatalf("response PeerDelivery = %#v, %v", pending, err)
	}
	return pending[0].Delivery()
}

func requireOriginCorrelation(t *testing.T, delivery agency.PeerDelivery, want agency.EventRef) {
	t.Helper()
	correlation, present := delivery.OriginCorrelation()
	if !present || correlation != want {
		t.Fatalf("response correlation = %v,%t; want origin anchor %v", correlation, present, want)
	}
}

func admitSignedResponse(t *testing.T, fixture *peerRoundTripFixture, delivery agency.PeerDelivery) {
	t.Helper()
	signature := ed25519.Sign(fixture.receiverPrivate, delivery.SigningMessage())
	staged, err := fixture.origin.store.StagePeerDelivery(fixture.origin.ctx,
		fixture.originRoute.RemotePeerID, delivery.CanonicalJSON(), signature)
	if err != nil || staged.State() != PeerAdmissionStateStaged {
		t.Fatalf("StagePeerDelivery(response) = %#v, %v", staged, err)
	}
	accepted, err := fixture.origin.store.AdmitPeerDelivery(fixture.origin.ctx, delivery.ID())
	if err != nil || accepted.State() != PeerAdmissionStateAccepted {
		t.Fatalf("AdmitPeerDelivery(response) = %#v, %v", accepted, err)
	}
}

type focusViewWire struct {
	Current *struct {
		Facts struct {
			Handle                  string `json:"handle"`
			ReplyTo                 string `json:"reply_to"`
			ReplyRequired           bool   `json:"reply_required"`
			ReplyTarget             string `json:"reply_target"`
			ReplyObservationPending bool   `json:"reply_observation_pending"`
		} `json:"facts"`
		Semantic struct {
			Kind    string `json:"kind"`
			Payload string `json:"payload"`
		} `json:"semantic"`
	} `json:"current"`
	Related []struct {
		Facts struct {
			Event    string `json:"event"`
			Relation string `json:"relation"`
			Outcome  string `json:"outcome"`
		} `json:"facts"`
		Semantic struct {
			Kind    string `json:"kind"`
			Payload string `json:"payload"`
		} `json:"semantic"`
	} `json:"related"`
	Outstanding struct {
		OpenTotal        int  `json:"open_total"`
		RelatedTotal     int  `json:"related_total"`
		RelatedProjected int  `json:"related_projected"`
		Truncated        bool `json:"truncated"`
	} `json:"outstanding"`
	Targets []string `json:"targets"`
}

func decodeFocusView(t *testing.T, view BoundView) focusViewWire {
	t.Helper()
	var wire focusViewWire
	if err := json.Unmarshal(view.AgentView().CanonicalJSON(), &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}
