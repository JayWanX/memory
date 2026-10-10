package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const MaxOpenHandlingsPerPrincipal = 64

type admissionRejection struct {
	code       agency.SemanticLabel
	diagnostic string
}

func validateMutableAuthorityTx(ctx context.Context, tx *sql.Tx, attachment agency.Attachment,
	request agency.BoundIntent, now time.Time,
) (*admissionRejection, error) {
	if rejection, err := validateIssuedViewTx(ctx, tx, attachment, request); err != nil || rejection != nil {
		return rejection, err
	}
	if rejection, err := validateTargetsTx(ctx, tx, attachment, request.Targets()); err != nil || rejection != nil {
		return rejection, err
	}
	if rejection, err := validatePeerDeliveryCapacityTx(ctx, tx, request, now); err != nil || rejection != nil {
		return rejection, err
	}
	claim, claimUntil, err := currentClaimForAdmissionTx(ctx, tx, attachment)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		claim = nil
	}
	if rejection := validateBoundSubject(request, claim, claimUntil, now); rejection != nil {
		return rejection, nil
	}
	if claim == nil {
		rejection, err := rejectIfClaimableTx(ctx, tx, attachment.Principal())
		if err != nil || rejection != nil {
			return rejection, err
		}
	}
	if rejection, err := validateReferenceReadSetTx(ctx, tx, request); err != nil || rejection != nil {
		return rejection, err
	}
	if rejection, err := validateHandlingBoundTx(ctx, tx, attachment.Principal(), request); err != nil || rejection != nil {
		return rejection, err
	}
	if request.Intent().Consequence() == agency.ConsequenceResolveCompleted && len(request.Artifacts()) == 0 {
		return reject(rejectionArtifactUnavailable, "completed requires a verified Artifact"), nil
	}
	return nil, nil
}

// validateIssuedViewTx proves that this request was bound from one exact View
// frozen by Current. BoundIntent construction already resolves every selected
// offer from that View; live subject and Reference heads are checked below.
// Unselected world changes therefore do not invalidate a fresh operation.
func validateIssuedViewTx(ctx context.Context, tx *sql.Tx, attachment agency.Attachment,
	request agency.BoundIntent,
) (*admissionRejection, error) {
	var digestValue string
	var canonical []byte
	err := tx.QueryRowContext(ctx, `SELECT authority_digest, authority_json
		FROM current_operations WHERE attachment_id = ? AND authority_digest = ? LIMIT 1`,
		attachment.ID().String(), request.ViewDigest().String()).Scan(&digestValue, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return reject(rejectionStaleView, "View was not issued by this authority"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("admit Intent: inspect issued View: %w", err)
	}
	digest, err := agency.ParseDigest(digestValue)
	view, parseErr := agency.ParseViewAuthorityCanonicalJSON(canonical, attachment)
	if err != nil || parseErr != nil || digest != request.ViewDigest() || view.Digest() != digest {
		return nil, errors.New("admit Intent: corrupt issued View authority")
	}
	return nil, nil
}

func validateHandlingBoundTx(ctx context.Context, tx *sql.Tx, principal agency.AgentPrincipalID,
	request agency.BoundIntent,
) (*admissionRejection, error) {
	var delta int64
	for _, target := range request.Targets() {
		if target.Destination() == agency.TargetDestinationLocal {
			delta++
		}
	}
	switch request.Intent().Consequence() {
	case agency.ConsequenceResolveCompleted, agency.ConsequenceResolveDeclined,
		agency.ConsequenceResolveUnresolved:
		delta--
	case agency.ConsequencePublishReference, agency.ConsequenceSupersedeReference,
		agency.ConsequenceRetractReference:
		return nil, nil
	}
	if delta <= 0 {
		return nil, nil
	}
	var openCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM handlings
		WHERE target_principal_id = ? AND state = 'open'`, principal.String()).Scan(&openCount); err != nil {
		return nil, fmt.Errorf("admit Intent: count open Handlings: %w", err)
	}
	if openCount+delta > MaxOpenHandlingsPerPrincipal {
		return reject(rejectionResourceBound, "open Handling bound reached"), nil
	}
	return nil, nil
}

func validateTargetsTx(ctx context.Context, tx *sql.Tx, attachment agency.Attachment,
	targets []agency.ResolvedTarget,
) (*admissionRejection, error) {
	for _, target := range targets {
		switch target.Destination() {
		case agency.TargetDestinationLocal:
			if target.LocalPrincipal() != attachment.Principal() {
				return reject(rejectionStaleView, "target is outside the local Principal"), nil
			}
		case agency.TargetDestinationRemote:
			route, found, err := peerRouteByIDTx(ctx, tx, target.RemoteRoute())
			if err != nil {
				return nil, fmt.Errorf("admit Intent: inspect peer route: %w", err)
			}
			if !found || !route.Active() || target.Requested().IsSelf() ||
				target.Requested().Alias() != route.PublicAlias() ||
				target.RemoteAlias() != route.RemoteTargetAlias() {
				return reject(rejectionStaleRoute, "peer route is unavailable, revoked, or changed"), nil
			}
		default:
			return reject(rejectionStaleView, "target destination is invalid"), nil
		}
	}
	return nil, nil
}

func validateBoundSubject(request agency.BoundIntent, claim *projectedClaim,
	claimUntil, now time.Time,
) *admissionRejection {
	subject, present := request.Subject()
	if !present {
		return nil
	}
	if claim == nil || subject.HandlingID() != claim.handlingID || subject.Head() != claim.head ||
		subject.Fence() != claim.fence ||
		subject.ObservationRevision() != claim.observationRevision || !now.Before(claimUntil) {
		return reject(rejectionStaleSubject,
			"subject claim, head, fence, or observation revision is stale")
	}
	return nil
}

func rejectIfClaimableTx(ctx context.Context, tx *sql.Tx,
	principal agency.AgentPrincipalID,
) (*admissionRejection, error) {
	var claimable int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM handlings
		WHERE target_principal_id = ? AND state = 'open' AND claim_attachment_id IS NULL)`,
		principal.String()).Scan(&claimable); err != nil {
		return nil, fmt.Errorf("admit Intent: inspect claimable Handling: %w", err)
	}
	if claimable == 1 {
		return reject(rejectionStaleView, "a pending Handling now requires a current View"), nil
	}
	return nil, nil
}

func currentClaimForAdmissionTx(ctx context.Context, tx *sql.Tx,
	attachment agency.Attachment,
) (*projectedClaim, time.Time, error) {
	var handlingValue, eventValue, claimUntilValue string
	var fence uint64
	err := tx.QueryRowContext(ctx, `SELECT h.handling_id, h.claim_fence, h.head_event_id, h.claim_until
		FROM handlings h
		WHERE h.claim_attachment_id = ? AND h.target_principal_id = ? AND h.state = 'open'`,
		attachment.ID().String(), attachment.Principal().String()).
		Scan(&handlingValue, &fence, &eventValue, &claimUntilValue)
	if err != nil {
		return nil, time.Time{}, err
	}
	handlingID, err := agency.NewHandlingID(handlingValue)
	if err != nil {
		return nil, time.Time{}, errors.New("admit Intent: corrupt claimed Handling ID")
	}
	eventRef, kind, payload, artifacts, err := loadStoredEventTx(ctx, tx, eventValue)
	if err != nil {
		return nil, time.Time{}, err
	}
	observationRevision, err := terminalObservationRevisionTx(ctx, tx, handlingID)
	if err != nil {
		return nil, time.Time{}, err
	}
	claimUntil, err := parseTime(claimUntilValue)
	if err != nil {
		return nil, time.Time{}, err
	}
	return &projectedClaim{handlingID: handlingID, head: eventRef, fence: fence,
		observationRevision: observationRevision,
		kind:                kind, payload: payload, artifacts: artifacts}, claimUntil, nil
}

func validateReferenceReadSetTx(ctx context.Context, tx *sql.Tx,
	request agency.BoundIntent,
) (*admissionRejection, error) {
	if rejection, err := validateReferenceExpectationTx(ctx, tx, request); err != nil || rejection != nil {
		return rejection, err
	}
	return validateReferenceCitationsTx(ctx, tx, request)
}

func validateReferenceExpectationTx(ctx context.Context, tx *sql.Tx,
	request agency.BoundIntent,
) (*admissionRejection, error) {
	expected, present := request.ExpectedReference()
	if !present {
		return nil, nil
	}
	var headValue, headDigestValue, state string
	err := tx.QueryRowContext(ctx, `SELECT r.head_event_id, e.event_digest, r.state
		FROM active_references r JOIN events e ON e.event_id = r.head_event_id
		WHERE r.reference_key = ?`, expected.Key().String()).
		Scan(&headValue, &headDigestValue, &state)
	if expected.IsAbsent() {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("admit Intent: inspect absent Reference: %w", err)
		}
		return reject(rejectionStaleReference, "Reference key is no longer absent"), nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return reject(rejectionStaleReference, "Reference head no longer exists"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("admit Intent: inspect Reference head: %w", err)
	}
	headDigest, digestErr := agency.ParseDigest(headDigestValue)
	if digestErr != nil {
		return nil, errors.New("admit Intent: corrupt Reference head digest")
	}
	if headValue != expected.Head().ID().String() || headDigest != expected.Head().Digest() {
		return reject(rejectionStaleReference, "Reference head changed"), nil
	}
	if request.Intent().Consequence() == agency.ConsequenceRetractReference && state != "active" {
		return reject(rejectionStaleReference, "Reference head is already retracted"), nil
	}
	return nil, nil
}

func validateReferenceCitationsTx(ctx context.Context, tx *sql.Tx,
	request agency.BoundIntent,
) (*admissionRejection, error) {
	// A Reference citation is an existing provenance selection whose exact
	// Event is also a Reference lineage member. Keeping this classification
	// machine-derived avoids a second Agent wire field and a second Event
	// evidence representation.
	provenance := request.Causation()
	if correlation, present := request.Correlation(); present {
		provenance = append(provenance, correlation)
	}
	seen := make(map[string]struct{}, len(provenance))
	for _, cited := range provenance {
		identity := cited.ID().String() + "\x00" + cited.Digest().String()
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}

		var keyValue, citedDigestValue string
		err := tx.QueryRowContext(ctx, `SELECT l.reference_key, e.event_digest
			FROM reference_lineage l JOIN events e ON e.event_id = l.event_id
			WHERE l.event_id = ?`, cited.ID().String()).Scan(&keyValue, &citedDigestValue)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("admit Intent: inspect Reference citation: %w", err)
		}
		key, keyErr := agency.NewReferenceKey(keyValue)
		citedDigest, digestErr := agency.ParseDigest(citedDigestValue)
		if keyErr != nil || digestErr != nil || citedDigest != cited.Digest() {
			return nil, errors.New("admit Intent: corrupt cited Reference lineage")
		}

		var headIDValue, headDigestValue string
		err = tx.QueryRowContext(ctx, `SELECT r.head_event_id, e.event_digest
			FROM active_references r JOIN events e ON e.event_id = r.head_event_id
			WHERE r.reference_key = ?`, key.String()).Scan(&headIDValue, &headDigestValue)
		if errors.Is(err, sql.ErrNoRows) {
			return reject(rejectionStaleReference, "cited Reference head changed"), nil
		}
		if err != nil {
			return nil, fmt.Errorf("admit Intent: inspect cited Reference head: %w", err)
		}
		headID, idErr := agency.NewEventID(headIDValue)
		headDigest, digestErr := agency.ParseDigest(headDigestValue)
		if idErr != nil || digestErr != nil {
			return nil, errors.New("admit Intent: corrupt current Reference head")
		}
		current, err := agency.NewEventRef(headID, headDigest)
		if err != nil {
			return nil, errors.New("admit Intent: corrupt current Reference identity")
		}
		if current != cited {
			return reject(rejectionStaleReference, "cited Reference head changed"), nil
		}
	}
	return nil, nil
}

func reject(code agency.SemanticLabel, diagnostic string) *admissionRejection {
	return &admissionRejection{code: code, diagnostic: diagnostic}
}
