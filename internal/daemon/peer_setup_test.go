package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

func TestConfigureExchangePublishesCanonicalPrivateCardAndReplays(t *testing.T) {
	root := canonicalTempDir(t)
	provisioned, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ConfigureExchange(context.Background(), provisioned.StateDirectory(),
		"127.0.0.1:17401", "node-a:17401")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ConfigureExchange(context.Background(), provisioned.StateDirectory(),
		"127.0.0.1:17401", "node-a:17401")
	if err != nil || !bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) ||
		first.PeerID() != provisioned.PeerID() {
		t.Fatalf("configuration replay = (%s, %v)", second.CanonicalJSON(), err)
	}
	if _, err := ParsePeerCardCanonicalJSON(first.CanonicalJSON()); err != nil {
		t.Fatalf("parse projected Card: %v", err)
	}
	if bytes.Contains(first.CanonicalJSON(), []byte("private")) {
		t.Fatalf("Peer Card exposed private material: %s", first.CanonicalJSON())
	}
	configPath := filepath.Join(provisioned.StateDirectory(), exchangeConfigFile)
	assertOwnerMode(t, configPath, ownerFileMode)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config, err := parseExchangeConfig(raw)
	if err != nil || config.listenAddress != "127.0.0.1:17401" ||
		config.advertisedAddress != "node-a:17401" {
		t.Fatalf("stored exchange configuration = (%#v, %v)", config, err)
	}
	if _, err := ConfigureExchange(context.Background(), provisioned.StateDirectory(),
		"127.0.0.1:17402", "node-a:17401"); !errors.Is(err, ErrPeerSetup) {
		t.Fatalf("immutable configuration error = %v", err)
	}

	// Prepared but not yet enrolled is deliberately a complete local node.
	runtime, err := OpenProvisioned(context.Background(), provisioned.StateDirectory())
	if err != nil || runtime.exchange != nil {
		t.Fatalf("prepared route-less OpenProvisioned = (%v, %v)", runtime, err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPeerCardParserFailsClosedBeforeEnrollment(t *testing.T) {
	root := canonicalTempDir(t)
	provisioned, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	card, err := ConfigureExchange(context.Background(), provisioned.StateDirectory(),
		"127.0.0.1:17411", "node-a:17411")
	if err != nil {
		t.Fatal(err)
	}
	var original peerCardWire
	if err := json.Unmarshal(card.CanonicalJSON(), &original); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		raw  func() []byte
	}{
		{name: "trailing newline", raw: func() []byte {
			return append(card.CanonicalJSON(), '\n')
		}},
		{name: "wrong identity", raw: func() []byte {
			changed := original
			changed.PeerID = "peer:not-derived"
			raw, _ := json.Marshal(changed)
			return raw
		}},
		{name: "wildcard advertisement", raw: func() []byte {
			changed := original
			changed.TransportAddress = "0.0.0.0:17411"
			raw, _ := json.Marshal(changed)
			return raw
		}},
		{name: "unknown field", raw: func() []byte {
			return bytes.Replace(card.CanonicalJSON(), []byte("}"), []byte(`,"extra":true}`), 1)
		}},
		{name: "non-canonical public key", raw: func() []byte {
			changed := original
			changed.PublicKey = changed.PublicKey[:8] + "\n" + changed.PublicKey[8:]
			raw, _ := json.Marshal(changed)
			return raw
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if parsed, err := ParsePeerCardCanonicalJSON(test.raw()); err == nil ||
				!parsed.PeerID().IsZero() {
				t.Fatalf("invalid Peer Card parsed as (%#v, %v)", parsed, err)
			}
		})
	}
	identity, err := loadTransportIdentity(provisioned.StateDirectory())
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"", ":7447", "node:0", "node:07447", "0.0.0.0:7447",
		"node name:7447", "node\nname:7447"} {
		if _, err := newPeerCard(identity.projection, address); err == nil {
			t.Fatalf("invalid advertised address %q accepted", address)
		}
	}
}

func TestExchangeConfigurationRequiresPrepareReplayToRecoverPending(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "linked"}[linked], func(t *testing.T) {
			root := canonicalTempDir(t)
			provisioned, err := Provision(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			config, err := newExchangeConfig("127.0.0.1:17421", "node-a:17421")
			if err != nil {
				t.Fatal(err)
			}
			pending := filepath.Join(provisioned.StateDirectory(), exchangeConfigPending)
			final := filepath.Join(provisioned.StateDirectory(), exchangeConfigFile)
			if err := createPrivateFile(pending, config.canonical); err != nil {
				t.Fatal(err)
			}
			if linked {
				if err := os.Link(pending, final); err != nil {
					t.Fatal(err)
				}
			}
			if runtime, err := OpenProvisioned(context.Background(), provisioned.StateDirectory()); err == nil || runtime != nil {
				t.Fatalf("strict open recovered pending state as (%v, %v)", runtime, err)
			}
			card, err := ConfigureExchange(context.Background(), provisioned.StateDirectory(),
				"127.0.0.1:17421", "node-a:17421")
			if err != nil || card.PeerID() != provisioned.PeerID() {
				t.Fatalf("prepare replay recovery = (%v, %v)", card.PeerID(), err)
			}
			runtime, err := OpenProvisioned(context.Background(), provisioned.StateDirectory())
			if err != nil || runtime.exchange != nil {
				t.Fatalf("open recovered prepared node = (%v, %v)", runtime, err)
			}
			if err := runtime.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending configuration survived recovery: %v", err)
			}
			assertOwnerMode(t, final, ownerFileMode)
		})
	}

	root := canonicalTempDir(t)
	provisioned, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "external.json")
	if err := os.WriteFile(target, []byte(`{}`), ownerFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(provisioned.StateDirectory(), exchangeConfigFile)); err != nil {
		t.Fatal(err)
	}
	if runtime, err := OpenProvisioned(context.Background(), provisioned.StateDirectory()); err == nil ||
		runtime != nil {
		t.Fatalf("unsafe exchange configuration opened as (%v, %v)", runtime, err)
	}
}

func TestReciprocalEnrollmentDerivesOneRouteAndIsIdempotent(t *testing.T) {
	left, leftCard := preparedPeerNode(t, "left", "127.0.0.1:17431", "left:17431")
	right, rightCard := preparedPeerNode(t, "right", "127.0.0.1:17432", "right:17432")
	leftEnrollment, err := EnrollPeer(context.Background(), left.StateDirectory(), "peer-right", rightCard)
	if err != nil {
		t.Fatal(err)
	}
	rightEnrollment, err := EnrollPeer(context.Background(), right.StateDirectory(), "peer-left", leftCard)
	if err != nil {
		t.Fatal(err)
	}
	if leftEnrollment.RouteID() != rightEnrollment.RouteID() {
		t.Fatalf("reciprocal RouteIDs diverged: %s != %s", leftEnrollment.RouteID().String(),
			rightEnrollment.RouteID().String())
	}
	replayed, err := EnrollPeer(context.Background(), left.StateDirectory(), "peer-right", rightCard)
	if err != nil || !bytes.Equal(replayed.CanonicalJSON(), leftEnrollment.CanonicalJSON()) {
		t.Fatalf("enrollment replay = (%s, %v)", replayed.CanonicalJSON(), err)
	}
	assertPeerEnrollmentRoute(t, left, leftEnrollment, rightCard, "peer-right")
	assertPeerEnrollmentRoute(t, right, rightEnrollment, leftCard, "peer-left")
	if _, err := EnrollPeer(context.Background(), left.StateDirectory(), "peer-self", leftCard); !errors.Is(err, ErrPeerSetup) {
		t.Fatalf("self enrollment error = %v", err)
	}
	if _, err := EnrollPeer(context.Background(), left.StateDirectory(), "peer-renamed", rightCard); !errors.Is(err, ErrPeerSetup) {
		t.Fatalf("route rebinding error = %v", err)
	}
}

func TestEnrollmentRequiresPreparedOfflineAuthority(t *testing.T) {
	unpreparedRoot := canonicalTempDir(t)
	unprepared, err := Provision(context.Background(), unpreparedRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, remoteCard := preparedPeerNode(t, "remote", "127.0.0.1:17441", "remote:17441")
	if _, err := EnrollPeer(context.Background(), unprepared.StateDirectory(), "peer-remote", remoteCard); !errors.Is(err, ErrPeerSetup) || !strings.Contains(err.Error(), "peer prepare") {
		t.Fatalf("unprepared enrollment error = %v", err)
	}

	prepared, _ := preparedPeerNode(t, "prepared", "127.0.0.1:17442", "prepared:17442")
	runtime, err := OpenProvisioned(context.Background(), prepared.StateDirectory())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	if _, err := EnrollPeer(context.Background(), prepared.StateDirectory(), "peer-remote", remoteCard); !errors.Is(err, authority.ErrWriterActive) {
		t.Fatalf("online enrollment error = %v, want writer active", err)
	}
}

func TestOpenProvisionedActivatesPreparedExchange(t *testing.T) {
	leftAddress, rightAddress := freeTCPAddress(t), freeTCPAddress(t)
	for rightAddress == leftAddress {
		rightAddress = freeTCPAddress(t)
	}
	left, leftCard := preparedPeerNode(t, "left-live", leftAddress, leftAddress)
	right, rightCard := preparedPeerNode(t, "right-live", rightAddress, rightAddress)
	if _, err := EnrollPeer(context.Background(), left.StateDirectory(), "peer-right", rightCard); err != nil {
		t.Fatal(err)
	}
	if _, err := EnrollPeer(context.Background(), right.StateDirectory(), "peer-left", leftCard); err != nil {
		t.Fatal(err)
	}
	leftRuntime, err := OpenProvisioned(context.Background(), left.StateDirectory())
	if err != nil || leftRuntime.exchange == nil {
		t.Fatalf("left prepared exchange = (%v, %v)", leftRuntime, err)
	}
	rightRuntime, err := OpenProvisioned(context.Background(), right.StateDirectory())
	if err != nil || rightRuntime.exchange == nil {
		_ = leftRuntime.Close(context.Background())
		t.Fatalf("right prepared exchange = (%v, %v)", rightRuntime, err)
	}
	rightErrors := make(chan error, 1)
	go func() { rightErrors <- rightRuntime.Serve(context.Background()) }()
	select {
	case serveErr := <-rightErrors:
		t.Fatalf("right prepared Serve failed before readiness: %v", serveErr)
	case <-time.After(100 * time.Millisecond):
	}
	waitForSocket(t, filepath.Join(right.StateDirectory(), controlSocketName))
	emitRemoteHandling(t, leftRuntime, "peer-right", []byte("prepared exchange evidence"))
	leftErrors := serveRuntimeForTest(t, leftRuntime, left.StateDirectory())
	waitForSettledOutbox(t, leftRuntime)
	assertRemoteHandlingVisible(t, rightRuntime, "prepared exchange evidence")
	stopExchangeNode(t, leftRuntime, leftErrors)
	stopExchangeNode(t, rightRuntime, rightErrors)
}

func preparedPeerNode(t *testing.T, name, listen, advertise string) (ProvisionResult, PeerCard) {
	t.Helper()
	_ = name
	base, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "mnp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	provisioned, err := Provision(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	card, err := ConfigureExchange(context.Background(), provisioned.StateDirectory(), listen, advertise)
	if err != nil {
		t.Fatal(err)
	}
	return provisioned, card
}

func assertPeerEnrollmentRoute(t *testing.T, node ProvisionResult, enrollment PeerEnrollment,
	remote PeerCard, alias string,
) {
	t.Helper()
	objects, err := artifact.OpenExisting(filepath.Join(node.StateDirectory(), "objects", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := authority.OpenExistingWithArtifactVerifier(context.Background(),
		filepath.Join(node.StateDirectory(), authorityFileName), objects)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routes, err := store.PeerRoutes(context.Background())
	if err != nil || len(routes) != 1 {
		t.Fatalf("peer routes = (%#v, %v)", routes, err)
	}
	route := routes[0]
	if route.RouteID() != enrollment.RouteID() || route.PublicAlias().String() != alias ||
		route.RemotePeerID() != remote.PeerID() ||
		!bytes.Equal(route.RemotePublicKey(), remote.PublicKey()) ||
		route.TransportAddress() != remote.TransportAddress() ||
		route.RemoteTargetAlias().String() != defaultTargetAlias ||
		route.InboundTargetAlias().String() != defaultTargetAlias ||
		route.LocalTargetPrincipal() != node.Principal() {
		t.Fatalf("enrolled route = %#v", route)
	}
}
