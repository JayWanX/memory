package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

const (
	peerEnrollmentSchema = "memory.r7.peer-enrollment"
	peerRouteDomain      = "memory.r7.peer-route.v1"
	defaultTargetAlias   = "target:default"
)

// PeerEnrollment is the bounded owner receipt for one immutable PeerRoute.
type PeerEnrollment struct {
	route     agency.RouteID
	alias     agency.OpaqueHandle
	peer      agency.OpaqueHandle
	canonical []byte
}

type peerEnrollmentWire struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	Status  string `json:"status"`
	Route   string `json:"route"`
	Alias   string `json:"alias"`
	PeerID  string `json:"peer_id"`
}

type peerEnrollmentRequest struct {
	alias agency.OpaqueHandle
	card  PeerCard
}

func (result PeerEnrollment) RouteID() agency.RouteID           { return result.route }
func (result PeerEnrollment) PublicAlias() agency.OpaqueHandle  { return result.alias }
func (result PeerEnrollment) RemotePeerID() agency.OpaqueHandle { return result.peer }
func (result PeerEnrollment) CanonicalJSON() []byte {
	return append([]byte(nil), result.canonical...)
}

// EnrollPeer turns one owner-confirmed Card into the existing immutable
// PeerRoute authority. It is offline-only because authority has one writer.
func EnrollPeer(ctx context.Context, stateDirectory, publicAlias string,
	card PeerCard,
) (result PeerEnrollment, err error) {
	if ctx == nil {
		return PeerEnrollment{}, fmt.Errorf("%w: context is required", ErrPeerSetup)
	}
	if err := ctx.Err(); err != nil {
		return PeerEnrollment{}, err
	}
	request, err := newPeerEnrollmentRequest(publicAlias, card)
	if err != nil {
		return PeerEnrollment{}, err
	}
	stateIdentity, err := snapshotOwnerDirectory(stateDirectory)
	if err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	lock, err := acquireExistingProvisionLock(ctx, stateDirectory)
	if err != nil {
		return PeerEnrollment{}, err
	}
	defer func() { err = errors.Join(err, releaseProvisionLock(lock)) }()
	if err := verifyOwnerDirectoryIdentity(stateDirectory, stateIdentity); err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	return enrollPeerLocked(ctx, stateDirectory, request)
}

func newPeerEnrollmentRequest(publicAlias string, card PeerCard) (peerEnrollmentRequest, error) {
	alias, err := agency.NewOpaqueHandle(publicAlias)
	if err != nil {
		return peerEnrollmentRequest{}, fmt.Errorf("%w: invalid public alias", ErrPeerSetup)
	}
	if _, err := agency.AliasTarget(alias); err != nil {
		return peerEnrollmentRequest{}, fmt.Errorf("%w: invalid public alias", ErrPeerSetup)
	}
	parsed, err := ParsePeerCardCanonicalJSON(card.CanonicalJSON())
	if err != nil {
		return peerEnrollmentRequest{}, err
	}
	return peerEnrollmentRequest{alias: alias, card: parsed}, nil
}

func enrollPeerLocked(ctx context.Context, stateDirectory string,
	request peerEnrollmentRequest,
) (result PeerEnrollment, err error) {
	if err := requireProvisionedLayout(stateDirectory); err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	if _, present, err := readExchangeConfigLocked(stateDirectory); err != nil || !present {
		return PeerEnrollment{}, errors.Join(
			fmt.Errorf("%w: peer prepare must commit exchange configuration first", ErrPeerSetup), err)
	}
	localIdentity, err := loadTransportIdentity(stateDirectory)
	if err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	if localIdentity.projection.PeerID() == request.card.peerID ||
		bytes.Equal(localIdentity.projection.PublicKey(), request.card.publicKey) {
		return PeerEnrollment{}, fmt.Errorf("%w: cannot enroll the local node", ErrPeerSetup)
	}
	principal, err := DefaultAgentPrincipal(localIdentity.projection.PublicKey())
	if err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	routeID, err := derivePeerRouteID(localIdentity.projection.PeerID(), request.card.peerID)
	if err != nil {
		return PeerEnrollment{}, err
	}
	targetAlias, err := agency.NewOpaqueHandle(defaultTargetAlias)
	if err != nil {
		return PeerEnrollment{}, err
	}
	objects, err := artifact.OpenExisting(filepath.Join(stateDirectory, "objects", "sha256"))
	if err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: open Artifact store: %v", ErrPeerSetup, err)
	}
	store, err := authority.OpenExistingWithArtifactVerifier(ctx,
		filepath.Join(stateDirectory, authorityFileName), objects)
	if err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: offline authority is required: %w", ErrPeerSetup, err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	if err := store.RequireProvisionedPrincipalShape(ctx, principal); err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: %v", ErrPeerSetup, err)
	}
	if _, err := store.EnrollPeerRoute(ctx, authority.PeerRouteSpec{
		RouteID: routeID, PublicAlias: request.alias, RemotePeerID: request.card.peerID,
		RemotePublicKey: request.card.PublicKey(), TransportAddress: request.card.address,
		RemoteTargetAlias: targetAlias, InboundTargetAlias: targetAlias,
		LocalTargetPrincipal: principal,
	}); err != nil {
		return PeerEnrollment{}, fmt.Errorf("%w: enroll route: %v", ErrPeerSetup, err)
	}
	wire := peerEnrollmentWire{Schema: peerEnrollmentSchema, Version: peerSetupVersion,
		Status: "enrolled", Route: routeID.String(), Alias: request.alias.String(),
		PeerID: request.card.peerID.String()}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return PeerEnrollment{}, err
	}
	return PeerEnrollment{route: routeID, alias: request.alias, peer: request.card.peerID,
		canonical: canonical}, nil
}

func derivePeerRouteID(left, right agency.OpaqueHandle) (agency.RouteID, error) {
	if left.IsZero() || right.IsZero() || left == right {
		return agency.RouteID{}, fmt.Errorf("%w: two distinct Peer identities are required", ErrPeerSetup)
	}
	peers := []string{left.String(), right.String()}
	sort.Strings(peers)
	digest := agency.Sum([]byte(peerRouteDomain + "\x00" + peers[0] + "\x00" + peers[1]))
	return agency.NewRouteID("route:" + strings.TrimPrefix(digest.String(), "sha256:"))
}
