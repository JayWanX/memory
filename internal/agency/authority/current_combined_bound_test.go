package authority

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JayWanX/memory/internal/agency"
)

// This is the cross-field bound oracle: every independently accepted maximum
// must still leave a newly created responsibility readable. The individual
// Event, Reference, route, and Artifact bounds are not sufficient evidence.
func TestMaximumAcceptedWorldKeepsCurrentReadable(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:combined-view-bound")
	for index := 0; index < MaxActivePeerRoutes; index++ {
		if _, err := fixture.store.EnrollPeerRoute(fixture.ctx,
			maximalPeerRouteSpec(t, fixture.principal, index)); err != nil {
			t.Fatalf("enroll maximal route %d: %v", index, err)
		}
	}
	for index := 0; index < maxProjectedReferences; index++ {
		digest := fixture.catalog(t, fmt.Sprintf("combined playbook %d", index))
		request := referenceRequest(t, fixture.current(t),
			fmt.Sprintf("operation:combined-reference-%d", index),
			agency.ConsequencePublishReference,
			combinedReferenceKey(index), &digest)
		result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
		if err != nil {
			t.Fatal(err)
		}
		requireOutcome(t, result, agency.ReceiptOutcomeAccepted)
	}

	view := fixture.current(t)
	operation := mustOperation(t, "operation:combined-current")
	inputs := make([]agency.ArtifactInput, 0, agency.MaxArtifactInputs)
	candidates := make([]agency.CapturedCandidate, 0, agency.MaxArtifactInputs)
	for index := 0; index < agency.MaxArtifactInputs; index++ {
		digest := fixture.catalog(t, fmt.Sprintf("combined current artifact %d", index))
		handle := mustHandle(t, fmt.Sprintf("candidate:combined-current-%02d", index))
		input, err := agency.NewArtifactCandidate(handle)
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := agency.NewCapturedCandidate(operation, input, digest)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input)
		candidates = append(candidates, candidate)
	}
	intent := mustIntent(t, agency.IntentSpec{
		Kind:        mustLabel(t, "generic.maximum-world"),
		Payload:     mustPayload(t, strings.Repeat("p", agency.MaxSemanticPayloadBytes)),
		Consequence: agency.ConsequenceCreateHandlings,
		Successors:  []agency.TargetRef{agency.SelfTarget()},
		Artifacts:   inputs,
	})
	request, err := view.Bind(intent, operation, candidates)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.store.Admit(fixture.ctx, fixture.proof, request)
	if err != nil {
		t.Fatal(err)
	}
	requireOutcome(t, result, agency.ReceiptOutcomeAccepted)

	current := fixture.current(t)
	if size := len(current.AgentView().CanonicalJSON()); size > agency.MaxAgentViewCanonicalBytes {
		t.Fatalf("maximum accepted world projected %d bytes, want <= %d",
			size, agency.MaxAgentViewCanonicalBytes)
	}
}

func combinedReferenceKey(index int) string {
	suffix := fmt.Sprintf("%02d", index)
	prefix := "combined."
	return prefix + strings.Repeat("k", agency.MaxReferenceKeyBytes-len(prefix)-len(suffix)) + suffix
}
