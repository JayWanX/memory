package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

var errCausalEventUnavailable = errors.New("admit Intent: causal Event is unavailable")

// deriveLocalEventCausalDepthTx carries the greatest already-accepted Peer
// hop depth named by the request. Local transitions do not add a hop; only an
// outbound PeerDelivery does. A root with no accepted predecessor has depth
// zero.
func deriveLocalEventCausalDepthTx(ctx context.Context, tx *sql.Tx,
	request agency.BoundIntent,
) (uint16, error) {
	refs := make([]agency.EventRef, 0, len(request.Causation())+3)
	directRemotePredecessors := make(map[string]struct{})
	if subject, present := request.Subject(); present {
		refs = append(refs, subject.Head())
		predecessors, err := subjectRemotePredecessorsTx(ctx, tx, subject)
		if err != nil {
			return 0, err
		}
		for _, predecessor := range predecessors {
			directRemotePredecessors[eventRefKey(predecessor)] = struct{}{}
		}
	}
	if expected, present := request.ExpectedReference(); present && !expected.IsAbsent() {
		refs = append(refs, expected.Head())
	}
	refs = append(refs, request.Causation()...)
	if correlation, present := request.Correlation(); present {
		refs = append(refs, correlation)
	}

	var maximum uint16
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		key := eventRefKey(ref)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		depth, err := exactEventDepthTx(ctx, tx, ref)
		if errors.Is(err, errCausalEventUnavailable) {
			if _, authenticated := directRemotePredecessors[key]; authenticated {
				continue
			}
		}
		if err != nil {
			return 0, err
		}
		if depth > maximum {
			maximum = depth
		}
	}
	return maximum, nil
}

func subjectRemotePredecessorsTx(ctx context.Context, tx *sql.Tx,
	subject agency.SubjectBinding,
) ([]agency.EventRef, error) {
	details, err := loadStoredEventDetailsTx(ctx, tx, subject.Head().ID().String())
	if err != nil || details.ref != subject.Head() {
		return nil, errors.New("admit Intent: subject causal authority is corrupt")
	}
	result := append([]agency.EventRef(nil), details.causation...)
	if !details.correlation.IsZero() {
		result = append(result, details.correlation)
	}
	reply, err := currentReplyContextTx(ctx, tx, subject.HandlingID(), subject.Head())
	if err != nil {
		return nil, err
	}
	if reply.imported {
		result = append(result, reply.root)
	}
	return result, nil
}

func eventRefKey(ref agency.EventRef) string {
	return ref.ID().String() + "\x00" + ref.Digest().String()
}

func exactEventDepthTx(ctx context.Context, tx *sql.Tx, ref agency.EventRef) (uint16, error) {
	var digestValue, sourceValue, requestValue, acceptedValue string
	var originSequence uint64
	var depth int64
	var canonical []byte
	err := tx.QueryRowContext(ctx, `SELECT event_digest, origin_sequence, causal_depth,
		source_principal_id, request_digest, accepted_at, canonical_json FROM events
		WHERE event_id = ?`, ref.ID().String()).Scan(&digestValue, &originSequence, &depth,
		&sourceValue, &requestValue, &acceptedValue, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errCausalEventUnavailable
	}
	if err != nil {
		return 0, fmt.Errorf("admit Intent: inspect causal Event: %w", err)
	}
	if depth < 0 || depth > agency.MaxPeerCausalDepth {
		return 0, errors.New("admit Intent: causal Event authority is corrupt")
	}
	storedRef, _, _, _, err := inspectStoredEvent(ref.ID().String(), digestValue, originSequence,
		uint16(depth), sourceValue, requestValue, acceptedValue, canonical)
	if err != nil || storedRef != ref {
		return 0, errors.New("admit Intent: causal Event authority is corrupt")
	}
	return uint16(depth), nil
}
