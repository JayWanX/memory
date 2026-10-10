package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
	"github.com/JayWanX/memory/internal/agency/peerlink"
)

const (
	defaultExchangeInterval = time.Second
	minimumExchangeInterval = 10 * time.Millisecond
	maximumExchangeInterval = time.Minute
	defaultExchangeBatch    = 8
	maximumExchangeBatch    = 8
	maxExchangeRoutes       = 8
	peerShutdownBudget      = 5 * time.Second
)

// ExchangeOptions explicitly enables the optional peer plane. ListenAddress
// is owner configuration, never an Agent-selected target or wire identity.
type ExchangeOptions struct {
	ListenAddress string
	PollInterval  time.Duration
	BatchSize     int
}

type exchangeAuthority interface {
	StagePeerDelivery(context.Context, agency.OpaqueHandle, []byte, []byte) (
		authority.PeerAdmissionResult, error)
	AdmitPeerDelivery(context.Context, agency.DeliveryID) (authority.PeerAdmissionResult, error)
	CatalogArtifact(context.Context, authority.VerifiedArtifact) error
	AuthorizePeerArtifact(context.Context, agency.OpaqueHandle, agency.DeliveryID,
		agency.Digest, agency.Digest) (bool, error)
	PendingPeerDeliveries(context.Context, int) ([]authority.PendingPeerDelivery, error)
	SettlePeerDelivery(context.Context, agency.OpaqueHandle, agency.DeliveryID, []byte, []byte) (
		agency.PeerAdmissionReceipt, bool, error)
}

var errRemoteArtifactUnavailable = errors.New("daemon exchange: remote Artifact unavailable")

type exchangeRuntime struct {
	store         exchangeAuthority
	objects       *artifact.Store
	now           func() time.Time
	identity      peerlink.Identity
	peers         []peerlink.Peer
	peersByID     map[string]peerlink.Peer
	client        *peerlink.Client
	listenAddress string
	pollInterval  time.Duration
	batchSize     int
}

func newExchangeRuntime(ctx context.Context, stateDirectory string, store *authority.Store,
	objects *artifact.Store, now func() time.Time, options ExchangeOptions,
) (*exchangeRuntime, error) {
	if ctx == nil || store == nil || objects == nil || now == nil || options.ListenAddress == "" {
		return nil, errors.New("daemon exchange: complete owner configuration is required")
	}
	interval, batch, err := validateExchangeBounds(options.PollInterval, options.BatchSize)
	if err != nil {
		return nil, err
	}
	loaded, err := loadTransportIdentity(stateDirectory)
	if err != nil {
		return nil, err
	}
	routes, err := store.PeerRoutes(ctx)
	if err != nil {
		return nil, fmt.Errorf("daemon exchange: load routes: %w", err)
	}
	peers, peersByID, err := activeTransportPeers(routes, loaded.projection)
	if err != nil {
		return nil, err
	}
	identity := peerlink.Identity{ID: loaded.projection.PeerID(), PrivateKey: loaded.privateKey}
	client, err := peerlink.NewClient(peerlink.ClientOptions{Identity: identity, Artifacts: objects})
	if err != nil {
		return nil, fmt.Errorf("daemon exchange: create peer client: %w", err)
	}
	return &exchangeRuntime{store: store, objects: objects, now: now, identity: identity,
		peers: peers, peersByID: peersByID, client: client,
		listenAddress: options.ListenAddress, pollInterval: interval, batchSize: batch}, nil
}

func validateExchangeBounds(interval time.Duration, batch int) (time.Duration, int, error) {
	if interval == 0 {
		interval = defaultExchangeInterval
	}
	if interval < minimumExchangeInterval || interval > maximumExchangeInterval {
		return 0, 0, errors.New("daemon exchange: poll interval is outside the supported bound")
	}
	if batch == 0 {
		batch = defaultExchangeBatch
	}
	if batch < 1 || batch > maximumExchangeBatch {
		return 0, 0, errors.New("daemon exchange: batch size is outside the supported bound")
	}
	return interval, batch, nil
}

func activeTransportPeers(routes []authority.PeerRouteProjection,
	local TransportIdentity,
) ([]peerlink.Peer, map[string]peerlink.Peer, error) {
	peers := make([]peerlink.Peer, 0, len(routes))
	byID := make(map[string]peerlink.Peer, len(routes))
	for _, route := range routes {
		if !route.Active() {
			continue
		}
		if len(peers) >= maxExchangeRoutes {
			return nil, nil, fmt.Errorf("daemon exchange: active route bound %d exceeded",
				maxExchangeRoutes)
		}
		key := ed25519.PublicKey(route.RemotePublicKey())
		derived, err := derivePeerIdentity(key)
		if err != nil || derived != route.RemotePeerID() {
			return nil, nil, errors.New("daemon exchange: route Peer identity is not derived from its key")
		}
		if derived == local.PeerID() || bytes.Equal(key, local.PublicKey()) {
			return nil, nil, errors.New("daemon exchange: route cannot target the local identity")
		}
		if _, duplicate := byID[derived.String()]; duplicate {
			return nil, nil, errors.New("daemon exchange: duplicate active Peer identity")
		}
		peer := peerlink.Peer{ID: derived, PublicKey: key, Address: route.TransportAddress()}
		peers = append(peers, peer)
		byID[derived.String()] = peer
	}
	if len(peers) == 0 {
		return nil, nil, errors.New("daemon exchange: at least one active route is required")
	}
	return peers, byID, nil
}

func (exchange *exchangeRuntime) start(parent context.Context) (*exchangeSession, error) {
	if exchange == nil || parent == nil || parent.Err() != nil {
		return nil, errors.New("daemon exchange: live runtime is required")
	}
	lifetime, cancel := context.WithCancel(parent)
	server, err := peerlink.Listen(lifetime, exchange.listenAddress, peerlink.ServerOptions{
		Identity: exchange.identity, Peers: exchange.peers, Artifacts: exchange.objects,
		Delivery: exchange.receiveDelivery, AuthorizeArtifact: exchange.authorizeArtifact,
		MaxHandlers: maxExchangeRoutes,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("daemon exchange: listen: %w", err)
	}
	session := &exchangeSession{cancel: cancel, server: server, done: make(chan struct{})}
	workerDone := make(chan error, 1)
	peerDone := make(chan error, 1)
	go func() { workerDone <- exchange.runOutbox(lifetime) }()
	go func() { peerDone <- server.Wait(context.Background()) }()
	go session.supervise(lifetime, workerDone, peerDone)
	return session, nil
}

func (exchange *exchangeRuntime) receiveDelivery(ctx context.Context,
	peer peerlink.AuthenticatedPeer, offer peerlink.DeliveryOffer,
) (peerlink.DeliveryResponse, error) {
	result, err := exchange.store.StagePeerDelivery(ctx, peer.ID(),
		offer.CanonicalDelivery(), offer.Signature())
	if err != nil {
		return peerlink.DeliveryResponse{}, err
	}
	if response, ok, err := peerAdmissionResponse(result); ok || err != nil {
		return response, err
	}
	if result.State() != authority.PeerAdmissionStateStaged {
		return peerlink.NewTransportACK(), nil
	}
	destination, err := exchange.authenticatedDestination(peer)
	if err != nil {
		return peerlink.DeliveryResponse{}, err
	}
	if err := exchange.pullAndCatalog(ctx, destination, result.Delivery()); err != nil {
		if ctx.Err() != nil {
			return peerlink.DeliveryResponse{}, ctx.Err()
		}
		if errors.Is(err, errRemoteArtifactUnavailable) {
			return peerlink.NewTransportACK(), nil
		}
		return peerlink.DeliveryResponse{}, err
	}
	result, err = exchange.store.AdmitPeerDelivery(ctx, offer.DeliveryID())
	if err != nil {
		return peerlink.DeliveryResponse{}, err
	}
	if response, ok, err := peerAdmissionResponse(result); ok || err != nil {
		return response, err
	}
	return peerlink.NewTransportACK(), nil
}

func peerAdmissionResponse(result authority.PeerAdmissionResult) (
	peerlink.DeliveryResponse, bool, error,
) {
	receipt, present := result.Receipt()
	if !present {
		return peerlink.DeliveryResponse{}, false, nil
	}
	response, err := peerlink.NewAdmissionResponse(receipt)
	return response, true, err
}

func (exchange *exchangeRuntime) authenticatedDestination(
	peer peerlink.AuthenticatedPeer,
) (peerlink.Peer, error) {
	destination, found := exchange.peersByID[peer.ID().String()]
	if !found || !bytes.Equal(destination.PublicKey, peer.PublicKey()) {
		return peerlink.Peer{}, errors.New("daemon exchange: authenticated Peer route changed")
	}
	return destination, nil
}

func (exchange *exchangeRuntime) pullAndCatalog(ctx context.Context, destination peerlink.Peer,
	delivery agency.PeerDelivery,
) error {
	for _, digest := range delivery.Artifacts() {
		content, err := exchange.objects.Read(ctx, digest, artifact.MaxObjectBytes)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("daemon exchange: read local Artifact: %w", err)
			}
			if _, err = exchange.client.PullArtifact(ctx, destination, delivery, digest); err != nil {
				if errors.Is(err, peerlink.ErrTransport) ||
					errors.Is(err, peerlink.ErrAuthentication) || errors.Is(err, peerlink.ErrFrame) {
					return fmt.Errorf("%w: %v", errRemoteArtifactUnavailable, err)
				}
				return err
			}
			content, err = exchange.objects.Read(ctx, digest, artifact.MaxObjectBytes)
			if err != nil {
				return fmt.Errorf("daemon exchange: read pulled Artifact: %w", err)
			}
		}
		verified, err := authority.VerifyArtifact(content, exchange.now().Round(0).UTC())
		if err != nil || verified.Digest() != digest {
			return errors.New("daemon exchange: pulled Artifact verification diverged")
		}
		if err := exchange.store.CatalogArtifact(ctx, verified); err != nil {
			return fmt.Errorf("daemon exchange: catalog Artifact: %w", err)
		}
	}
	return nil
}

func (exchange *exchangeRuntime) authorizeArtifact(ctx context.Context,
	peer peerlink.AuthenticatedPeer, request peerlink.ArtifactRequest,
) (bool, error) {
	return exchange.store.AuthorizePeerArtifact(ctx, peer.ID(), request.DeliveryID(),
		request.EnvelopeDigest(), request.ObjectDigest())
}

func (exchange *exchangeRuntime) runOutbox(ctx context.Context) error {
	if err := exchange.deliverPending(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(exchange.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := exchange.deliverPending(ctx); err != nil {
				return err
			}
		}
	}
}

func (exchange *exchangeRuntime) deliverPending(ctx context.Context) error {
	pending, err := exchange.store.PendingPeerDeliveries(ctx, exchange.batchSize)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	for _, item := range pending {
		if ctx.Err() != nil {
			return nil
		}
		destination, err := exchange.peerForRoute(item.Route())
		if err != nil {
			return err
		}
		response, err := exchange.client.SendDelivery(ctx, destination, item.Delivery())
		if err != nil || response.IsTransportACK() {
			continue
		}
		receipt, present := response.AdmissionReceipt()
		if !present {
			return errors.New("daemon exchange: peer returned an invalid response state")
		}
		if _, _, err := exchange.store.SettlePeerDelivery(ctx, destination.ID,
			item.Delivery().ID(), receipt.CanonicalJSON(), response.Signature()); err != nil {
			if errors.Is(err, authority.ErrPeerDeliveryExpired) {
				continue
			}
			return fmt.Errorf("daemon exchange: settle delivery: %w", err)
		}
	}
	return nil
}

func (exchange *exchangeRuntime) peerForRoute(route authority.PeerRouteProjection) (
	peerlink.Peer, error,
) {
	peer, found := exchange.peersByID[route.RemotePeerID().String()]
	if !found || !route.Active() || peer.Address != route.TransportAddress() ||
		!bytes.Equal(peer.PublicKey, route.RemotePublicKey()) {
		return peerlink.Peer{}, errors.New("daemon exchange: pending delivery route changed")
	}
	return peer, nil
}
