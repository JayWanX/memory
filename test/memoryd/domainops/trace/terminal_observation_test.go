package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

func TestTraceEventShapeSeparatesTerminalObservationFromOrdinaryDelivery(t *testing.T) {
	root := eventRefWire{ID: "event:request-root", Digest: testDigest("request-root")}
	origin := eventRefWire{ID: "event:remote-terminal", Digest: testDigest("remote-terminal")}
	replyID := "delivery:" + strings.Repeat("a", 64)

	var observation eventWire
	observation.Machine.Consequence = "observation.completed"
	observation.Machine.InReplyToDelivery = replyID
	observation.Evidence.Correlation = &root
	observation.Evidence.Causation = []eventRefWire{origin}
	if err := validateEventShape(observation); err != nil {
		t.Fatalf("validateEventShape(observation) error = %v", err)
	}

	withTarget := observation
	withTarget.Machine.Targets = []targetWire{{Destination: "local", LocalPrincipal: "principal:lead"}}
	if err := validateEventShape(withTarget); err == nil {
		t.Fatal("terminal observation with a successor target passed independent shape validation")
	}
	withoutReply := observation
	withoutReply.Machine.InReplyToDelivery = ""
	if err := validateEventShape(withoutReply); err == nil {
		t.Fatal("terminal observation without in-reply-to authority passed validation")
	}

	var ordinary eventWire
	ordinary.Machine.Consequence = "handling.create"
	ordinary.Machine.Targets = []targetWire{{Destination: "local", LocalPrincipal: "principal:worker"}}
	if err := validateEventShape(ordinary); err != nil {
		t.Fatalf("validateEventShape(ordinary delivery) error = %v", err)
	}
	ordinary.Machine.InReplyToDelivery = replyID
	if err := validateEventShape(ordinary); err == nil {
		t.Fatal("ordinary handling creation carrying a reply binding passed validation")
	}
}

func TestStoredInboxReplyLinkMustEqualSignedDelivery(t *testing.T) {
	delivery := terminalPeerDelivery(t)
	inReplyTo, present := delivery.InReplyToDelivery()
	if !present {
		t.Fatal("terminal delivery fixture has no reply link")
	}
	if !storedInboxReplyMatches(delivery, sql.NullString{String: inReplyTo.String(), Valid: true}) {
		t.Fatal("exact durable reply link did not match signed Delivery")
	}
	if storedInboxReplyMatches(delivery, sql.NullString{}) {
		t.Fatal("missing durable reply link matched signed Delivery")
	}
	if storedInboxReplyMatches(delivery, sql.NullString{
		String: "delivery:" + strings.Repeat("b", 64), Valid: true,
	}) {
		t.Fatal("different durable reply link matched signed Delivery")
	}
}

func TestGlobalDeliveryValidationBindsTerminalObservationToExactOutboundRequest(t *testing.T) {
	fixture := newTerminalObservationValidationFixture()
	assertTerminalObservationBaseline(t, fixture)
	assertTerminalObservationRejectsPseudoHead(t, fixture)
	assertTerminalObservationRejectsPreClosedAnchor(t, fixture)
	assertTerminalObservationAllowsLaterExplicitClose(t, fixture)
	assertTerminalObservationRejectsWrongRoute(t, fixture)
	assertTerminalObservationRejectsSuccessor(t, fixture)
}

type terminalObservationValidationFixture struct {
	accepted    time.Time
	request     deliveryEvidence
	root        eventEvidence
	terminal    eventEvidence
	observation eventEvidence
	nodes       []nodeEvidence
	global      map[string]eventEvidence
}

func newTerminalObservationValidationFixture() terminalObservationValidationFixture {
	accepted := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	root := eventEvidence{Node: "lead", ID: "event:request-root", Digest: testDigest("request-root"),
		AcceptedAt: accepted, OriginSequence: 1, SourcePrincipal: "principal:lead",
		SemanticKind: "ops.request", PayloadBytes: len("inspect production"),
		Consequence: "handling.create", Targets: []string{"principal:lead", "remote/data"}}
	requestDeliveryID := "delivery:" + strings.Repeat("a", 64)
	request := deliveryFromOrigin(root, "lead", "outbox", requestDeliveryID,
		"route:lead-data", testDigest("request-envelope"))
	request.ReplyAnchorHandlingID = "handling:request-anchor"
	request.ExpectedReplyRootID, request.ExpectedReplyRootDigest = root.ID, root.Digest
	requestRemote := eventEvidence{Node: "data", ID: "event:data-request",
		Digest: testDigest("data-request"), AcceptedAt: accepted.Add(30 * time.Second),
		OriginSequence: 1, CausalDepth: request.OriginCausalDepth,
		SourcePrincipal: "principal:lead-surrogate", OperationKey: request.ID,
		RequestDigest: request.EnvelopeDigest, SemanticKind: root.SemanticKind,
		PayloadBytes: root.PayloadBytes, Consequence: "handling.create",
		Targets:   []string{"principal:data"},
		Causation: []eventRefWire{{ID: root.ID, Digest: root.Digest}}}
	requestInbox := request
	requestInbox.Node, requestInbox.Direction = "data", "inbox"
	requestInbox.ReplyAnchorHandlingID = ""
	requestInbox.ExpectedReplyRootID, requestInbox.ExpectedReplyRootDigest = "", ""
	request.LocalEventID, request.LocalEventDigest, request.Accepted =
		requestRemote.ID, requestRemote.Digest, true
	requestInbox.LocalEventID, requestInbox.LocalEventDigest, requestInbox.Accepted =
		requestRemote.ID, requestRemote.Digest, true

	correlation := &eventRefWire{ID: root.ID, Digest: root.Digest}
	terminal := eventEvidence{Node: "data", ID: "event:data-terminal", Digest: testDigest("data-terminal"),
		AcceptedAt: accepted.Add(time.Minute), OriginSequence: 2, CausalDepth: 1,
		SourcePrincipal: "principal:data", RequestDigest: testDigest("data-operation"),
		SemanticKind: "ops.response", PayloadBytes: len("database is healthy"),
		Consequence: "handling.resolve.completed", Targets: []string{"remote/lead"},
		SubjectHandling: "handling:data-request", Correlation: correlation,
		Causation:         []eventRefWire{{ID: requestRemote.ID, Digest: requestRemote.Digest}},
		InReplyToDelivery: requestDeliveryID}
	replyEnvelope := testDigest("reply-envelope")
	reply := deliveryFromOrigin(terminal, "lead", "inbox",
		"delivery:"+strings.Repeat("c", 64), "route:lead-data", replyEnvelope)
	reply.InReplyToDeliveryID = requestDeliveryID

	observation := eventEvidence{Node: "lead", ID: "event:lead-observation",
		Digest: testDigest("lead-observation"), AcceptedAt: accepted.Add(2 * time.Minute),
		OriginSequence: 3, CausalDepth: reply.OriginCausalDepth,
		SourcePrincipal: "principal:data-surrogate", OperationKey: reply.ID,
		RequestDigest: replyEnvelope,
		SemanticKind:  terminal.SemanticKind, PayloadBytes: terminal.PayloadBytes,
		Consequence: "observation.completed", Correlation: correlation,
		Causation:         []eventRefWire{{ID: terminal.ID, Digest: terminal.Digest}},
		InReplyToDelivery: requestDeliveryID}
	reply.LocalEventID, reply.LocalEventDigest, reply.Accepted = observation.ID, observation.Digest, true
	replyOutbox := reply
	replyOutbox.Node, replyOutbox.Direction = "data", "outbox"

	nodes := []nodeEvidence{{Role: "lead", Events: []eventEvidence{root, observation},
		Handlings: []handlingEvidence{{ID: request.ReplyAnchorHandlingID, HeadEventID: root.ID,
			State: "open", CreatedSequence: root.OriginSequence}},
		Deliveries: []deliveryEvidence{request, reply}},
		{Role: "data", Events: []eventEvidence{requestRemote, terminal},
			Handlings: []handlingEvidence{{ID: "handling:data-request", HeadEventID: terminal.ID,
				State: "terminal", CreatedSequence: requestRemote.OriginSequence}},
			Deliveries: []deliveryEvidence{requestInbox, replyOutbox}}}
	global := map[string]eventEvidence{root.ID: root, requestRemote.ID: requestRemote,
		terminal.ID: terminal, observation.ID: observation}
	return terminalObservationValidationFixture{accepted: accepted, request: request, root: root,
		terminal: terminal, observation: observation, nodes: nodes, global: global}
}

func assertTerminalObservationBaseline(t *testing.T, fixture terminalObservationValidationFixture) {
	t.Helper()
	if err := validateGlobalDeliveries(fixture.nodes, fixture.global); err != nil {
		t.Fatalf("validateGlobalDeliveries(terminal observation) error = %v", err)
	}
}

func assertTerminalObservationRejectsPseudoHead(t *testing.T,
	fixture terminalObservationValidationFixture,
) {
	t.Helper()
	pseudoHead := cloneDeliveryNodes(fixture.nodes)
	pseudoHead[0].Handlings[0].State = "terminal"
	pseudoHead[0].Handlings[0].HeadEventID = "event:nonexistent-local-decision"
	if err := validateGlobalDeliveries(pseudoHead, fixture.global); err == nil {
		t.Fatal("nonexistent final anchor head passed validation")
	}
}

func assertTerminalObservationRejectsPreClosedAnchor(t *testing.T,
	fixture terminalObservationValidationFixture,
) {
	t.Helper()
	preClose := eventEvidence{Node: "lead", ID: "event:preclosed-local-decision",
		Digest: testDigest("preclosed-local-decision"), AcceptedAt: fixture.accepted.Add(time.Minute),
		OriginSequence: 2, SourcePrincipal: "principal:lead",
		SemanticKind: "ops.local-decision", Consequence: "handling.resolve.declined",
		SubjectHandling: fixture.request.ReplyAnchorHandlingID}
	preClosed := cloneDeliveryNodes(fixture.nodes)
	preClosed[0].Events = append(preClosed[0].Events, preClose)
	preClosed[0].Handlings[0].State = "terminal"
	preClosed[0].Handlings[0].HeadEventID = preClose.ID
	preClosedGlobal := cloneEventEvidenceMap(fixture.global)
	preClosedGlobal[preClose.ID] = preClose
	if err := validateGlobalDeliveries(preClosed, preClosedGlobal); err == nil {
		t.Fatal("anchor closed before terminal observation passed validation")
	}
}

func assertTerminalObservationAllowsLaterExplicitClose(t *testing.T,
	fixture terminalObservationValidationFixture,
) {
	t.Helper()
	laterClose := eventEvidence{Node: "lead", ID: "event:later-local-decision",
		Digest: testDigest("later-local-decision"), AcceptedAt: fixture.accepted.Add(3 * time.Minute),
		OriginSequence: 4, SourcePrincipal: "principal:lead",
		SemanticKind: "ops.local-decision", Consequence: "handling.resolve.completed",
		SubjectHandling: fixture.request.ReplyAnchorHandlingID}
	settledLater := cloneDeliveryNodes(fixture.nodes)
	settledLater[0].Events = append(settledLater[0].Events, laterClose)
	settledLater[0].Handlings[0].State = "terminal"
	settledLater[0].Handlings[0].HeadEventID = laterClose.ID
	settledLaterGlobal := cloneEventEvidenceMap(fixture.global)
	settledLaterGlobal[laterClose.ID] = laterClose
	if err := validateGlobalDeliveries(settledLater, settledLaterGlobal); err != nil {
		t.Fatalf("later explicit local settlement invalidated prior observation: %v", err)
	}
}

func assertTerminalObservationRejectsWrongRoute(t *testing.T,
	fixture terminalObservationValidationFixture,
) {
	t.Helper()
	tampered := cloneDeliveryNodes(fixture.nodes)
	tampered[0].Deliveries[1].RouteID = "route:other"
	if err := validateGlobalDeliveries(tampered, fixture.global); err == nil {
		t.Fatal("terminal observation linked across a different route passed validation")
	}
}

func assertTerminalObservationRejectsSuccessor(t *testing.T,
	fixture terminalObservationValidationFixture,
) {
	t.Helper()
	tampered := cloneDeliveryNodes(fixture.nodes)
	tampered[0].Events[1].Targets = []string{"principal:lead"}
	globalTampered := map[string]eventEvidence{fixture.root.ID: fixture.root,
		fixture.terminal.ID: fixture.terminal, fixture.observation.ID: tampered[0].Events[1]}
	if err := validateGlobalDeliveries(tampered, globalTampered); err == nil {
		t.Fatal("terminal observation that creates a successor passed validation")
	}
}

func TestGlobalDeliveryValidationKeepsOrdinaryDeliveryAsOneHandling(t *testing.T) {
	accepted := time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC)
	origin := eventEvidence{Node: "lead", ID: "event:ordinary-origin", Digest: testDigest("ordinary-origin"),
		AcceptedAt: accepted, OriginSequence: 1, SourcePrincipal: "principal:lead",
		SemanticKind: "ops.request", PayloadBytes: len("inspect logs"),
		Consequence: "handling.create", Targets: []string{"remote/data"}}
	envelope := testDigest("ordinary-envelope")
	outbox := deliveryFromOrigin(origin, "lead", "outbox",
		"delivery:"+strings.Repeat("d", 64), "route:data-lead", envelope)
	inbox := outbox
	inbox.Node, inbox.Direction = "data", "inbox"
	local := eventEvidence{Node: "data", ID: "event:ordinary-local", Digest: testDigest("ordinary-local"),
		AcceptedAt: accepted.Add(time.Minute), OriginSequence: 1, CausalDepth: outbox.OriginCausalDepth,
		SourcePrincipal: "principal:lead-surrogate", OperationKey: outbox.ID,
		RequestDigest: envelope,
		SemanticKind:  origin.SemanticKind, PayloadBytes: origin.PayloadBytes,
		Consequence: "handling.create", Targets: []string{"principal:data"},
		Causation: []eventRefWire{{ID: origin.ID, Digest: origin.Digest}}}
	outbox.LocalEventID, outbox.LocalEventDigest, outbox.Accepted = local.ID, local.Digest, true
	inbox.LocalEventID, inbox.LocalEventDigest, inbox.Accepted = local.ID, local.Digest, true
	nodes := []nodeEvidence{{Role: "lead", Events: []eventEvidence{origin},
		Deliveries: []deliveryEvidence{outbox}}, {Role: "data", Events: []eventEvidence{local},
		Deliveries: []deliveryEvidence{inbox}}}
	global := map[string]eventEvidence{origin.ID: origin, local.ID: local}
	if err := validateGlobalDeliveries(nodes, global); err != nil {
		t.Fatalf("validateGlobalDeliveries(ordinary) error = %v", err)
	}
	local.Consequence = "observation.completed"
	nodes[1].Events[0] = local
	global[local.ID] = local
	if err := validateGlobalDeliveries(nodes, global); err == nil {
		t.Fatal("ordinary Delivery disguised as a zero-target observation passed validation")
	}
}

func terminalPeerDelivery(t *testing.T) agency.PeerDelivery {
	t.Helper()
	route := testValue(t, "route", "route:trace-reply", agency.NewRouteID)
	inReplyTo, err := agency.ParseDeliveryID("delivery:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := agency.NewPeerDelivery(route, agency.PeerDeliverySpec{
		OriginEvent:    testEventRef(t, "event:remote-terminal", "remote-terminal"),
		OriginSequence: 2, OriginAcceptedAt: time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC),
		OriginSource:      testValue(t, "principal", "principal:remote", agency.NewAgentPrincipalID),
		OriginConsequence: agency.ConsequenceResolveCompleted, OriginTargetCount: 1,
		OriginCorrelation: testEventRef(t, "event:request-root", "request-root"),
		InReplyToDelivery: inReplyTo,
		TargetAlias:       testValue(t, "alias", "local/requester", agency.NewOpaqueHandle),
		Kind:              testValue(t, "kind", "review.response", agency.NewSemanticLabel),
		Payload:           testValue(t, "payload", "accepted", agency.NewSemanticPayload),
		CausalDepth:       2, ExpiresAt: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func deliveryFromOrigin(origin eventEvidence, node, direction, id, route, envelope string) deliveryEvidence {
	return deliveryEvidence{Node: node, Direction: direction, ID: id, RouteID: route,
		State: "settled", CapturedAt: origin.AcceptedAt.Add(time.Second), EnvelopeDigest: envelope,
		OriginEventID: origin.ID, OriginEventDigest: origin.Digest,
		OriginSequence: origin.OriginSequence, OriginAcceptedAt: origin.AcceptedAt,
		OriginSource: origin.SourcePrincipal, OriginConsequence: origin.Consequence,
		OriginTargetCount: len(origin.Targets), OriginCausalDepth: origin.CausalDepth + 1,
		OriginSemanticKind: origin.SemanticKind, OriginPayloadBytes: origin.PayloadBytes,
		OriginArtifacts:   append([]string(nil), origin.Artifacts...),
		OriginCausation:   append([]eventRefWire(nil), origin.Causation...),
		OriginCorrelation: cloneEventRef(origin.Correlation)}
}

func cloneDeliveryNodes(values []nodeEvidence) []nodeEvidence {
	result := append([]nodeEvidence(nil), values...)
	for index := range result {
		result[index].Events = append([]eventEvidence(nil), result[index].Events...)
		result[index].Deliveries = append([]deliveryEvidence(nil), result[index].Deliveries...)
		result[index].Handlings = append([]handlingEvidence(nil), result[index].Handlings...)
	}
	return result
}

func cloneEventEvidenceMap(values map[string]eventEvidence) map[string]eventEvidence {
	result := make(map[string]eventEvidence, len(values))
	for id, event := range values {
		result[id] = event
	}
	return result
}

func testDigest(value string) string { return agency.Sum([]byte(value)).String() }

func testEventRef(t *testing.T, id, content string) agency.EventRef {
	t.Helper()
	eventID := testValue(t, "Event", id, agency.NewEventID)
	reference, err := agency.NewEventRef(eventID, agency.Sum([]byte(content)))
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func testValue[T any](t *testing.T, label, value string, constructor func(string) (T, error)) T {
	t.Helper()
	result, err := constructor(value)
	if err != nil {
		t.Fatalf("construct %s: %v", label, err)
	}
	return result
}
