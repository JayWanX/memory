package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/JayWanX/memory/internal/agency"
)

func projectBoundViewTx(ctx context.Context, tx *sql.Tx, attachment agency.Attachment,
	claim *projectedClaim, operation agency.OperationKey,
) (BoundView, error) {
	references, err := loadReferencesTx(ctx, tx)
	if err != nil {
		return BoundView{}, err
	}
	spec := agency.MachineViewSpec{Attachment: attachment,
		Consequences: projectedConsequences(claim, references, attachment.MayInitiate())}
	if err := projectTargetsTx(ctx, tx, attachment, claim != nil, &spec); err != nil {
		return BoundView{}, err
	}
	publicSpec := agency.AgentViewSpec{}
	if err := projectViewContentTx(ctx, tx, attachment.Principal(), claim, references,
		&spec, &publicSpec); err != nil {
		return BoundView{}, err
	}
	authorityView, err := agency.NewViewAuthority(spec)
	if err != nil {
		return BoundView{}, err
	}
	viewHandle, err := currentViewHandle(attachment, operation, authorityView.Digest())
	if err != nil {
		return BoundView{}, err
	}
	publicSpec.Handle = viewHandle
	publicSpec.Authority = authorityView
	publicView, err := agency.NewAgentView(publicSpec)
	if err != nil {
		return BoundView{}, err
	}
	return BoundView{authority: authorityView, public: publicView}, nil
}

func projectTargetsTx(ctx context.Context, tx *sql.Tx, attachment agency.Attachment,
	hasClaim bool, spec *agency.MachineViewSpec,
) error {
	if !hasClaim && !attachment.MayInitiate() {
		return nil
	}
	self, err := agency.ResolveLocalTarget(agency.SelfTarget(), attachment.Principal())
	if err != nil {
		return err
	}
	spec.Targets = []agency.ResolvedTarget{self}
	routes, err := loadActivePeerRoutesTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route.LocalTargetPrincipal() != attachment.Principal() {
			continue
		}
		requested, err := agency.AliasTarget(route.PublicAlias())
		if err != nil {
			return errors.New("current View: corrupt peer route public alias")
		}
		resolved, err := agency.ResolveRemoteTarget(requested, route.RouteID(),
			route.RemoteTargetAlias())
		if err != nil {
			return errors.New("current View: corrupt peer route target authority")
		}
		spec.Targets = append(spec.Targets, resolved)
	}
	return nil
}

func projectViewContentTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	claim *projectedClaim, references []projectedReference, spec *agency.MachineViewSpec,
	publicSpec *agency.AgentViewSpec,
) error {
	var reply currentReplyContext
	if claim != nil {
		var err error
		reply, err = currentReplyContextTx(ctx, tx, claim.handlingID, claim.head)
		if err != nil {
			return err
		}
		spec.ReplyObservationPending, err = replyObservationPendingTx(ctx, tx,
			claim.handlingID)
		if err != nil {
			return err
		}
		spec.ReplyTarget = reply.target
		spec.ReplyDelivery = reply.delivery
		if err := projectClaim(claim, reply.root, spec, publicSpec); err != nil {
			return err
		}
	}
	related, outstanding, err := loadFocusProjectionTx(ctx, tx, principal, claim, reply.root)
	if err != nil {
		return err
	}
	for _, item := range related {
		if err := projectRelated(item, spec, publicSpec); err != nil {
			return err
		}
	}
	publicSpec.Outstanding = outstanding
	for _, reference := range references {
		if err := projectReference(reference, spec, publicSpec); err != nil {
			return err
		}
	}
	return nil
}

func loadActivePeerRoutesTx(ctx context.Context, tx *sql.Tx) ([]PeerRouteProjection, error) {
	rows, err := tx.QueryContext(ctx, peerRouteColumns+
		` WHERE state = 'active' ORDER BY route_id LIMIT ?`, MaxActivePeerRoutes+1)
	if err != nil {
		return nil, fmt.Errorf("current View: load active peer routes: %w", err)
	}
	defer rows.Close()
	routes := make([]PeerRouteProjection, 0, MaxActivePeerRoutes)
	for rows.Next() {
		route, err := scanPeerRoute(rows)
		if err != nil {
			return nil, fmt.Errorf("current View: scan peer route: %w", err)
		}
		if !route.Active() {
			return nil, errors.New("current View: active peer route query returned revoked route")
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current View: iterate peer routes: %w", err)
	}
	if len(routes) > MaxActivePeerRoutes {
		return nil, fmt.Errorf("current View: peer route projection exceeds %d", MaxActivePeerRoutes)
	}
	return routes, nil
}

func projectedConsequences(claim *projectedClaim,
	references []projectedReference, mayInitiate bool,
) []agency.Consequence {
	var result []agency.Consequence
	if len(references) < maxProjectedReferences {
		result = append(result, agency.ConsequencePublishReference)
	}
	if claim == nil {
		if mayInitiate {
			result = append(result, agency.ConsequenceCreateHandlings)
		}
	} else {
		result = append(result, agency.ConsequenceAdvanceHandling, agency.ConsequenceResolveCompleted,
			agency.ConsequenceResolveDeclined, agency.ConsequenceResolveUnresolved)
	}
	if len(references) == 0 {
		return result
	}
	result = append(result, agency.ConsequenceSupersedeReference)
	for _, reference := range references {
		if reference.state == "active" {
			return append(result, agency.ConsequenceRetractReference)
		}
	}
	return result
}

func currentViewHandle(attachment agency.Attachment, operation agency.OperationKey,
	authorityDigest agency.Digest,
) (agency.OpaqueHandle, error) {
	if attachment.ID().IsZero() || operation.IsZero() || authorityDigest.IsZero() {
		return agency.OpaqueHandle{}, errors.New("current View: handle authority is incomplete")
	}
	return deterministicHandle("view", attachment.ID().String(), operation.String(),
		authorityDigest.String())
}

func projectClaim(claim *projectedClaim, replyRoot agency.EventRef, spec *agency.MachineViewSpec,
	publicSpec *agency.AgentViewSpec,
) error {
	if replyRoot.IsZero() {
		return errors.New("current View: reply root is required")
	}
	subjectHandle, err := deterministicHandle("subject", claim.handlingID.String(),
		claim.head.ID().String(), claim.head.Digest().String(), fmt.Sprint(claim.fence),
		fmt.Sprint(claim.observationRevision))
	if err != nil {
		return err
	}
	subject, err := agency.NewSubjectBinding(subjectHandle, claim.handlingID, claim.head,
		claim.fence, claim.observationRevision)
	if err != nil {
		return err
	}
	provenance, err := agency.NewProvenanceOffer(subjectHandle, claim.head)
	if err != nil {
		return err
	}
	spec.Subjects = append(spec.Subjects, subject)
	spec.Provenance = append(spec.Provenance, provenance)
	replyHandle := subjectHandle
	if replyRoot != claim.head {
		replyHandle, err = deterministicHandle("reply-to", claim.head.ID().String(),
			replyRoot.ID().String(), replyRoot.Digest().String())
		if err != nil {
			return err
		}
		replyOffer, err := agency.NewProvenanceOffer(replyHandle, replyRoot)
		if err != nil {
			return err
		}
		spec.Provenance = append(spec.Provenance, replyOffer)
	}
	spec.ReplyTo = replyHandle
	current := agency.AgentViewCurrentSpec{Subject: subjectHandle, ReplyTo: replyHandle,
		Kind: claim.kind, Payload: claim.payload}
	for _, digest := range claim.artifacts {
		handle, err := deterministicHandle("artifact", claim.head.ID().String(), digest.String())
		if err != nil {
			return err
		}
		offer, err := agency.NewViewArtifactOffer(handle, digest)
		if err != nil {
			return err
		}
		spec.Artifacts = append(spec.Artifacts, offer)
		current.Artifacts = append(current.Artifacts, handle)
	}
	publicSpec.Current = &current
	return nil
}

func projectReference(reference projectedReference, spec *agency.MachineViewSpec,
	publicSpec *agency.AgentViewSpec,
) error {
	headHandle, err := deterministicHandle("reference", reference.key.String(),
		reference.head.ID().String(), reference.head.Digest().String())
	if err != nil {
		return err
	}
	expectation, err := agency.ExpectReferenceHead(headHandle, reference.key, reference.head)
	if err != nil {
		return err
	}
	provenance, err := agency.NewProvenanceOffer(headHandle, reference.head)
	if err != nil {
		return err
	}
	spec.References = append(spec.References, expectation)
	spec.Provenance = append(spec.Provenance, provenance)
	publicReference := agency.AgentViewReferenceSpec{Head: headHandle,
		State: agency.AgentViewReferenceStateRetracted}
	if reference.state == "active" {
		publicReference.State = agency.AgentViewReferenceStateActive
		artifactHandle, err := deterministicHandle("reference-artifact", reference.head.ID().String(),
			reference.artifact.String())
		if err != nil {
			return err
		}
		offer, err := agency.NewViewArtifactOffer(artifactHandle, reference.artifact)
		if err != nil {
			return err
		}
		spec.Artifacts = append(spec.Artifacts, offer)
		publicReference.Artifact = artifactHandle
	}
	publicSpec.References = append(publicSpec.References, publicReference)
	return nil
}

func loadReferencesTx(ctx context.Context, tx *sql.Tx) ([]projectedReference, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.reference_key, r.state, r.artifact_digest,
		r.head_event_id, e.event_digest
		FROM active_references r JOIN events e ON e.event_id = r.head_event_id
		ORDER BY r.reference_key LIMIT ?`, maxProjectedReferences+1)
	if err != nil {
		return nil, fmt.Errorf("current View: load References: %w", err)
	}
	defer rows.Close()
	var result []projectedReference
	for rows.Next() {
		reference, err := scanProjectedReference(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current View: iterate References: %w", err)
	}
	if len(result) > maxProjectedReferences {
		return nil, fmt.Errorf("current View: Reference projection exceeds %d", maxProjectedReferences)
	}
	return result, nil
}

func scanProjectedReference(row rowScanner) (projectedReference, error) {
	var keyValue, state, eventValue, digestValue string
	var artifactValue sql.NullString
	if err := row.Scan(&keyValue, &state, &artifactValue, &eventValue, &digestValue); err != nil {
		return projectedReference{}, fmt.Errorf("current View: scan Reference: %w", err)
	}
	key, err := agency.NewReferenceKey(keyValue)
	if err != nil {
		return projectedReference{}, errors.New("current View: corrupt Reference key")
	}
	eventID, err := agency.NewEventID(eventValue)
	if err != nil {
		return projectedReference{}, errors.New("current View: corrupt Reference Event ID")
	}
	digest, err := agency.ParseDigest(digestValue)
	if err != nil {
		return projectedReference{}, errors.New("current View: corrupt Reference Event digest")
	}
	head, err := agency.NewEventRef(eventID, digest)
	if err != nil {
		return projectedReference{}, err
	}
	reference := projectedReference{key: key, head: head, state: state}
	if state == "retracted" && !artifactValue.Valid {
		return reference, nil
	}
	if state != "active" || !artifactValue.Valid {
		return projectedReference{}, errors.New("current View: corrupt Reference state")
	}
	reference.artifact, err = agency.ParseDigest(artifactValue.String)
	if err != nil {
		return projectedReference{}, errors.New("current View: corrupt Reference Artifact digest")
	}
	return reference, nil
}

func deterministicHandle(domain string, values ...string) (agency.OpaqueHandle, error) {
	material := domain + "\x00" + strings.Join(values, "\x00")
	digest := agency.Sum([]byte(material)).String()
	return agency.NewOpaqueHandle("r7:" + domain + ":" + strings.TrimPrefix(digest, "sha256:"))
}
