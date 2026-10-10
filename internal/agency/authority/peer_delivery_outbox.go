package authority

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	MaxPendingPeerDeliveries = 64
	PeerDeliveryTTL          = time.Hour
)

// PendingPeerDelivery is the owner-private durable transmission projection.
// The Agent sees only the route's public alias in its View; transport receives
// this exact canonical candidate after local admission commits.
type PendingPeerDelivery struct {
	route     PeerRouteProjection
	delivery  agency.PeerDelivery
	createdAt time.Time
}

func (pending PendingPeerDelivery) Route() PeerRouteProjection { return pending.route }
func (pending PendingPeerDelivery) Delivery() agency.PeerDelivery {
	return pending.delivery
}
func (pending PendingPeerDelivery) CreatedAt() time.Time { return pending.createdAt }

func localTargetCount(targets []agency.ResolvedTarget) int {
	count := 0
	for _, target := range targets {
		if target.Destination() == agency.TargetDestinationLocal {
			count++
		}
	}
	return count
}

func remoteTargetCount(targets []agency.ResolvedTarget) int {
	count := 0
	for _, target := range targets {
		if target.Destination() == agency.TargetDestinationRemote {
			count++
		}
	}
	return count
}

func validatePeerDeliveryCapacityTx(ctx context.Context, tx *sql.Tx,
	request agency.BoundIntent, now time.Time,
) (*admissionRejection, error) {
	remote := remoteTargetCount(request.Targets())
	if remote == 0 {
		return nil, nil
	}
	depth, err := deriveLocalEventCausalDepthTx(ctx, tx, request)
	if err != nil {
		return nil, err
	}
	if depth >= agency.MaxPeerCausalDepth {
		return reject(rejectionResourceBound, "peer causal depth bound reached"), nil
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_outbox
		WHERE state = 'pending' AND expires_at > ?`, formatTime(now)).Scan(&pending); err != nil {
		return nil, fmt.Errorf("admit Intent: count pending PeerDelivery: %w", err)
	}
	if pending+remote > MaxPendingPeerDeliveries {
		return reject(rejectionResourceBound, "pending PeerDelivery bound reached"), nil
	}
	return nil, nil
}

func insertPeerDeliveriesTx(ctx context.Context, tx *sql.Tx, event agency.Event,
	handlingIDs []agency.HandlingID, now time.Time,
) error {
	reply, err := outboundReplyBinding(event, handlingIDs)
	if err != nil {
		return err
	}
	for _, target := range event.Targets() {
		if target.Destination() != agency.TargetDestinationRemote {
			continue
		}
		route, found, err := peerRouteByIDTx(ctx, tx, target.RemoteRoute())
		if err != nil {
			return fmt.Errorf("admit Intent: inspect outbound peer route: %w", err)
		}
		if !found || !route.Active() || route.RemoteTargetAlias() != target.RemoteAlias() {
			return errors.New("admit Intent: outbound peer route changed after validation")
		}
		correlation, _ := event.Correlation()
		inReplyToDelivery, _ := event.InReplyToDelivery()
		delivery, err := agency.NewPeerDelivery(route.RouteID(), agency.PeerDeliverySpec{
			OriginEvent:       event.Ref(),
			OriginSequence:    event.OriginSequence(),
			OriginAcceptedAt:  event.AcceptedAt(),
			OriginSource:      event.Source(),
			OriginConsequence: event.Consequence(),
			OriginTargetCount: uint8(len(event.Targets())),
			OriginCausation:   event.Causation(),
			OriginCorrelation: correlation,
			InReplyToDelivery: inReplyToDelivery,
			TargetAlias:       route.RemoteTargetAlias(),
			Kind:              event.Kind(),
			Payload:           event.Payload(),
			Artifacts:         event.Artifacts(),
			CausalDepth:       event.CausalDepth() + 1,
			ExpiresAt:         now.Add(PeerDeliveryTTL),
		})
		if err != nil {
			return fmt.Errorf("admit Intent: construct PeerDelivery: %w", err)
		}
		anchor, rootID, rootDigest := reply.databaseValues()
		if _, err := tx.ExecContext(ctx, `INSERT INTO peer_outbox(
			delivery_id, route_id, origin_event_id, reply_anchor_handling_id,
			expected_reply_root_event_id, expected_reply_root_event_digest,
			envelope_digest, delivery_json, state, expires_at, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
			delivery.ID().String(), route.RouteID().String(), event.ID().String(),
			anchor, rootID, rootDigest, delivery.EnvelopeDigest().String(), delivery.CanonicalJSON(),
			formatTime(delivery.ExpiresAt()), formatTime(now)); err != nil {
			return fmt.Errorf("admit Intent: persist PeerDelivery: %w", err)
		}
	}
	return nil
}

type peerReplyBinding struct {
	anchor agency.HandlingID
	root   agency.EventRef
}

func (binding peerReplyBinding) databaseValues() (any, any, any) {
	if binding.anchor.IsZero() || binding.root.IsZero() {
		return nil, nil, nil
	}
	return binding.anchor.String(), binding.root.ID().String(), binding.root.Digest().String()
}

// outboundReplyBinding records the local authority against which a later
// terminal candidate may be re-admitted. It never infers that authority from
// semantic kind or from a Handling's historical creation Event.
func outboundReplyBinding(event agency.Event,
	handlingIDs []agency.HandlingID,
) (peerReplyBinding, error) {
	if remoteTargetCount(event.Targets()) == 0 || isExactTerminalReplyEvent(event) {
		return peerReplyBinding{}, nil
	}
	root := event.Ref()
	if correlation, present := event.Correlation(); present {
		root = correlation
	}
	if event.Consequence() == agency.ConsequenceAdvanceHandling {
		subject, present := event.Subject()
		if !present {
			return peerReplyBinding{}, errors.New("admit Intent: remote advance lacks reply anchor")
		}
		return peerReplyBinding{anchor: subject.HandlingID(), root: root}, nil
	}
	anchor, err := sourceLocalSuccessor(event, handlingIDs)
	if err != nil {
		return peerReplyBinding{}, err
	}
	return peerReplyBinding{anchor: anchor, root: root}, nil
}

func isExactTerminalReplyEvent(event agency.Event) bool {
	if len(event.Targets()) != 1 || event.Targets()[0].Destination() != agency.TargetDestinationRemote {
		return false
	}
	if _, present := event.InReplyToDelivery(); !present {
		return false
	}
	switch event.Consequence() {
	case agency.ConsequenceResolveCompleted, agency.ConsequenceResolveDeclined,
		agency.ConsequenceResolveUnresolved:
		_, correlated := event.Correlation()
		return correlated
	default:
		return false
	}
}

func sourceLocalSuccessor(event agency.Event,
	handlingIDs []agency.HandlingID,
) (agency.HandlingID, error) {
	var result agency.HandlingID
	localIndex := 0
	for _, target := range event.Targets() {
		if target.Destination() != agency.TargetDestinationLocal {
			continue
		}
		if target.LocalPrincipal() == event.Source() {
			if localIndex >= len(handlingIDs) {
				return agency.HandlingID{}, errors.New("admit Intent: reply anchor cardinality mismatch")
			}
			result = handlingIDs[localIndex]
		}
		localIndex++
	}
	if result.IsZero() {
		return agency.HandlingID{}, errors.New("admit Intent: remote Event lacks source-local reply anchor")
	}
	if localIndex != len(handlingIDs) {
		return agency.HandlingID{}, errors.New("admit Intent: reply anchor cardinality mismatch")
	}
	return result, nil
}

type pendingPeerDeliveryRow struct {
	deliveryID     string
	routeID        string
	originEventID  string
	envelopeDigest string
	deliveryJSON   []byte
	expiresAt      string
	createdAt      string
}

// PendingPeerDeliveries expires stale occupancy and returns at most limit
// exact candidates. It performs no network I/O and never treats delivery as
// remote admission.
func (s *Store) PendingPeerDeliveries(ctx context.Context,
	limit int,
) ([]PendingPeerDelivery, error) {
	if ctx == nil || limit < 1 || limit > MaxPendingPeerDeliveries {
		return nil, errors.New("project pending PeerDelivery: bounded positive limit is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("project pending PeerDelivery: begin: %w", err)
	}
	defer tx.Rollback()
	if err := expirePeerOutboxTx(ctx, tx, now); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT delivery_id, route_id, origin_event_id,
		envelope_digest, delivery_json, expires_at, created_at FROM peer_outbox
		WHERE state = 'pending' ORDER BY created_at, delivery_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("project pending PeerDelivery: query: %w", err)
	}
	var raw []pendingPeerDeliveryRow
	for rows.Next() {
		var row pendingPeerDeliveryRow
		if err := rows.Scan(&row.deliveryID, &row.routeID, &row.originEventID,
			&row.envelopeDigest, &row.deliveryJSON, &row.expiresAt, &row.createdAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("project pending PeerDelivery: scan: %w", err)
		}
		raw = append(raw, row)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("project pending PeerDelivery: close rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project pending PeerDelivery: iterate: %w", err)
	}
	result := make([]PendingPeerDelivery, 0, len(raw))
	for _, row := range raw {
		pending, err := parsePendingPeerDeliveryTx(ctx, tx, row)
		if err != nil {
			return nil, err
		}
		result = append(result, pending)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("project pending PeerDelivery: commit expiry: %w", err)
	}
	return result, nil
}

func parsePendingPeerDeliveryTx(ctx context.Context, tx *sql.Tx,
	row pendingPeerDeliveryRow,
) (PendingPeerDelivery, error) {
	pending, err := parsePeerDeliveryRowTx(ctx, tx, row)
	if err != nil {
		return PendingPeerDelivery{}, err
	}
	if !pending.route.Active() {
		return PendingPeerDelivery{}, errors.New("project pending PeerDelivery: unavailable route authority")
	}
	return pending, nil
}

func parsePeerDeliveryRowTx(ctx context.Context, tx *sql.Tx,
	row pendingPeerDeliveryRow,
) (PendingPeerDelivery, error) {
	routeID, err := agency.NewRouteID(row.routeID)
	if err != nil {
		return PendingPeerDelivery{}, errors.New("inspect PeerDelivery: corrupt RouteID")
	}
	route, found, err := peerRouteByIDTx(ctx, tx, routeID)
	if err != nil || !found {
		return PendingPeerDelivery{}, errors.New("inspect PeerDelivery: unavailable route")
	}
	parsed, err := agency.ParsePeerDeliveryCanonicalJSON(row.deliveryJSON, routeID)
	if err != nil {
		return PendingPeerDelivery{}, errors.New("inspect PeerDelivery: corrupt envelope")
	}
	delivery := parsed.Delivery()
	origin, _, _, _, err := loadStoredEventTx(ctx, tx, row.originEventID)
	if err != nil || origin != delivery.OriginEvent() {
		return PendingPeerDelivery{}, errors.New("inspect PeerDelivery: origin Event diverges")
	}
	createdAt, createdErr := parseTime(row.createdAt)
	expiresAt, expiresErr := parseTime(row.expiresAt)
	if createdErr != nil || expiresErr != nil || row.deliveryID != delivery.ID().String() ||
		row.envelopeDigest != delivery.EnvelopeDigest().String() ||
		row.originEventID != delivery.OriginEvent().ID().String() || !expiresAt.Equal(delivery.ExpiresAt()) {
		return PendingPeerDelivery{}, errors.New("inspect PeerDelivery: durable columns diverge")
	}
	return PendingPeerDelivery{route: route, delivery: delivery, createdAt: createdAt}, nil
}

func expirePeerOutboxTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE peer_outbox SET state = 'expired', settled_at = ?
		WHERE state = 'pending' AND expires_at <= ?`, formatTime(now), formatTime(now)); err != nil {
		return fmt.Errorf("expire PeerDelivery: %w", err)
	}
	return nil
}

// AuthorizePeerArtifact checks only the exact authenticated route, live
// Delivery envelope, and referenced digest. CAS byte reads remain outside the
// authority Store and must be hashed by the caller before response.
func (s *Store) AuthorizePeerArtifact(ctx context.Context, remotePeer agency.OpaqueHandle,
	deliveryID agency.DeliveryID, envelope agency.Digest, object agency.Digest,
) (bool, error) {
	if ctx == nil || remotePeer.IsZero() || deliveryID.IsZero() || envelope.IsZero() || object.IsZero() {
		return false, errors.New("authorize peer Artifact: exact request authority is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return false, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return false, err
	}
	var routeValue string
	var canonical []byte
	err = s.db.QueryRowContext(ctx, `SELECT o.route_id, o.delivery_json FROM peer_outbox o
		JOIN peer_routes r ON r.route_id = o.route_id
		WHERE o.delivery_id = ? AND o.envelope_digest = ? AND o.state = 'pending'
		AND o.expires_at > ? AND r.state = 'active' AND r.remote_peer_id = ?`,
		deliveryID.String(), envelope.String(), formatTime(now), remotePeer.String()).
		Scan(&routeValue, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("authorize peer Artifact: inspect outbox: %w", err)
	}
	routeID, err := agency.NewRouteID(routeValue)
	if err != nil {
		return false, errors.New("authorize peer Artifact: corrupt RouteID")
	}
	parsed, err := agency.ParsePeerDeliveryCanonicalJSON(canonical, routeID)
	if err != nil || parsed.ID() != deliveryID || parsed.EnvelopeDigest() != envelope {
		return false, errors.New("authorize peer Artifact: corrupt outbox envelope")
	}
	for _, digest := range parsed.Delivery().Artifacts() {
		if digest == object {
			return true, nil
		}
	}
	return false, nil
}

func verifyPeerSignature(key []byte, message, signature []byte) bool {
	return len(key) == ed25519.PublicKeySize && len(signature) == ed25519.SignatureSize &&
		ed25519.Verify(ed25519.PublicKey(key), message, signature)
}
