package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

type outboxRow struct {
	ID, RouteID, OriginID, EnvelopeDigest, State, CreatedAt string
	Canonical                                               []byte
	ReplyAnchor, ExpectedRootID, ExpectedRootDigest         sql.NullString
	SettledAt, ReceiptDigest                                sql.NullString
	ReceiptJSON                                             []byte
}

type inboxRow struct {
	ID, RouteID, EnvelopeDigest, State, ReceivedAt string
	Canonical                                      []byte
	InReplyToDelivery                              sql.NullString
	SettledAt, LocalEventID, ReceiptDigest         sql.NullString
	ReceiptJSON                                    []byte
}

func loadDeliveries(ctx context.Context, db *sql.DB, role string,
	events map[string]eventEvidence,
) ([]deliveryEvidence, int, error) {
	outbox, err := loadOutbox(ctx, db, role, events)
	if err != nil {
		return nil, 0, err
	}
	inbox, accepted, err := loadInbox(ctx, db, role, events)
	if err != nil {
		return nil, 0, err
	}
	return append(outbox, inbox...), accepted, nil
}

func loadOutbox(ctx context.Context, db *sql.DB, role string,
	events map[string]eventEvidence,
) ([]deliveryEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT delivery_id, route_id, origin_event_id,
		reply_anchor_handling_id, expected_reply_root_event_id,
		expected_reply_root_event_digest, envelope_digest, delivery_json, state, created_at, settled_at,
		receipt_digest, receipt_json FROM peer_outbox ORDER BY created_at, delivery_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []deliveryEvidence
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.ID, &row.RouteID, &row.OriginID, &row.ReplyAnchor,
			&row.ExpectedRootID, &row.ExpectedRootDigest, &row.EnvelopeDigest,
			&row.Canonical, &row.State, &row.CreatedAt, &row.SettledAt,
			&row.ReceiptDigest, &row.ReceiptJSON); err != nil {
			return nil, err
		}
		value, err := parseOutboxRow(role, row, events)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func parseOutboxRow(role string, row outboxRow,
	events map[string]eventEvidence,
) (deliveryEvidence, error) {
	delivery, err := parseStoredDelivery(row.RouteID, row.ID, row.EnvelopeDigest, row.Canonical)
	if err != nil {
		return deliveryEvidence{}, fmt.Errorf("%s outbox Delivery: %w", role, err)
	}
	origin, exists := events[row.OriginID]
	if !exists || delivery.OriginEvent().ID().String() != origin.ID ||
		delivery.OriginEvent().Digest().String() != origin.Digest {
		return deliveryEvidence{}, fmt.Errorf("%s outbox Delivery has invalid origin Event", role)
	}
	capturedAt, err := parseStoredTime("outbox created_at", row.CreatedAt)
	if err != nil {
		return deliveryEvidence{}, err
	}
	value, err := newDeliveryEvidence(role, "outbox", row.RouteID, row.State, capturedAt, delivery)
	if err != nil {
		return deliveryEvidence{}, fmt.Errorf("%s outbox Delivery evidence: %w", role, err)
	}
	if row.ReplyAnchor.Valid != row.ExpectedRootID.Valid ||
		row.ReplyAnchor.Valid != row.ExpectedRootDigest.Valid {
		return deliveryEvidence{}, fmt.Errorf("%s outbox Delivery has a partial reply binding", role)
	}
	if row.ReplyAnchor.Valid {
		if _, err := agency.NewHandlingID(row.ReplyAnchor.String); err != nil {
			return deliveryEvidence{}, fmt.Errorf("%s outbox Delivery has an invalid reply anchor", role)
		}
		if err := validateEventRef(eventRefWire{ID: row.ExpectedRootID.String,
			Digest: row.ExpectedRootDigest.String}); err != nil {
			return deliveryEvidence{}, fmt.Errorf("%s outbox Delivery has an invalid expected reply root", role)
		}
		value.ReplyAnchorHandlingID = row.ReplyAnchor.String
		value.ExpectedReplyRootID = row.ExpectedRootID.String
		value.ExpectedReplyRootDigest = row.ExpectedRootDigest.String
	}
	switch row.State {
	case "settled":
		value, err = bindPeerReceipt(value, delivery, row.SettledAt,
			row.ReceiptDigest, row.ReceiptJSON)
	case "expired":
		value.CapturedAt, err = parseNullableStoredTime("outbox settled_at", row.SettledAt)
	}
	if err != nil {
		return deliveryEvidence{}, fmt.Errorf("%s outbox %s Delivery: %w", role, row.State, err)
	}
	return value, nil
}

func loadInbox(ctx context.Context, db *sql.DB, role string,
	events map[string]eventEvidence,
) ([]deliveryEvidence, int, error) {
	rows, err := db.QueryContext(ctx, `SELECT delivery_id, route_id, in_reply_to_delivery_id, envelope_digest,
		delivery_json, state, received_at, settled_at, local_event_id,
		receipt_digest, receipt_json FROM peer_inbox ORDER BY received_at, delivery_id`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []deliveryEvidence
	accepted := 0
	for rows.Next() {
		var row inboxRow
		if err := rows.Scan(&row.ID, &row.RouteID, &row.InReplyToDelivery,
			&row.EnvelopeDigest, &row.Canonical,
			&row.State, &row.ReceivedAt, &row.SettledAt, &row.LocalEventID,
			&row.ReceiptDigest, &row.ReceiptJSON); err != nil {
			return nil, 0, err
		}
		value, isAccepted, err := parseInboxRow(role, row, events)
		if err != nil {
			return nil, 0, err
		}
		if isAccepted {
			accepted++
		}
		result = append(result, value)
	}
	return result, accepted, rows.Err()
}

func parseInboxRow(role string, row inboxRow,
	events map[string]eventEvidence,
) (deliveryEvidence, bool, error) {
	delivery, err := parseStoredDelivery(row.RouteID, row.ID, row.EnvelopeDigest, row.Canonical)
	if err != nil {
		return deliveryEvidence{}, false, fmt.Errorf("%s inbox Delivery: %w", role, err)
	}
	if !storedInboxReplyMatches(delivery, row.InReplyToDelivery) {
		return deliveryEvidence{}, false,
			fmt.Errorf("%s inbox Delivery reply binding differs from signed envelope", role)
	}
	capturedAt, err := parseStoredTime("inbox received_at", row.ReceivedAt)
	if err != nil {
		return deliveryEvidence{}, false, err
	}
	value, err := newDeliveryEvidence(role, "inbox", row.RouteID, row.State, capturedAt, delivery)
	if err != nil {
		return deliveryEvidence{}, false, fmt.Errorf("%s inbox Delivery evidence: %w", role, err)
	}
	if row.State == "expired" {
		value.CapturedAt, err = parseNullableStoredTime("inbox settled_at", row.SettledAt)
		return value, false, err
	}
	if row.State != "settled" {
		return value, false, nil
	}
	value, err = bindPeerReceipt(value, delivery, row.SettledAt, row.ReceiptDigest, row.ReceiptJSON)
	if err != nil {
		return deliveryEvidence{}, false, fmt.Errorf("%s inbox settled Delivery: %w", role, err)
	}
	return validateInboxLocalEvent(role, row.LocalEventID, value, events)
}

func storedInboxReplyMatches(delivery agency.PeerDelivery, stored sql.NullString) bool {
	inReplyTo, present := delivery.InReplyToDelivery()
	return stored.Valid == present && (!present || stored.String == inReplyTo.String())
}

func newDeliveryEvidence(role, direction, routeID, state string, capturedAt time.Time,
	delivery agency.PeerDelivery,
) (deliveryEvidence, error) {
	origin := delivery.OriginEvent()
	value := deliveryEvidence{
		Node: role, Direction: direction, ID: delivery.ID().String(), RouteID: routeID,
		State: state, CapturedAt: capturedAt, EnvelopeDigest: delivery.EnvelopeDigest().String(),
		OriginEventID: origin.ID().String(), OriginEventDigest: origin.Digest().String(),
		OriginSequence: delivery.OriginSequence(), OriginAcceptedAt: delivery.OriginAcceptedAt(),
		OriginSource:      delivery.OriginSource().String(),
		OriginConsequence: delivery.OriginConsequence().String(),
		OriginTargetCount: delivery.OriginTargetCount(), OriginCausalDepth: delivery.CausalDepth(),
		OriginSemanticKind: delivery.Kind().String(), OriginPayloadBytes: len([]byte(delivery.Payload().String())),
	}
	for _, artifact := range delivery.Artifacts() {
		value.OriginArtifacts = append(value.OriginArtifacts, artifact.String())
	}
	for _, causal := range delivery.OriginCausation() {
		value.OriginCausation = append(value.OriginCausation,
			eventRefWire{ID: causal.ID().String(), Digest: causal.Digest().String()})
	}
	if correlation, present := delivery.OriginCorrelation(); present {
		value.OriginCorrelation = &eventRefWire{ID: correlation.ID().String(),
			Digest: correlation.Digest().String()}
	}
	if inReplyTo, present := delivery.InReplyToDelivery(); present {
		value.InReplyToDeliveryID = inReplyTo.String()
	}
	if _, err := agency.NewRouteID(routeID); err != nil {
		return deliveryEvidence{}, err
	}
	return value, nil
}

func validateInboxLocalEvent(role string, localEventID sql.NullString, value deliveryEvidence,
	events map[string]eventEvidence,
) (deliveryEvidence, bool, error) {
	if !localEventID.Valid {
		if value.Accepted {
			return deliveryEvidence{}, false, fmt.Errorf("%s accepted inbox Receipt has no local Event", role)
		}
		return value, false, nil
	}
	local, exists := events[localEventID.String]
	if !exists || !value.Accepted || value.LocalEventID != local.ID ||
		value.LocalEventDigest != local.Digest {
		return deliveryEvidence{}, false,
			fmt.Errorf("%s inbox local Event differs from accepted peer Receipt", role)
	}
	return value, true, nil
}

func parseStoredDelivery(routeID, id, digest string, canonical []byte) (agency.PeerDelivery, error) {
	route, err := agency.NewRouteID(routeID)
	if err != nil {
		return agency.PeerDelivery{}, err
	}
	parsed, err := agency.ParsePeerDeliveryCanonicalJSON(canonical, route)
	if err != nil {
		return agency.PeerDelivery{}, err
	}
	delivery := parsed.Delivery()
	if delivery.ID().String() != id || delivery.EnvelopeDigest().String() != digest {
		return agency.PeerDelivery{}, errors.New("Delivery identity differs from canonical envelope")
	}
	return delivery, nil
}

func bindPeerReceipt(value deliveryEvidence, delivery agency.PeerDelivery,
	settledAt, receiptDigest sql.NullString, canonical []byte,
) (deliveryEvidence, error) {
	if !settledAt.Valid || !receiptDigest.Valid || len(canonical) == 0 {
		return deliveryEvidence{}, errors.New("settled Delivery has no complete Receipt")
	}
	receipt, err := agency.ParsePeerAdmissionReceiptCanonicalJSON(canonical, delivery)
	if err != nil || receipt.Digest().String() != receiptDigest.String {
		return deliveryEvidence{}, errors.New("peer admission Receipt is not canonical and bound")
	}
	value.CapturedAt, err = parseNullableStoredTime("delivery settled_at", settledAt)
	if err != nil {
		return deliveryEvidence{}, err
	}
	value.Accepted = receipt.Outcome() == agency.PeerAdmissionOutcomeAccepted
	if local, exists := receipt.LocalEvent(); exists {
		value.LocalEventID, value.LocalEventDigest = local.ID().String(), local.Digest().String()
	}
	return value, nil
}

func parseNullableStoredTime(label string, value sql.NullString) (time.Time, error) {
	if !value.Valid {
		return time.Time{}, fmt.Errorf("%s is missing", label)
	}
	return parseStoredTime(label, value.String)
}
