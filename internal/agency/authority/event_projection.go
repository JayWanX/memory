package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/JayWanX/memory/internal/agency"
)

func loadEventArtifactsTx(ctx context.Context, tx *sql.Tx,
	eventID agency.EventID,
) ([]agency.Digest, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artifact_digest FROM event_artifacts
		WHERE event_id = ? ORDER BY artifact_digest`, eventID.String())
	if err != nil {
		return nil, fmt.Errorf("current View: load Event Artifacts: %w", err)
	}
	defer rows.Close()
	var result []agency.Digest
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		digest, err := agency.ParseDigest(value)
		if err != nil {
			return nil, errors.New("current View: corrupt Event Artifact digest")
		}
		result = append(result, digest)
	}
	return result, rows.Err()
}

type storedEventDetails struct {
	ref         agency.EventRef
	kind        agency.SemanticLabel
	payload     agency.SemanticPayload
	artifacts   []agency.Digest
	causation   []agency.EventRef
	correlation agency.EventRef
	consequence agency.Consequence
	inReplyTo   agency.DeliveryID
}

func loadStoredEventTx(ctx context.Context, tx *sql.Tx, idValue string) (
	agency.EventRef, agency.SemanticLabel, agency.SemanticPayload, []agency.Digest, error,
) {
	details, err := loadStoredEventDetailsTx(ctx, tx, idValue)
	if err != nil {
		return agency.EventRef{}, agency.SemanticLabel{}, agency.SemanticPayload{}, nil, err
	}
	return details.ref, details.kind, details.payload, details.artifacts, nil
}

func loadStoredEventDetailsTx(ctx context.Context, tx *sql.Tx,
	idValue string,
) (storedEventDetails, error) {
	var digestValue, sourceValue, requestValue, acceptedValue string
	var originSequence uint64
	var causalDepth uint16
	var canonical []byte
	err := tx.QueryRowContext(ctx, `SELECT event_digest, origin_sequence, causal_depth,
		source_principal_id, request_digest, accepted_at, canonical_json FROM events WHERE event_id = ?`, idValue).
		Scan(&digestValue, &originSequence, &causalDepth, &sourceValue, &requestValue,
			&acceptedValue, &canonical)
	if err != nil {
		return storedEventDetails{}, fmt.Errorf("current View: load Event: %w", err)
	}
	details, canonicalArtifacts, err := inspectStoredEventDetails(idValue, digestValue,
		originSequence, causalDepth, sourceValue, requestValue, acceptedValue, canonical)
	if err != nil {
		return storedEventDetails{}, err
	}
	artifacts, err := loadEventArtifactsTx(ctx, tx, details.ref.ID())
	if err != nil {
		return storedEventDetails{}, err
	}
	if !slices.Equal(canonicalArtifacts, artifacts) {
		return storedEventDetails{}, errors.New("current View: Event Artifact pins diverge from canonical bytes")
	}
	details.artifacts = artifacts
	return details, nil
}

func inspectStoredEvent(idValue, digestValue string, originSequence uint64, causalDepth uint16,
	sourceValue, requestValue, acceptedValue string, canonical []byte,
) (agency.EventRef, agency.SemanticLabel, agency.SemanticPayload, []agency.Digest, error) {
	details, artifacts, err := inspectStoredEventDetails(idValue, digestValue, originSequence,
		causalDepth, sourceValue, requestValue, acceptedValue, canonical)
	if err != nil {
		return agency.EventRef{}, agency.SemanticLabel{}, agency.SemanticPayload{}, nil, err
	}
	return details.ref, details.kind, details.payload, artifacts, nil
}

func inspectStoredEventDetails(idValue, digestValue string, originSequence uint64, causalDepth uint16,
	sourceValue, requestValue, acceptedValue string, canonical []byte,
) (storedEventDetails, []agency.Digest, error) {
	digest, err := agency.ParseDigest(digestValue)
	if err != nil || agency.Sum(canonical) != digest {
		return storedEventDetails{}, nil, errors.New("current View: corrupt Event bytes")
	}
	event, err := agency.ParseEventCanonicalJSON(canonical)
	if err != nil {
		return storedEventDetails{}, nil, fmt.Errorf("current View: invalid Event projection: %w", err)
	}
	if event.Digest() != digest {
		return storedEventDetails{}, nil, errors.New("current View: corrupt Event bytes")
	}
	if err := validateStoredEventAuthority(event, idValue, originSequence, causalDepth, sourceValue,
		requestValue, acceptedValue); err != nil {
		return storedEventDetails{}, nil, err
	}
	correlation, _ := event.Correlation()
	inReplyTo, _ := event.InReplyToDelivery()
	return storedEventDetails{ref: event.Ref(), kind: event.Kind(), payload: event.Payload(),
		causation: event.Causation(), correlation: correlation, consequence: event.Consequence(),
		inReplyTo: inReplyTo}, event.Artifacts(), nil
}

func validateStoredEventAuthority(event agency.Event, idValue string,
	originSequence uint64, causalDepth uint16, sourceValue, requestValue, acceptedValue string,
) error {
	if event.ID().String() != idValue || event.OriginSequence() != originSequence ||
		event.CausalDepth() != causalDepth || event.Source().String() != sourceValue ||
		event.RequestDigest().String() != requestValue {
		return errors.New("current View: Event authority columns diverge from canonical bytes")
	}
	acceptedAt, err := parseTime(acceptedValue)
	if err != nil || !acceptedAt.Equal(event.AcceptedAt()) {
		return errors.New("current View: Event accepted time diverges from canonical bytes")
	}
	return nil
}
