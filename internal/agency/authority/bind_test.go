package authority

import (
	"errors"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestBindIntentUsesOnlySealedViewAuthority(t *testing.T) {
	principal := mustPrincipal(t, "agent:bind-local")
	self, err := agency.ResolveLocalTarget(agency.SelfTarget(), principal)
	if err != nil {
		t.Fatal(err)
	}
	view := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{self},
	})
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.action"),
		Payload:     mustPayload(t, "Continue the durable responsibility."),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget()}})
	bound, err := bindIntent(view, intent, mustOperation(t, "operation:bind-root"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Attachment() != view.Attachment() || bound.ViewDigest() != view.Digest() ||
		len(bound.Targets()) != 1 || bound.Targets()[0].LocalPrincipal() != principal {
		t.Fatal("BoundIntent did not preserve exact sealed authority")
	}

	unoffered := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceAdvanceHandling},
		Targets:      []agency.ResolvedTarget{self},
	})
	if _, err := bindIntent(unoffered, intent, mustOperation(t, "operation:unoffered"), nil); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("unoffered consequence error = %v", err)
	}
	managed := mustBindingView(t, principal, false, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{self},
	})
	if _, err := bindIntent(managed, intent, mustOperation(t, "operation:managed-root"), nil); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("machine boundary initiated root responsibility: %v", err)
	}
}

func TestBindIntentKeepsAuthorityClassesAndDestinationsExact(t *testing.T) {
	principal := mustPrincipal(t, "agent:bind-typed")
	shared := mustHandle(t, "opaque:shared")
	artifactOffer, err := agency.NewViewArtifactOffer(shared, agency.Sum([]byte("artifact")))
	if err != nil {
		t.Fatal(err)
	}
	view := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceAdvanceHandling},
		Artifacts:    []agency.ViewArtifactOffer{artifactOffer},
	})
	advance := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.advance"),
		Consequence: agency.ConsequenceAdvanceHandling, SubjectHandling: shared})
	if _, err := bindIntent(view, advance, mustOperation(t, "operation:cross-class"), nil); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("Artifact handle was repurposed as subject: %v", err)
	}

	firstRef, err := agency.AliasTarget(mustHandle(t, "target:first"))
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := agency.AliasTarget(mustHandle(t, "target:second"))
	if err != nil {
		t.Fatal(err)
	}
	destination := mustPrincipal(t, "agent:same-destination")
	first, _ := agency.ResolveLocalTarget(firstRef, destination)
	second, _ := agency.ResolveLocalTarget(secondRef, destination)
	duplicateView := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{first, second},
	})
	root := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.request"),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{firstRef, secondRef}})
	if _, err := bindIntent(duplicateView, root, mustOperation(t, "operation:duplicate-target"), nil); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("duplicate resolved destination error = %v", err)
	}
}

func TestBindIntentCapturesArtifactsPerOperationAndLane(t *testing.T) {
	principal := mustPrincipal(t, "agent:bind-artifact")
	self, _ := agency.ResolveLocalTarget(agency.SelfTarget(), principal)
	operation := mustOperation(t, "operation:artifact-bind")
	first := mustCandidateInput(t, "candidate:first")
	second := mustCandidateInput(t, "candidate:second")
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.produce"),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget()},
		Artifacts:   []agency.ArtifactInput{first, second}})
	view := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{self},
	})
	firstCapture := mustCapture(t, operation, first, "first")
	secondCapture := mustCapture(t, operation, second, "second")
	bound, err := bindIntent(view, intent, operation,
		[]agency.CapturedCandidate{secondCapture, firstCapture})
	if err != nil || len(bound.Artifacts()) != 2 {
		t.Fatalf("capture binding = %#v, %v", bound, err)
	}
	wrong := mustCapture(t, mustOperation(t, "operation:other"), first, "first")
	if _, err := bindIntent(view, intent, operation,
		[]agency.CapturedCandidate{wrong, secondCapture}); !errors.Is(err, agency.ErrInvalid) {
		t.Fatalf("wrong-operation capture error = %v", err)
	}
	if _, err := bindIntent(view, intent, operation,
		[]agency.CapturedCandidate{firstCapture}); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("missing capture error = %v", err)
	}
	unusedInput := mustCandidateInput(t, "candidate:unused")
	unused := mustCapture(t, operation, unusedInput, "unused")
	if _, err := bindIntent(view, intent, operation,
		[]agency.CapturedCandidate{firstCapture, secondCapture, unused}); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("unused capture error = %v", err)
	}
	duplicateFirst := mustCapture(t, operation, first, "same")
	duplicateSecond := mustCapture(t, operation, second, "same")
	if _, err := bindIntent(view, intent, operation,
		[]agency.CapturedCandidate{duplicateFirst, duplicateSecond}); !errors.Is(err, agency.ErrInvalid) {
		t.Fatalf("duplicate Artifact digest error = %v", err)
	}
}

func TestBindIntentResolvesOpenReferenceAndExactProvenance(t *testing.T) {
	principal := mustPrincipal(t, "agent:bind-reference")
	operation := mustOperation(t, "operation:publish-reference")
	input := mustCandidateInput(t, "candidate:playbook")
	key := mustReferenceKey(t, "playbook.review")
	publish := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "knowledge.publish"),
		Consequence: agency.ConsequencePublishReference, ReferenceKey: key,
		Artifacts: []agency.ArtifactInput{input}})
	view := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequencePublishReference},
	})
	bound, err := bindIntent(view, publish, operation,
		[]agency.CapturedCandidate{mustCapture(t, operation, input, "review playbook")})
	if err != nil {
		t.Fatal(err)
	}
	expected, exists := bound.ExpectedReference()
	if !exists || !expected.IsAbsent() || expected.Key() != key {
		t.Fatalf("first-publish expectation = %#v, %t", expected, exists)
	}

	self, _ := agency.ResolveLocalTarget(agency.SelfTarget(), principal)
	firstHandle := mustHandle(t, "provenance:first")
	secondHandle := mustHandle(t, "provenance:second")
	correlationHandle := mustHandle(t, "provenance:correlation")
	firstEvent := mustEventRef(t, "event:first-cause", "first")
	secondEvent := mustEventRef(t, "event:second-cause", "second")
	correlationEvent := mustEventRef(t, "event:correlation", "correlation")
	firstOffer, _ := agency.NewProvenanceOffer(firstHandle, firstEvent)
	secondOffer, _ := agency.NewProvenanceOffer(secondHandle, secondEvent)
	correlationOffer, _ := agency.NewProvenanceOffer(correlationHandle, correlationEvent)
	provenanceView := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{self},
		Provenance:   []agency.ProvenanceOffer{correlationOffer, secondOffer, firstOffer},
	})
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.followup"),
		Consequence:       agency.ConsequenceCreateHandlings,
		Successors:        []agency.TargetRef{agency.SelfTarget()},
		CausationHandles:  []agency.OpaqueHandle{secondHandle, firstHandle},
		CorrelationHandle: correlationHandle,
	})
	resolved, err := bindIntent(provenanceView, intent,
		mustOperation(t, "operation:provenance"), nil)
	if err != nil {
		t.Fatal(err)
	}
	causation := resolved.Causation()
	correlation, exists := resolved.Correlation()
	if len(causation) != 2 || causation[0] != firstEvent || causation[1] != secondEvent ||
		!exists || correlation != correlationEvent {
		t.Fatalf("resolved provenance = %#v, %#v/%t", causation, correlation, exists)
	}
}

func TestBindIntentRequiresLocalAnchorExceptForExactTerminalReply(t *testing.T) {
	principal := mustPrincipal(t, "agent:bind-origin")
	requested, err := agency.AliasTarget(mustHandle(t, "target:peer"))
	if err != nil {
		t.Fatal(err)
	}
	remote, err := agency.ResolveRemoteTarget(requested, mustRoute(t, "route:peer"),
		mustHandle(t, "peer:principal"))
	if err != nil {
		t.Fatal(err)
	}
	root := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.delegate"),
		Consequence: agency.ConsequenceCreateHandlings, Successors: []agency.TargetRef{requested}})
	view := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{remote},
	})
	if _, err := bindIntent(view, root, mustOperation(t, "operation:unanchored"), nil); !errors.Is(err, agency.ErrInvariant) {
		t.Fatalf("unanchored remote request error = %v", err)
	}

	self, _ := agency.ResolveLocalTarget(agency.SelfTarget(), principal)
	anchored := mustBindingView(t, principal, true, agency.MachineViewSpec{
		Consequences: []agency.Consequence{agency.ConsequenceCreateHandlings},
		Targets:      []agency.ResolvedTarget{self, remote},
	})
	anchoredIntent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.delegate"),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget(), requested}})
	if _, err := bindIntent(anchored, anchoredIntent,
		mustOperation(t, "operation:anchored"), nil); err != nil {
		t.Fatalf("anchored remote request: %v", err)
	}

	subject := mustSubjectBinding(t, "handling:reply", "event:request", "request")
	replyTo := mustHandle(t, "provenance:reply")
	replyEvent := mustEventRef(t, "event:remote-request", "remote-request")
	provenance, err := agency.NewProvenanceOffer(replyTo, replyEvent)
	if err != nil {
		t.Fatal(err)
	}
	delivery := mustDeliveryIDValue(t, "bind-reply")
	replyView := mustBindingView(t, principal, false, agency.MachineViewSpec{
		Consequences:  []agency.Consequence{agency.ConsequenceResolveDeclined},
		Subjects:      []agency.SubjectBinding{subject},
		Targets:       []agency.ResolvedTarget{remote},
		Provenance:    []agency.ProvenanceOffer{provenance},
		ReplyTo:       replyTo,
		ReplyTarget:   requested,
		ReplyDelivery: delivery,
	})
	reply := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "agent.reply"),
		Consequence:     agency.ConsequenceResolveDeclined,
		SubjectHandling: subject.Handle(), Successors: []agency.TargetRef{requested},
		CorrelationHandle: replyTo})
	bound, err := bindIntent(replyView, reply, mustOperation(t, "operation:reply"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := bound.InReplyToDelivery(); !ok || got != delivery {
		t.Fatalf("reply Delivery = %v, %v", got, ok)
	}
}

func mustBindingView(t *testing.T, principal agency.AgentPrincipalID, mayInitiate bool,
	spec agency.MachineViewSpec,
) agency.ViewAuthority {
	t.Helper()
	id, err := agency.NewAttachmentID("attachment:" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	attachment, err := agency.NewAttachment(id, principal, mayInitiate, now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	spec.Attachment = attachment
	view, err := agency.NewViewAuthority(spec)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func mustCandidateInput(t *testing.T, handle string) agency.ArtifactInput {
	t.Helper()
	input, err := agency.NewArtifactCandidate(mustHandle(t, handle))
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func mustCapture(t *testing.T, operation agency.OperationKey, input agency.ArtifactInput,
	content string,
) agency.CapturedCandidate {
	t.Helper()
	candidate, err := agency.NewCapturedCandidate(operation, input, agency.Sum([]byte(content)))
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func mustEventRef(t *testing.T, id, content string) agency.EventRef {
	t.Helper()
	eventID, err := agency.NewEventID(id)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := agency.NewEventRef(eventID, agency.Sum([]byte(content)))
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func mustSubjectBinding(t *testing.T, handling, event, content string) agency.SubjectBinding {
	t.Helper()
	handlingID, err := agency.NewHandlingID(handling)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := agency.NewSubjectBinding(mustHandle(t, "subject:"+handling), handlingID,
		mustEventRef(t, event, content), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
