package authority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

type currentReplyContext struct {
	root     agency.EventRef
	target   agency.TargetRef
	delivery agency.DeliveryID
	imported bool
}

// currentReplyContextTx freezes the stable correlation root at the Event that
// created the durable Handling. When that Event was imported on an active
// route, the exact public alias can carry a response to its authenticated
// sender across any number of local advances. Route and peer identity remain
// private authority and never enter the Agent View.
func currentReplyContextTx(ctx context.Context, tx *sql.Tx,
	handling agency.HandlingID, current agency.EventRef,
) (currentReplyContext, error) {
	if handling.IsZero() || current.IsZero() {
		return currentReplyContext{}, errors.New("current View: corrupt current reply context")
	}
	creation, err := handlingCreationEventTx(ctx, tx, handling, current)
	if err != nil {
		return currentReplyContext{}, err
	}
	delivery, route, imported, err := peerDeliveryForLocalEventTx(ctx, tx, creation.ref)
	if err != nil {
		return currentReplyContext{}, err
	}
	result := currentReplyContext{root: creation.ref, imported: imported}
	if !creation.correlation.IsZero() {
		result.root = creation.correlation
	} else if imported {
		if correlation, present := delivery.OriginCorrelation(); present {
			result.root = correlation
		} else {
			result.root = delivery.OriginEvent()
		}
	}
	if !imported || !route.Active() {
		return result, nil
	}
	result.delivery = delivery.ID()
	result.target, err = agency.AliasTarget(route.PublicAlias())
	if err != nil {
		return currentReplyContext{}, errors.New("current View: corrupt reply target alias")
	}
	return result, nil
}

func handlingCreationEventTx(ctx context.Context, tx *sql.Tx, handling agency.HandlingID,
	current agency.EventRef,
) (storedEventDetails, error) {
	var eventID string
	err := tx.QueryRowContext(ctx, `SELECT created.event_id
		FROM handlings h JOIN events created ON created.origin_sequence = h.created_sequence
		WHERE h.handling_id = ? AND h.state = 'open' AND h.head_event_id = ?`,
		handling.String(), current.ID().String()).Scan(&eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return storedEventDetails{}, errors.New("current View: corrupt Handling creation authority")
	}
	if err != nil {
		return storedEventDetails{}, fmt.Errorf("current View: load Handling creation authority: %w", err)
	}
	creation, err := loadStoredEventDetailsTx(ctx, tx, eventID)
	if err != nil {
		return storedEventDetails{}, err
	}
	return creation, nil
}

func peerDeliveryForLocalEventTx(ctx context.Context, tx *sql.Tx,
	local agency.EventRef,
) (agency.PeerDelivery, PeerRouteProjection, bool, error) {
	var deliveryValue, routeValue string
	err := tx.QueryRowContext(ctx, `SELECT delivery_id, route_id FROM peer_inbox
		WHERE local_event_id = ?`, local.ID().String()).Scan(&deliveryValue, &routeValue)
	if errors.Is(err, sql.ErrNoRows) {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false, nil
	}
	if err != nil {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false,
			fmt.Errorf("current View: inspect imported Event: %w", err)
	}
	deliveryID, err := agency.ParseDeliveryID(deliveryValue)
	if err != nil {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false,
			errors.New("current View: corrupt imported Delivery ID")
	}
	routeID, err := agency.NewRouteID(routeValue)
	if err != nil {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false,
			errors.New("current View: corrupt imported Route ID")
	}
	route, found, err := peerRouteByIDTx(ctx, tx, routeID)
	if err != nil || !found {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false,
			errors.New("current View: imported Event route authority is absent")
	}
	result, found, err := peerInboxResultTx(ctx, tx, deliveryID)
	if err != nil || !found || result.State() != PeerAdmissionStateAccepted {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false,
			errors.New("current View: corrupt imported Event authority")
	}
	receipt, present := result.Receipt()
	if !present {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false,
			errors.New("current View: imported Event Receipt is absent")
	}
	receiptEvent, accepted := receipt.LocalEvent()
	if !accepted || receiptEvent != local {
		return agency.PeerDelivery{}, PeerRouteProjection{}, false,
			errors.New("current View: imported Event Receipt diverges")
	}
	return result.Delivery(), route, true, nil
}

type projectedRelated struct {
	details  storedEventDetails
	relation agency.AgentViewRelation
	outcome  agency.AgentViewTerminalOutcome
}

func loadFocusProjectionTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	claim *projectedClaim, focusRoot agency.EventRef,
) ([]projectedRelated, agency.AgentViewOutstanding, error) {
	heads, err := loadOpenHeadsTx(ctx, tx, principal)
	if err != nil {
		return nil, agency.AgentViewOutstanding{}, err
	}
	outstanding := agency.AgentViewOutstanding{OpenTotal: len(heads)}
	if claim == nil {
		return nil, outstanding, nil
	}
	if focusRoot.IsZero() {
		return nil, agency.AgentViewOutstanding{}, errors.New("current View: corrupt current focus root")
	}
	candidates := make([]storedEventDetails, 0, len(heads))
	for _, head := range heads {
		if head.handlingID == claim.handlingID.String() {
			continue
		}
		details, err := loadStoredEventDetailsTx(ctx, tx, head.eventID)
		if err != nil {
			return nil, agency.AgentViewOutstanding{}, err
		}
		candidates = append(candidates, details)
	}
	projected, openRelatedTotal := selectFocusRelated(claim.payload, focusRoot, candidates)
	observations, err := loadTerminalObservationCandidatesTx(ctx, tx, claim.handlingID)
	if err != nil {
		return nil, agency.AgentViewOutstanding{}, err
	}
	if observation := selectTerminalObservation(claim.payload, claim.fence, observations); observation != nil {
		projected = observation
	}
	outstanding.RelatedTotal = openRelatedTotal + len(observations)
	outstanding.RelatedProjected = len(projected)
	outstanding.Truncated = outstanding.RelatedProjected < outstanding.RelatedTotal
	return projected, outstanding, nil
}

func selectFocusRelated(currentPayload agency.SemanticPayload, root agency.EventRef,
	candidates []storedEventDetails,
) ([]projectedRelated, int) {
	projected := make([]projectedRelated, 0, agency.MaxAgentViewRelated)
	relatedTotal := 0
	encodedPayloadBytes := jsonEncodedPayloadBytes(currentPayload)
	for _, details := range candidates {
		if details.correlation != root {
			continue
		}
		relatedTotal++
		relatedPayloadBytes := jsonEncodedPayloadBytes(details.payload)
		withinPayload := encodedPayloadBytes <= agency.MaxAgentViewFocusPayloadBytes &&
			relatedPayloadBytes <= agency.MaxAgentViewFocusPayloadBytes-encodedPayloadBytes
		if len(projected) < agency.MaxAgentViewRelated && withinPayload {
			projected = append(projected, projectedRelated{details: details,
				relation: agency.AgentViewRelationCorrelation})
			encodedPayloadBytes += relatedPayloadBytes
		}
	}
	return projected, relatedTotal
}

type terminalObservationCandidate struct {
	details storedEventDetails
	outcome agency.AgentViewTerminalOutcome
}

// replyObservationPendingTx projects whether one current requester anchor has
// any ordinary outbound reply binding for which no exact terminal observation
// has yet been accepted. Delivery settlement is deliberately irrelevant: an
// ACK is not a reply, and this read-only fact neither changes Handling state
// nor removes any offered subject consequence.
func replyObservationPendingTx(ctx context.Context, tx *sql.Tx,
	anchor agency.HandlingID,
) (bool, error) {
	if anchor.IsZero() {
		return false, errors.New("current View: reply observation anchor is required")
	}
	var pending int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM peer_outbox outbound
		WHERE outbound.reply_anchor_handling_id = ?
		AND NOT EXISTS(
			SELECT 1 FROM peer_inbox inbox
			WHERE inbox.in_reply_to_delivery_id = outbound.delivery_id
			AND inbox.local_event_id IS NOT NULL))`, anchor.String()).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("current View: inspect pending reply observation: %w", err)
	}
	if pending != 0 && pending != 1 {
		return false, errors.New("current View: corrupt pending reply observation")
	}
	return pending == 1, nil
}

func loadTerminalObservationCandidatesTx(ctx context.Context, tx *sql.Tx,
	anchor agency.HandlingID,
) ([]terminalObservationCandidate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT inbox.local_event_id, outbound.delivery_id
		FROM peer_inbox inbox
		JOIN peer_outbox outbound ON outbound.delivery_id = inbox.in_reply_to_delivery_id
		JOIN events observed ON observed.event_id = inbox.local_event_id
		WHERE outbound.reply_anchor_handling_id = ? AND inbox.local_event_id IS NOT NULL
		ORDER BY observed.origin_sequence, observed.event_id LIMIT ?`, anchor.String(),
		MaxTerminalObservationsPerAnchor+1)
	if err != nil {
		return nil, fmt.Errorf("current View: load terminal observations: %w", err)
	}
	defer rows.Close()
	type observationRow struct{ eventID, outboundID string }
	var selected []observationRow
	for rows.Next() {
		var row observationRow
		if err := rows.Scan(&row.eventID, &row.outboundID); err != nil {
			return nil, fmt.Errorf("current View: scan terminal observation: %w", err)
		}
		selected = append(selected, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current View: iterate terminal observations: %w", err)
	}
	if len(selected) > MaxTerminalObservationsPerAnchor {
		return nil, errors.New("current View: terminal observation bound violated")
	}
	result := make([]terminalObservationCandidate, 0, len(selected))
	for _, row := range selected {
		details, err := loadStoredEventDetailsTx(ctx, tx, row.eventID)
		if err != nil {
			return nil, err
		}
		outbound, err := agency.ParseDeliveryID(row.outboundID)
		if err != nil || details.inReplyTo != outbound {
			return nil, errors.New("current View: terminal observation reply binding diverges")
		}
		outcome, err := terminalObservationOutcome(details.consequence)
		if err != nil {
			return nil, err
		}
		result = append(result, terminalObservationCandidate{details: details, outcome: outcome})
	}
	return result, nil
}

func terminalObservationOutcome(consequence agency.Consequence) (agency.AgentViewTerminalOutcome, error) {
	switch consequence {
	case agency.ConsequenceObserveCompleted:
		return agency.AgentViewTerminalOutcomeCompleted, nil
	case agency.ConsequenceObserveDeclined:
		return agency.AgentViewTerminalOutcomeDeclined, nil
	case agency.ConsequenceObserveUnresolved:
		return agency.AgentViewTerminalOutcomeUnresolved, nil
	default:
		return agency.AgentViewTerminalOutcomeInvalid,
			errors.New("current View: linked reply is not a terminal observation")
	}
}

func selectTerminalObservation(currentPayload agency.SemanticPayload, fence uint64,
	candidates []terminalObservationCandidate,
) []projectedRelated {
	if len(candidates) == 0 || fence == 0 {
		return nil
	}
	encodedPayloadBytes := jsonEncodedPayloadBytes(currentPayload)
	start := int((fence - 1) % uint64(len(candidates)))
	for offset := 0; offset < len(candidates); offset++ {
		candidate := candidates[(start+offset)%len(candidates)]
		relatedPayloadBytes := jsonEncodedPayloadBytes(candidate.details.payload)
		if encodedPayloadBytes <= agency.MaxAgentViewFocusPayloadBytes &&
			relatedPayloadBytes <= agency.MaxAgentViewFocusPayloadBytes-encodedPayloadBytes {
			return []projectedRelated{{details: candidate.details,
				relation: agency.AgentViewRelationTerminalReply, outcome: candidate.outcome}}
		}
	}
	return nil
}

// jsonEncodedPayloadBytes measures the JSON string content rather than its raw
// UTF-8 input. Excluding the two quote delimiters preserves the payload-byte
// meaning of the budget while accounting for control, quote, backslash, HTML,
// and line-separator escaping that consumes the final AgentView byte bound.
func jsonEncodedPayloadBytes(payload agency.SemanticPayload) int {
	encoded, err := json.Marshal(payload.String())
	if err != nil || len(encoded) < 2 {
		return agency.MaxAgentViewFocusPayloadBytes + 1
	}
	return len(encoded) - 2
}

type openHead struct{ handlingID, eventID string }

func loadOpenHeadsTx(ctx context.Context, tx *sql.Tx,
	principal agency.AgentPrincipalID,
) ([]openHead, error) {
	// Keep bounded focus enumeration aligned with fresh Current selection so
	// projections and writable attention share one deterministic order.
	rows, err := tx.QueryContext(ctx, `SELECT handling_id, head_event_id FROM handlings
		WHERE target_principal_id = ? AND state = 'open'
		ORDER BY claim_fence, created_sequence, handling_id LIMIT ?`, principal.String(),
		MaxOpenHandlingsPerPrincipal+1)
	if err != nil {
		return nil, fmt.Errorf("current View: load open Handlings: %w", err)
	}
	defer rows.Close()
	heads := make([]openHead, 0, MaxOpenHandlingsPerPrincipal)
	for rows.Next() {
		var head openHead
		if err := rows.Scan(&head.handlingID, &head.eventID); err != nil {
			return nil, fmt.Errorf("current View: scan open Handling: %w", err)
		}
		heads = append(heads, head)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current View: iterate open Handlings: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("current View: close open Handlings: %w", err)
	}
	if len(heads) > MaxOpenHandlingsPerPrincipal {
		return nil, errors.New("current View: open Handling bound violated")
	}
	return heads, nil
}

func projectRelated(related projectedRelated, spec *agency.MachineViewSpec,
	publicSpec *agency.AgentViewSpec,
) error {
	eventHandle, err := deterministicHandle("related", related.details.ref.ID().String(),
		related.details.ref.Digest().String())
	if err != nil {
		return err
	}
	provenance, err := agency.NewProvenanceOffer(eventHandle, related.details.ref)
	if err != nil {
		return err
	}
	spec.Provenance = append(spec.Provenance, provenance)
	public := agency.AgentViewRelatedSpec{Event: eventHandle, Relation: related.relation,
		Outcome: related.outcome, Kind: related.details.kind, Payload: related.details.payload}
	for _, digest := range related.details.artifacts {
		handle, err := deterministicHandle("related-artifact", related.details.ref.ID().String(),
			digest.String())
		if err != nil {
			return err
		}
		offer, err := agency.NewViewArtifactOffer(handle, digest)
		if err != nil {
			return err
		}
		spec.Artifacts = append(spec.Artifacts, offer)
		public.Artifacts = append(public.Artifacts, handle)
	}
	publicSpec.Related = append(publicSpec.Related, public)
	return nil
}
