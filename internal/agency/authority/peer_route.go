package authority

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	MaxPeerRoutePublicKeyBytes   = ed25519.PublicKeySize
	MaxPeerTransportAddressBytes = 512
	MaxActivePeerRoutes          = 8
	peerRouteSourceDomain        = "memory.r7.peer-route.source-principal.v1"
)

// PeerRouteSpec is setup authority for one pre-agreed, direction-local view
// of a shared RouteID. RouteID is supplied by the owner and must be identical
// at both peers. All other fields are immutable after first enrollment.
type PeerRouteSpec struct {
	RouteID              agency.RouteID
	PublicAlias          agency.OpaqueHandle
	RemotePeerID         agency.OpaqueHandle
	RemotePublicKey      []byte
	TransportAddress     string
	RemoteTargetAlias    agency.OpaqueHandle
	InboundTargetAlias   agency.OpaqueHandle
	LocalTargetPrincipal agency.AgentPrincipalID
}

// PeerRouteProjection is the owner-private durable route projection. The
// Agent View receives PublicAlias only; key, identities, Principals, RouteID,
// and the remote alias never cross that seam.
type PeerRouteProjection struct {
	routeID              agency.RouteID
	publicAlias          agency.OpaqueHandle
	remotePeerID         agency.OpaqueHandle
	remotePublicKey      []byte
	transportAddress     string
	remoteTargetAlias    agency.OpaqueHandle
	inboundTargetAlias   agency.OpaqueHandle
	localTargetPrincipal agency.AgentPrincipalID
	surrogateSource      agency.AgentPrincipalID
	active               bool
	enrolledAt           time.Time
	revokedAt            time.Time
}

func (route PeerRouteProjection) RouteID() agency.RouteID          { return route.routeID }
func (route PeerRouteProjection) PublicAlias() agency.OpaqueHandle { return route.publicAlias }
func (route PeerRouteProjection) RemotePeerID() agency.OpaqueHandle {
	return route.remotePeerID
}
func (route PeerRouteProjection) TransportAddress() string { return route.transportAddress }
func (route PeerRouteProjection) RemoteTargetAlias() agency.OpaqueHandle {
	return route.remoteTargetAlias
}
func (route PeerRouteProjection) InboundTargetAlias() agency.OpaqueHandle {
	return route.inboundTargetAlias
}
func (route PeerRouteProjection) LocalTargetPrincipal() agency.AgentPrincipalID {
	return route.localTargetPrincipal
}
func (route PeerRouteProjection) SurrogateSourcePrincipal() agency.AgentPrincipalID {
	return route.surrogateSource
}
func (route PeerRouteProjection) RemotePublicKey() []byte {
	return append([]byte(nil), route.remotePublicKey...)
}
func (route PeerRouteProjection) Active() bool          { return route.active }
func (route PeerRouteProjection) EnrolledAt() time.Time { return route.enrolledAt }
func (route PeerRouteProjection) RevokedAt() time.Time  { return route.revokedAt }

// EnrollPeerRoute is an owner/setup operation, not an Agent Intent. Exact
// replay is idempotent. A RouteID, alias, identity, key, or Principal can never
// be rebound, and a revoked route can never be reactivated.
func (s *Store) EnrollPeerRoute(ctx context.Context, spec PeerRouteSpec) (PeerRouteProjection, error) {
	if ctx == nil {
		return PeerRouteProjection{}, errors.New("enroll peer route: nil context")
	}
	spec.RemotePublicKey = append([]byte(nil), spec.RemotePublicKey...)
	if err := validatePeerRouteSpec(spec); err != nil {
		return PeerRouteProjection{}, err
	}
	surrogate, err := derivePeerRouteSourcePrincipal(spec)
	if err != nil {
		return PeerRouteProjection{}, err
	}
	now, err := s.trustedNow()
	if err != nil {
		return PeerRouteProjection{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return PeerRouteProjection{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PeerRouteProjection{}, fmt.Errorf("enroll peer route: begin: %w", err)
	}
	defer tx.Rollback()
	if current, found, err := peerRouteByIDTx(ctx, tx, spec.RouteID); err != nil {
		return PeerRouteProjection{}, err
	} else if found {
		if !peerRouteMatchesSpec(current, spec, surrogate) {
			return PeerRouteProjection{}, ErrPeerRouteConflict
		}
		if !current.Active() {
			return PeerRouteProjection{}, ErrPeerRouteRevoked
		}
		return current, nil
	}
	if err := requirePeerRouteInputsAvailableTx(ctx, tx, spec, surrogate); err != nil {
		return PeerRouteProjection{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO principals(principal_id, created_at)
		VALUES(?, ?)`, surrogate.String(), formatTime(now)); err != nil {
		return PeerRouteProjection{}, fmt.Errorf("enroll peer route: persist surrogate Principal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_routes(
		route_id, public_alias, remote_peer_id, remote_public_key, transport_address,
		remote_target_alias, inbound_target_alias, local_target_principal_id,
		surrogate_source_principal_id, state, enrolled_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?)`, spec.RouteID.String(), spec.PublicAlias.String(),
		spec.RemotePeerID.String(), append([]byte(nil), spec.RemotePublicKey...),
		spec.TransportAddress, spec.RemoteTargetAlias.String(), spec.InboundTargetAlias.String(),
		spec.LocalTargetPrincipal.String(), surrogate.String(),
		formatTime(now)); err != nil {
		return PeerRouteProjection{}, fmt.Errorf("enroll peer route: persist: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PeerRouteProjection{}, fmt.Errorf("enroll peer route: commit: %w", err)
	}
	return peerRouteProjection(spec, surrogate, true, now, time.Time{}), nil
}

// RevokePeerRoute irreversibly removes one route from future Views and fresh
// admission. Exact repeated revocation returns the same durable projection.
func (s *Store) RevokePeerRoute(ctx context.Context, routeID agency.RouteID) (PeerRouteProjection, error) {
	if ctx == nil || routeID.IsZero() {
		return PeerRouteProjection{}, errors.New("revoke peer route: RouteID is required")
	}
	now, err := s.trustedNow()
	if err != nil {
		return PeerRouteProjection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return PeerRouteProjection{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PeerRouteProjection{}, fmt.Errorf("revoke peer route: begin: %w", err)
	}
	defer tx.Rollback()
	current, found, err := peerRouteByIDTx(ctx, tx, routeID)
	if err != nil {
		return PeerRouteProjection{}, err
	}
	if !found {
		return PeerRouteProjection{}, ErrPeerRouteUnavailable
	}
	if !current.Active() {
		return current, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE peer_routes SET state = 'revoked', revoked_at = ?
		WHERE route_id = ? AND state = 'active' AND revoked_at IS NULL`, formatTime(now), routeID.String())
	if err != nil {
		return PeerRouteProjection{}, fmt.Errorf("revoke peer route: persist: %w", err)
	}
	if err := requireOneRow(result, "peer route revoke"); err != nil {
		return PeerRouteProjection{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peer_outbox SET state = 'expired', settled_at = ?
		WHERE route_id = ? AND state = 'pending'`, formatTime(now), routeID.String()); err != nil {
		return PeerRouteProjection{}, fmt.Errorf("revoke peer route: expire pending deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PeerRouteProjection{}, fmt.Errorf("revoke peer route: commit: %w", err)
	}
	current.active, current.revokedAt = false, now
	return current, nil
}

// PeerRoutes returns the complete owner-private route projection, including
// revoked rows, in stable RouteID order.
func (s *Store) PeerRoutes(ctx context.Context) ([]PeerRouteProjection, error) {
	if ctx == nil {
		return nil, errors.New("project peer routes: nil context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, peerRouteColumns+` ORDER BY route_id`)
	if err != nil {
		return nil, fmt.Errorf("project peer routes: query: %w", err)
	}
	defer rows.Close()
	var routes []PeerRouteProjection
	for rows.Next() {
		route, err := scanPeerRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project peer routes: iterate: %w", err)
	}
	return routes, nil
}

const peerRouteColumns = `SELECT route_id, public_alias, remote_peer_id, remote_public_key,
	transport_address, remote_target_alias, inbound_target_alias,
	local_target_principal_id, surrogate_source_principal_id,
	state, enrolled_at, revoked_at FROM peer_routes`

func validatePeerRouteSpec(spec PeerRouteSpec) error {
	if spec.RouteID.IsZero() || spec.PublicAlias.IsZero() || spec.RemotePeerID.IsZero() ||
		spec.RemoteTargetAlias.IsZero() || spec.InboundTargetAlias.IsZero() ||
		spec.LocalTargetPrincipal.IsZero() {
		return errors.New("enroll peer route: complete route authority is required")
	}
	if _, err := agency.AliasTarget(spec.PublicAlias); err != nil {
		return fmt.Errorf("enroll peer route: public alias: %w", err)
	}
	if _, err := agency.AliasTarget(spec.RemoteTargetAlias); err != nil {
		return fmt.Errorf("enroll peer route: remote target alias: %w", err)
	}
	if _, err := agency.AliasTarget(spec.InboundTargetAlias); err != nil {
		return fmt.Errorf("enroll peer route: inbound target alias: %w", err)
	}
	if len(spec.RemotePublicKey) != MaxPeerRoutePublicKeyBytes {
		return fmt.Errorf("enroll peer route: remote Ed25519 public key must be %d bytes",
			MaxPeerRoutePublicKeyBytes)
	}
	address, err := agency.NewSemanticPayload(spec.TransportAddress)
	if err != nil || address.String() == "" || len(spec.TransportAddress) > MaxPeerTransportAddressBytes {
		return fmt.Errorf("enroll peer route: transport address must be 1..%d opaque UTF-8 bytes",
			MaxPeerTransportAddressBytes)
	}
	return nil
}

func derivePeerRouteSourcePrincipal(spec PeerRouteSpec) (agency.AgentPrincipalID, error) {
	if err := validatePeerRouteSpec(spec); err != nil {
		return agency.AgentPrincipalID{}, err
	}
	keyDigest := agency.Sum(spec.RemotePublicKey)
	digest := agency.Sum([]byte(peerRouteSourceDomain + "\x00" + spec.RouteID.String() + "\x00" +
		spec.RemotePeerID.String() + "\x00" + keyDigest.String()))
	return agency.NewAgentPrincipalID("agent:peer-route:" + strings.TrimPrefix(digest.String(), "sha256:"))
}

func requirePeerRouteInputsAvailableTx(ctx context.Context, tx *sql.Tx, spec PeerRouteSpec,
	surrogate agency.AgentPrincipalID,
) error {
	var principalExists, surrogateExists, aliasExists, peerExists, activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM principals WHERE principal_id = ?),
		EXISTS(SELECT 1 FROM principals WHERE principal_id = ?),
		EXISTS(SELECT 1 FROM peer_routes WHERE public_alias = ?),
		EXISTS(SELECT 1 FROM peer_routes WHERE remote_peer_id = ?),
		(SELECT COUNT(*) FROM peer_routes WHERE state = 'active')`,
		spec.LocalTargetPrincipal.String(), surrogate.String(), spec.PublicAlias.String(),
		spec.RemotePeerID.String()).
		Scan(&principalExists, &surrogateExists, &aliasExists, &peerExists, &activeCount); err != nil {
		return fmt.Errorf("enroll peer route: inspect authority: %w", err)
	}
	if principalExists != 1 {
		return ErrPrincipalUnavailable
	}
	if surrogateExists != 0 || aliasExists != 0 || peerExists != 0 {
		return ErrPeerRouteConflict
	}
	if activeCount >= MaxActivePeerRoutes {
		return fmt.Errorf("enroll peer route: active route bound %d reached", MaxActivePeerRoutes)
	}
	return nil
}

func peerRouteByIDTx(ctx context.Context, tx *sql.Tx,
	routeID agency.RouteID,
) (PeerRouteProjection, bool, error) {
	row := tx.QueryRowContext(ctx, peerRouteColumns+` WHERE route_id = ?`, routeID.String())
	route, err := scanPeerRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PeerRouteProjection{}, false, nil
	}
	if err != nil {
		return PeerRouteProjection{}, false, err
	}
	return route, true, nil
}

func scanPeerRoute(row rowScanner) (PeerRouteProjection, error) {
	var routeValue, publicAliasValue, remotePeerValue, transportAddress, remoteAliasValue string
	var inboundAliasValue string
	var localPrincipalValue, surrogateValue, state, enrolledValue string
	var key []byte
	var revokedValue sql.NullString
	if err := row.Scan(&routeValue, &publicAliasValue, &remotePeerValue, &key, &transportAddress,
		&remoteAliasValue, &inboundAliasValue, &localPrincipalValue, &surrogateValue, &state,
		&enrolledValue, &revokedValue); err != nil {
		return PeerRouteProjection{}, err
	}
	routeID, err := agency.NewRouteID(routeValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt RouteID")
	}
	publicAlias, err := agency.NewOpaqueHandle(publicAliasValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt public alias")
	}
	remotePeer, err := agency.NewOpaqueHandle(remotePeerValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt remote peer identity")
	}
	remoteAlias, err := agency.NewOpaqueHandle(remoteAliasValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt remote target alias")
	}
	inboundAlias, err := agency.NewOpaqueHandle(inboundAliasValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt inbound target alias")
	}
	localPrincipal, err := agency.NewAgentPrincipalID(localPrincipalValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt local target Principal")
	}
	surrogate, err := agency.NewAgentPrincipalID(surrogateValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt surrogate Principal")
	}
	if len(key) != MaxPeerRoutePublicKeyBytes {
		return PeerRouteProjection{}, errors.New("peer route: corrupt remote public key")
	}
	enrolledAt, err := parseTime(enrolledValue)
	if err != nil {
		return PeerRouteProjection{}, errors.New("peer route: corrupt enrollment time")
	}
	projection := PeerRouteProjection{routeID: routeID, publicAlias: publicAlias,
		remotePeerID: remotePeer, remotePublicKey: append([]byte(nil), key...),
		transportAddress: transportAddress, remoteTargetAlias: remoteAlias,
		inboundTargetAlias: inboundAlias, localTargetPrincipal: localPrincipal,
		surrogateSource: surrogate, enrolledAt: enrolledAt}
	spec := PeerRouteSpec{RouteID: routeID, PublicAlias: publicAlias, RemotePeerID: remotePeer,
		RemotePublicKey: key, TransportAddress: transportAddress, RemoteTargetAlias: remoteAlias,
		InboundTargetAlias: inboundAlias, LocalTargetPrincipal: localPrincipal}
	expectedSurrogate, err := derivePeerRouteSourcePrincipal(spec)
	if err != nil || expectedSurrogate != surrogate {
		return PeerRouteProjection{}, errors.New("peer route: corrupt surrogate derivation")
	}
	switch {
	case state == "active" && !revokedValue.Valid:
		projection.active = true
	case state == "revoked" && revokedValue.Valid:
		projection.revokedAt, err = parseTime(revokedValue.String)
		if err != nil || projection.revokedAt.Before(enrolledAt) {
			return PeerRouteProjection{}, errors.New("peer route: corrupt revocation time")
		}
	default:
		return PeerRouteProjection{}, errors.New("peer route: corrupt lifecycle")
	}
	return projection, nil
}

func peerRouteMatchesSpec(route PeerRouteProjection, spec PeerRouteSpec,
	surrogate agency.AgentPrincipalID,
) bool {
	return route.RouteID() == spec.RouteID && route.PublicAlias() == spec.PublicAlias &&
		route.RemotePeerID() == spec.RemotePeerID && bytes.Equal(route.remotePublicKey, spec.RemotePublicKey) &&
		route.TransportAddress() == spec.TransportAddress &&
		route.RemoteTargetAlias() == spec.RemoteTargetAlias &&
		route.InboundTargetAlias() == spec.InboundTargetAlias &&
		route.LocalTargetPrincipal() == spec.LocalTargetPrincipal &&
		route.SurrogateSourcePrincipal() == surrogate
}

func peerRouteProjection(spec PeerRouteSpec, surrogate agency.AgentPrincipalID, active bool,
	enrolledAt, revokedAt time.Time,
) PeerRouteProjection {
	return PeerRouteProjection{routeID: spec.RouteID, publicAlias: spec.PublicAlias,
		remotePeerID: spec.RemotePeerID, remotePublicKey: append([]byte(nil), spec.RemotePublicKey...),
		transportAddress: spec.TransportAddress, remoteTargetAlias: spec.RemoteTargetAlias,
		inboundTargetAlias: spec.InboundTargetAlias, localTargetPrincipal: spec.LocalTargetPrincipal,
		surrogateSource: surrogate, active: active, enrolledAt: enrolledAt, revokedAt: revokedAt}
}
