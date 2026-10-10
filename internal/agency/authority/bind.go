package authority

import (
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

// bindIntent resolves only handles offered by one sealed ViewAuthority. The
// Agent supplies semantics; this function owns the authority cut from opaque
// View handles to machine identities, fences, routes, and digests.
func bindIntent(view agency.ViewAuthority, intent agency.AgentIntent,
	operation agency.OperationKey, candidates []agency.CapturedCandidate,
) (agency.BoundIntent, error) {
	if operation.IsZero() || len(intent.CanonicalJSON()) == 0 || view.Digest().IsZero() {
		return agency.BoundIntent{}, bindInvalid("complete Intent, operation, and sealed View are required")
	}
	if !view.Allows(intent.Consequence()) {
		return agency.BoundIntent{}, bindInvariant("consequence was not offered by the View")
	}
	if intent.Consequence() == agency.ConsequenceCreateHandlings && !view.Attachment().MayInitiate() {
		return agency.BoundIntent{}, bindInvariant("Attachment may not initiate root responsibility")
	}

	subject, expectedReference, err := bindSubjectOrReference(view, intent)
	if err != nil {
		return agency.BoundIntent{}, err
	}
	targets, err := bindTargets(view, intent.Successors())
	if err != nil {
		return agency.BoundIntent{}, err
	}
	artifacts, err := bindArtifacts(view, intent.Artifacts(), operation, candidates)
	if err != nil {
		return agency.BoundIntent{}, err
	}
	causation, correlation, err := bindProvenance(view, intent.CausationHandles(),
		intent.CorrelationHandle())
	if err != nil {
		return agency.BoundIntent{}, err
	}
	if err := requireLocalResponsibility(view, intent, targets, correlation); err != nil {
		return agency.BoundIntent{}, err
	}
	var replyDelivery agency.DeliveryID
	if exactReply(view, intent, targets, correlation) {
		_, _, replyDelivery, _ = view.ReplyContext()
	}
	return agency.NewBoundIntent(agency.BoundIntentSpec{
		Intent: intent, OperationKey: operation, Attachment: view.Attachment(),
		ViewDigest: view.Digest(), Subject: subject, ExpectedReference: expectedReference,
		Targets: targets, Artifacts: artifacts, Causation: causation,
		Correlation: correlation, InReplyToDelivery: replyDelivery,
	})
}

func bindSubjectOrReference(view agency.ViewAuthority, intent agency.AgentIntent) (
	*agency.SubjectBinding, *agency.ReferenceExpectation, error,
) {
	switch intent.Consequence() {
	case agency.ConsequenceAdvanceHandling, agency.ConsequenceResolveCompleted,
		agency.ConsequenceResolveDeclined, agency.ConsequenceResolveUnresolved:
		subject, offered := view.ResolveSubject(intent.SubjectHandling())
		if !offered {
			return nil, nil, bindInvariant("subject handle was not offered by the View")
		}
		return &subject, nil, nil
	case agency.ConsequencePublishReference:
		expected, err := agency.ExpectAbsentReference(intent.ReferenceKey())
		if err != nil {
			return nil, nil, err
		}
		return nil, &expected, nil
	case agency.ConsequenceSupersedeReference, agency.ConsequenceRetractReference:
		expected, offered := view.ResolveReference(intent.ReferenceHead())
		if !offered {
			return nil, nil, bindInvariant("Reference head was not offered by the View")
		}
		return nil, &expected, nil
	default:
		return nil, nil, nil
	}
}

type targetDestination struct {
	kind      agency.TargetDestination
	principal agency.AgentPrincipalID
	route     agency.RouteID
	alias     agency.OpaqueHandle
}

func bindTargets(view agency.ViewAuthority, requested []agency.TargetRef) ([]agency.ResolvedTarget, error) {
	result := make([]agency.ResolvedTarget, 0, len(requested))
	seen := make(map[targetDestination]struct{}, len(requested))
	for _, target := range requested {
		resolved, offered := view.ResolveTarget(target)
		if !offered {
			return nil, bindInvariant("successor target was not offered by the View")
		}
		key := targetDestination{kind: resolved.Destination(), principal: resolved.LocalPrincipal(),
			route: resolved.RemoteRoute(), alias: resolved.RemoteAlias()}
		if _, duplicate := seen[key]; duplicate {
			return nil, bindInvariant("successors resolve to a duplicate destination")
		}
		seen[key] = struct{}{}
		result = append(result, resolved)
	}
	return result, nil
}

func bindArtifacts(view agency.ViewAuthority, inputs []agency.ArtifactInput,
	operation agency.OperationKey, candidates []agency.CapturedCandidate,
) ([]agency.ResolvedArtifact, error) {
	captured := make(map[string]agency.CapturedCandidate, len(candidates))
	for _, candidate := range candidates {
		input := candidate.Input()
		if candidate.OperationKey() != operation || input.Kind() != agency.ArtifactInputCandidate ||
			input.Handle().IsZero() || candidate.Digest().IsZero() {
			return nil, bindInvalid("candidate capture is incomplete or belongs to another operation")
		}
		key := input.Handle().String()
		if _, duplicate := captured[key]; duplicate {
			return nil, bindInvalid("candidate captures contain a duplicate handle")
		}
		captured[key] = candidate
	}

	result := make([]agency.ResolvedArtifact, 0, len(inputs))
	usedCandidates := 0
	for _, input := range inputs {
		var digest agency.Digest
		switch input.Kind() {
		case agency.ArtifactInputCandidate:
			candidate, found := captured[input.Handle().String()]
			if !found || candidate.Input() != input {
				return nil, bindInvariant("Artifact candidate was not captured for this operation")
			}
			digest = candidate.Digest()
			usedCandidates++
		case agency.ArtifactInputViewHandle:
			var err error
			digest, err = view.ResolveOfferedArtifact(input.Handle())
			if err != nil {
				return nil, err
			}
		default:
			return nil, bindInvalid("Artifact input kind is invalid")
		}
		resolved, err := agency.NewResolvedArtifact(input, digest)
		if err != nil {
			return nil, err
		}
		result = append(result, resolved)
	}
	if usedCandidates != len(captured) {
		return nil, bindInvariant("candidate captures contain an unused input")
	}
	return result, nil
}

func bindProvenance(view agency.ViewAuthority, handles []agency.OpaqueHandle,
	correlationHandle agency.OpaqueHandle,
) ([]agency.EventRef, agency.EventRef, error) {
	causation := make([]agency.EventRef, 0, len(handles))
	for _, handle := range handles {
		event, offered := view.ResolveProvenance(handle)
		if !offered {
			return nil, agency.EventRef{}, bindInvariant("causation handle was not offered by the View")
		}
		causation = append(causation, event)
	}
	var correlation agency.EventRef
	if !correlationHandle.IsZero() {
		var offered bool
		correlation, offered = view.ResolveProvenance(correlationHandle)
		if !offered {
			return nil, agency.EventRef{}, bindInvariant("correlation handle was not offered by the View")
		}
	}
	return causation, correlation, nil
}

func requireLocalResponsibility(view agency.ViewAuthority, intent agency.AgentIntent,
	targets []agency.ResolvedTarget, correlation agency.EventRef,
) error {
	remote, local := false, false
	for _, target := range targets {
		remote = remote || target.Destination() == agency.TargetDestinationRemote
		local = local || target.Destination() == agency.TargetDestinationLocal
	}
	if !remote || intent.Consequence() == agency.ConsequenceAdvanceHandling ||
		exactReply(view, intent, targets, correlation) {
		return nil
	}
	if intent.Consequence() == agency.ConsequenceCreateHandlings ||
		terminalConsequence(intent.Consequence()) {
		if !local {
			return bindInvariant("remote request must leave one causal local Handling open")
		}
	}
	return nil
}

func exactReply(view agency.ViewAuthority, intent agency.AgentIntent,
	targets []agency.ResolvedTarget, correlation agency.EventRef,
) bool {
	replyTo, replyTarget, delivery, offered := view.ReplyContext()
	if !offered || replyTarget.IsZero() || delivery.IsZero() || len(targets) != 1 ||
		!terminalConsequence(intent.Consequence()) || intent.CorrelationHandle() != replyTo ||
		correlation.IsZero() || targets[0].Destination() != agency.TargetDestinationRemote ||
		targets[0].Requested() != replyTarget {
		return false
	}
	expected, found := view.ResolveProvenance(replyTo)
	return found && expected == correlation
}

func terminalConsequence(value agency.Consequence) bool {
	return value == agency.ConsequenceResolveCompleted ||
		value == agency.ConsequenceResolveDeclined ||
		value == agency.ConsequenceResolveUnresolved
}

func bindInvalid(problem string) error {
	return fmt.Errorf("%w: Intent binding: %s", agency.ErrInvalid, problem)
}

func bindInvariant(problem string) error {
	return fmt.Errorf("%w: Intent binding: %s", agency.ErrInvariant, problem)
}
