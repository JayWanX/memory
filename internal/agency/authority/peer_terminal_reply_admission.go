package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

const MaxTerminalObservationsPerAnchor = 64

type terminalReplyMatch struct {
	outbound agency.DeliveryID
	anchor   agency.HandlingID
	root     agency.EventRef
}

// matchOpenTerminalReplyAnchorTx corroborates a signed terminal candidate
// against the exact outbound Delivery and authority frozen when that Delivery
// was accepted locally. The peer cannot choose the route, root, local
// Principal, or open anchor by semantic text. A match reserves no state: the
// accepted inbox row and Event are committed atomically by the caller.
func matchOpenTerminalReplyAnchorTx(ctx context.Context, tx *sql.Tx,
	verified agency.VerifiedPeerDelivery, inboundRoute agency.RouteID,
) (terminalReplyMatch, bool, error) {
	delivery := verified.Delivery()
	outbound, hasOutbound := delivery.InReplyToDelivery()
	root, hasRoot := delivery.OriginCorrelation()
	if !hasOutbound || !hasRoot || inboundRoute.IsZero() || verified.LocalTarget().IsZero() {
		return terminalReplyMatch{}, false, nil
	}

	var anchorValue, rootIDValue, rootDigestValue string
	err := tx.QueryRowContext(ctx, `SELECT outbound.reply_anchor_handling_id,
		outbound.expected_reply_root_event_id, outbound.expected_reply_root_event_digest
		FROM peer_outbox outbound
		JOIN handlings anchor ON anchor.handling_id = outbound.reply_anchor_handling_id
		WHERE outbound.delivery_id = ? AND outbound.route_id = ?
		AND outbound.expected_reply_root_event_id = ?
		AND outbound.expected_reply_root_event_digest = ?
		AND anchor.target_principal_id = ? AND anchor.state = 'open'`,
		outbound.String(), inboundRoute.String(), root.ID().String(), root.Digest().String(),
		verified.LocalTarget().String()).Scan(&anchorValue, &rootIDValue, &rootDigestValue)
	if errors.Is(err, sql.ErrNoRows) {
		return terminalReplyMatch{}, false, nil
	}
	if err != nil {
		return terminalReplyMatch{}, false,
			fmt.Errorf("admit terminal PeerDelivery: inspect exact reply binding: %w", err)
	}
	anchor, err := agency.NewHandlingID(anchorValue)
	if err != nil || rootIDValue != root.ID().String() || rootDigestValue != root.Digest().String() {
		return terminalReplyMatch{}, false,
			errors.New("admit terminal PeerDelivery: corrupt reply binding")
	}

	var alreadyAccepted int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM peer_inbox
		WHERE in_reply_to_delivery_id = ? AND local_event_id IS NOT NULL)`,
		outbound.String()).Scan(&alreadyAccepted); err != nil {
		return terminalReplyMatch{}, false,
			fmt.Errorf("admit terminal PeerDelivery: inspect accepted reply: %w", err)
	}
	if alreadyAccepted != 0 {
		return terminalReplyMatch{}, false, nil
	}
	revision, err := terminalObservationRevisionTx(ctx, tx, anchor)
	if err != nil {
		return terminalReplyMatch{}, false, err
	}
	if revision >= MaxTerminalObservationsPerAnchor {
		return terminalReplyMatch{}, false, nil
	}
	return terminalReplyMatch{outbound: outbound, anchor: anchor, root: root}, true, nil
}

// terminalObservationRevisionTx is the immutable accepted-observation count
// linked to one anchor. It is both a bounded projection cursor and a subject
// read-set revision; it never changes Handling state by itself.
func terminalObservationRevisionTx(ctx context.Context, tx *sql.Tx,
	anchor agency.HandlingID,
) (uint64, error) {
	if anchor.IsZero() {
		return 0, errors.New("terminal observation revision: anchor is required")
	}
	var count uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_inbox inbox
		JOIN peer_outbox outbound
		ON outbound.delivery_id = inbox.in_reply_to_delivery_id
		WHERE outbound.reply_anchor_handling_id = ?
		AND inbox.local_event_id IS NOT NULL`, anchor.String()).Scan(&count); err != nil {
		return 0, fmt.Errorf("terminal observation revision: count accepted replies: %w", err)
	}
	if count > MaxTerminalObservationsPerAnchor {
		return 0, errors.New("terminal observation revision: durable bound violated")
	}
	return count, nil
}
