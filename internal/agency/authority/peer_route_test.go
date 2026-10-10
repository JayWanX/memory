package authority

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestPeerRouteEnrollmentIsImmutableAndIdempotent(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:route-owner")
	spec := peerRouteSpec(t, fixture.principal, "alpha")
	first, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	assertEnrolledPeerRoute(t, first, spec, fixture.principal)
	key := first.RemotePublicKey()
	key[0] ^= 0xff
	if bytes.Equal(key, first.RemotePublicKey()) {
		t.Fatal("PeerRouteProjection exposed mutable key storage")
	}
	*fixture.now = fixture.now.Add(time.Minute)
	replayed, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec)
	if err != nil || replayed.EnrolledAt() != first.EnrolledAt() ||
		replayed.SurrogateSourcePrincipal() != first.SurrogateSourcePrincipal() {
		t.Fatalf("exact enrollment replay = %#v, %v", replayed, err)
	}
}

func TestPeerRouteEnrollmentIsDurableAndRevocationIsIrreversible(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:route-lifecycle")
	spec := peerRouteSpec(t, fixture.principal, "alpha")
	if _, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := open(fixture.ctx, fixture.path, func() time.Time { return *fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = reopened
	routes, err := reopened.PeerRoutes(fixture.ctx)
	if err != nil || len(routes) != 1 || routes[0].RouteID() != spec.RouteID || !routes[0].Active() {
		t.Fatalf("restarted route projection = %#v, %v", routes, err)
	}
	revoked, err := reopened.RevokePeerRoute(fixture.ctx, spec.RouteID)
	if err != nil || revoked.Active() || revoked.RevokedAt().IsZero() {
		t.Fatalf("revoke route = %#v, %v", revoked, err)
	}
	replayedRevoke, err := reopened.RevokePeerRoute(fixture.ctx, spec.RouteID)
	if err != nil || replayedRevoke.RevokedAt() != revoked.RevokedAt() {
		t.Fatalf("replay route revoke = %#v, %v", replayedRevoke, err)
	}
	if _, err := reopened.EnrollPeerRoute(fixture.ctx, spec); !errors.Is(err, ErrPeerRouteRevoked) {
		t.Fatalf("reactivate route error = %v, want ErrPeerRouteRevoked", err)
	}
}

func assertEnrolledPeerRoute(t *testing.T, route PeerRouteProjection, spec PeerRouteSpec,
	principal agency.AgentPrincipalID,
) {
	t.Helper()
	if !route.Active() || route.RouteID() != spec.RouteID || route.PublicAlias() != spec.PublicAlias {
		t.Fatalf("enrolled route identity = %#v", route)
	}
	if route.RemoteTargetAlias() != spec.RemoteTargetAlias ||
		route.InboundTargetAlias() != spec.InboundTargetAlias {
		t.Fatalf("enrolled route target aliases = %#v", route)
	}
	if route.LocalTargetPrincipal() != principal {
		t.Fatalf("enrolled local target = %s, want %s", route.LocalTargetPrincipal(), principal)
	}
	if route.SurrogateSourcePrincipal().IsZero() || route.SurrogateSourcePrincipal() == principal {
		t.Fatalf("enrolled surrogate Principal = %s", route.SurrogateSourcePrincipal())
	}
}

func TestPeerRouteRejectsImmutableFieldAndNamespaceRebinding(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:route-conflicts")
	spec := peerRouteSpec(t, fixture.principal, "alpha")
	if _, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		spec PeerRouteSpec
	}{
		{name: "same RouteID different remote alias", spec: func() PeerRouteSpec {
			changed := spec
			changed.RemoteTargetAlias = mustHandle(t, "remote:changed")
			return changed
		}()},
		{name: "reuse public alias", spec: func() PeerRouteSpec {
			changed := peerRouteSpec(t, fixture.principal, "beta")
			changed.PublicAlias = spec.PublicAlias
			return changed
		}()},
		{name: "reuse remote identity", spec: func() PeerRouteSpec {
			changed := peerRouteSpec(t, fixture.principal, "gamma")
			changed.RemotePeerID = spec.RemotePeerID
			return changed
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := fixture.store.EnrollPeerRoute(fixture.ctx, test.spec); !errors.Is(err, ErrPeerRouteConflict) {
				t.Fatalf("EnrollPeerRoute() error = %v, want ErrPeerRouteConflict", err)
			}
		})
	}
}

func TestPeerRouteRequiresEnrolledLocalPrincipalAndMachineOwnedSurrogate(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:route-authority")
	missing := mustPrincipal(t, "principal:not-enrolled")
	spec := peerRouteSpec(t, missing, "missing")
	if _, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec); !errors.Is(err, ErrPrincipalUnavailable) {
		t.Fatalf("route to missing Principal error = %v, want ErrPrincipalUnavailable", err)
	}

	spec = peerRouteSpec(t, fixture.principal, "reserved")
	surrogate, err := derivePeerRouteSourcePrincipal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.EnrollPrincipal(fixture.ctx, surrogate); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec); !errors.Is(err, ErrPeerRouteConflict) {
		t.Fatalf("preclaimed surrogate error = %v, want ErrPeerRouteConflict", err)
	}
}

func TestCurrentProjectsOnlyPublicPeerAliasAndBindsPrivateRoute(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:route-view")
	spec := peerRouteSpec(t, fixture.principal, "view")
	route, err := fixture.store.EnrollPeerRoute(fixture.ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	view := fixture.current(t)
	var public struct {
		Targets []string `json:"targets"`
	}
	if err := json.Unmarshal(view.AgentView().CanonicalJSON(), &public); err != nil {
		t.Fatal(err)
	}
	if len(public.Targets) != 2 || public.Targets[0] != spec.PublicAlias.String() ||
		public.Targets[1] != "self" {
		t.Fatalf("Agent targets = %v", public.Targets)
	}
	publicBytes := string(view.AgentView().CanonicalJSON())
	for _, private := range []string{spec.RouteID.String(), spec.RemotePeerID.String(),
		spec.TransportAddress, spec.RemoteTargetAlias.String(), spec.InboundTargetAlias.String(),
		fixture.principal.String(), route.SurrogateSourcePrincipal().String()} {
		if strings.Contains(publicBytes, private) {
			t.Fatalf("Agent View leaked private route field %q: %s", private, publicBytes)
		}
	}
	privateBytes := string(view.authority.CanonicalJSON())
	if !strings.Contains(privateBytes, spec.RouteID.String()) ||
		!strings.Contains(privateBytes, spec.RemoteTargetAlias.String()) ||
		strings.Contains(privateBytes, spec.RemotePeerID.String()) ||
		strings.Contains(privateBytes, spec.TransportAddress) {
		t.Fatalf("sealed View has wrong route authority boundary: %s", privateBytes)
	}

	remote, err := agency.AliasTarget(spec.PublicAlias)
	if err != nil {
		t.Fatal(err)
	}
	intent := mustIntent(t, agency.IntentSpec{Kind: mustLabel(t, "work.remote-request"),
		Payload: mustPayload(t, "ask the peer"), Consequence: agency.ConsequenceCreateHandlings,
		Successors: []agency.TargetRef{agency.SelfTarget(), remote}})
	request, err := view.Bind(intent, mustOperation(t, "operation:remote-bound"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Targets()) != 2 || request.Targets()[1].Destination() != agency.TargetDestinationRemote ||
		request.Targets()[1].RemoteRoute() != spec.RouteID ||
		request.Targets()[1].RemoteAlias() != spec.RemoteTargetAlias {
		t.Fatalf("private remote target binding = %#v", request.Targets())
	}
	assertRouteValidation(t, fixture, request, "")

	if _, err := fixture.store.RevokePeerRoute(fixture.ctx, spec.RouteID); err != nil {
		t.Fatal(err)
	}
	assertRouteValidation(t, fixture, request, rejectionStaleRoute.String())
	fresh := fixture.current(t)
	if strings.Contains(string(fresh.AgentView().CanonicalJSON()), spec.PublicAlias.String()) {
		t.Fatalf("revoked alias remained in fresh Agent View: %s", fresh.AgentView().CanonicalJSON())
	}
}

func TestPeerRouteBoundKeepsWorstCaseRouteViewRepresentable(t *testing.T) {
	fixture := newAuthorityFixture(t, "principal:route-bound")
	for index := 0; index < MaxActivePeerRoutes; index++ {
		if _, err := fixture.store.EnrollPeerRoute(fixture.ctx,
			maximalPeerRouteSpec(t, fixture.principal, index)); err != nil {
			t.Fatalf("enroll maximal route %d: %v", index, err)
		}
	}

	view := fixture.current(t)
	if size := len(view.authority.CanonicalJSON()); size > agency.MaxViewCanonicalBytes {
		t.Fatalf("maximal route authority View bytes = %d, maximum %d",
			size, agency.MaxViewCanonicalBytes)
	}
	if size := len(view.AgentView().CanonicalJSON()); size > agency.MaxAgentViewCanonicalBytes {
		t.Fatalf("maximal route Agent View bytes = %d, maximum %d",
			size, agency.MaxAgentViewCanonicalBytes)
	}
	var public struct {
		Targets []string `json:"targets"`
	}
	if err := json.Unmarshal(view.AgentView().CanonicalJSON(), &public); err != nil {
		t.Fatal(err)
	}
	if len(public.Targets) != MaxActivePeerRoutes+1 {
		t.Fatalf("maximal route targets = %d, want %d", len(public.Targets), MaxActivePeerRoutes+1)
	}
	if _, err := fixture.store.EnrollPeerRoute(fixture.ctx,
		maximalPeerRouteSpec(t, fixture.principal, MaxActivePeerRoutes)); err == nil {
		t.Fatal("route above active bound unexpectedly enrolled")
	}
}

func assertRouteValidation(t *testing.T, fixture *authorityFixture, request agency.BoundIntent,
	wantCode string,
) {
	t.Helper()
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	authenticated, err := authenticateAttachmentTx(fixture.ctx, tx, fixture.proof)
	if err != nil {
		t.Fatal(err)
	}
	rejection, err := validateMutableAuthorityTx(fixture.ctx, tx, authenticated.value,
		request, *fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if wantCode == "" {
		if rejection != nil {
			t.Fatalf("active route rejected: %#v", rejection)
		}
		return
	}
	if rejection == nil || rejection.code.String() != wantCode {
		t.Fatalf("route rejection = %#v, want %q", rejection, wantCode)
	}
}

func peerRouteSpec(t *testing.T, principal agency.AgentPrincipalID, suffix string) PeerRouteSpec {
	t.Helper()
	return PeerRouteSpec{
		RouteID:              mustRoute(t, "route:"+suffix),
		PublicAlias:          mustHandle(t, "peer-"+suffix),
		RemotePeerID:         mustHandle(t, "transport-peer:"+suffix),
		RemotePublicKey:      bytes.Repeat([]byte{byte(len(suffix) + 1)}, MaxPeerRoutePublicKeyBytes),
		TransportAddress:     "/ip4/127.0.0.1/tcp/4" + string(rune('0'+len(suffix))),
		RemoteTargetAlias:    mustHandle(t, "remote-target:"+suffix),
		InboundTargetAlias:   mustHandle(t, "inbound-target:"+suffix),
		LocalTargetPrincipal: principal,
	}
}

func maximalPeerRouteSpec(t *testing.T, principal agency.AgentPrincipalID, index int) PeerRouteSpec {
	t.Helper()
	return PeerRouteSpec{
		RouteID:              mustRoute(t, maximalRouteToken("route", index)),
		PublicAlias:          mustHandle(t, maximalRouteToken("public", index)),
		RemotePeerID:         mustHandle(t, maximalRouteToken("peer", index)),
		RemotePublicKey:      bytes.Repeat([]byte{byte(index + 1)}, MaxPeerRoutePublicKeyBytes),
		TransportAddress:     strings.Repeat("x", MaxPeerTransportAddressBytes),
		RemoteTargetAlias:    mustHandle(t, maximalRouteToken("remote", index)),
		InboundTargetAlias:   mustHandle(t, maximalRouteToken("inbound", index)),
		LocalTargetPrincipal: principal,
	}
}

func maximalRouteToken(prefix string, index int) string {
	suffix := fmt.Sprintf("%02d", index)
	return prefix + strings.Repeat("x", agency.MaxOpaqueHandleBytes-len(prefix)-len(suffix)) + suffix
}

func mustRoute(t *testing.T, value string) agency.RouteID {
	t.Helper()
	route, err := agency.NewRouteID(value)
	if err != nil {
		t.Fatal(err)
	}
	return route
}
