package main

import (
	"strings"
	"testing"
	"time"
)

func TestAcceptedOutboxReceiptMayNameExactRemoteEvent(t *testing.T) {
	nodes, global := acceptedOutboxReceiptFixture()
	if err := validateGlobalDeliveries(nodes, global); err != nil {
		t.Fatalf("accepted outbox Receipt with exact receiver inbox Event error = %v", err)
	}
}

func TestAcceptedOutboxReceiptRejectsTamperedOrUnknownRemoteEvent(t *testing.T) {
	t.Run("tampered digest", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		remote := nodes[1].Events[0]
		remote.Digest = testDigest("tampered-remote-digest")
		setPairLocalEvent(nodes, remote)
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted outbox Receipt with a tampered remote Event digest passed validation")
		}
	})

	t.Run("unknown event", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		remote := nodes[1].Events[0]
		remote.ID = "event:unknown-remote-effect"
		setPairLocalEvent(nodes, remote)
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted outbox Receipt naming an uncollected remote Event passed validation")
		}
	})
}

func TestDeliveryReceiptDirectionAndLocalEffectRemainFailClosed(t *testing.T) {
	t.Run("invalid direction", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		nodes[0].Deliveries[0].Direction = "mailbox"
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted Delivery with an invalid evidence direction passed validation")
		}
	})

	t.Run("accepted without local Event", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		nodes[0].Deliveries[0].LocalEventID = ""
		nodes[0].Deliveries[0].LocalEventDigest = ""
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted Delivery without a Receipt local Event passed validation")
		}
	})

	t.Run("inbox still requires readmission shape", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		remote := nodes[1].Events[0]
		remote.RequestDigest = testDigest("different-envelope")
		nodes[1].Events[0] = remote
		global[remote.ID] = remote
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted inbox Delivery bypassed strict local readmission shape")
		}
	})

	t.Run("inbox operation identity is the Delivery ID", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		remote := nodes[1].Events[0]
		remote.OperationKey = "operation:unrelated"
		nodes[1].Events[0] = remote
		global[remote.ID] = remote
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted inbox Event with an unrelated operation identity passed validation")
		}
	})
}

func TestAcceptedOutboxReceiptRequiresUniqueReceiverInbox(t *testing.T) {
	t.Run("missing counterpart", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		nodes[1].Deliveries = nil
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted outbox Receipt without a receiver inbox passed validation")
		}
	})

	t.Run("accepted inbox missing sender", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		nodes[0].Deliveries = nil
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted inbox Receipt without a sender outbox passed validation")
		}
	})

	t.Run("duplicate inbox", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		duplicate := nodes[1].Deliveries[0]
		duplicate.Node = "edge"
		nodes = append(nodes, nodeEvidence{Role: "edge", Deliveries: []deliveryEvidence{duplicate}})
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("Delivery with two receiver inbox authorities passed validation")
		}
	})

	t.Run("duplicate outbox", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		duplicate := nodes[0].Deliveries[0]
		duplicate.Node = "platform"
		nodes = append(nodes, nodeEvidence{Role: "platform", Deliveries: []deliveryEvidence{duplicate}})
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("Delivery with two sender outbox authorities passed validation")
		}
	})
}

func TestDeliveryPairAllowsReceiverSettlementBeforeSender(t *testing.T) {
	nodes, global := acceptedOutboxReceiptFixture()
	outbox := &nodes[0].Deliveries[0]
	outbox.State, outbox.Accepted = "pending", false
	outbox.LocalEventID, outbox.LocalEventDigest = "", ""
	if err := validateGlobalDeliveries(nodes, global); err != nil {
		t.Fatalf("receiver-settled sender-pending Delivery pair error = %v", err)
	}
}

func TestRejectedReceiptPairCarriesNoLocalEffect(t *testing.T) {
	nodes, global := acceptedOutboxReceiptFixture()
	for nodeIndex := range nodes {
		for deliveryIndex := range nodes[nodeIndex].Deliveries {
			delivery := &nodes[nodeIndex].Deliveries[deliveryIndex]
			delivery.Accepted = false
			delivery.LocalEventID, delivery.LocalEventDigest = "", ""
		}
	}
	if err := validateGlobalDeliveries(nodes, global); err != nil {
		t.Fatalf("matching rejected Receipt pair error = %v", err)
	}
}

func TestNonAcceptedDeliveryCannotCarryLocalEffect(t *testing.T) {
	t.Run("outbox", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		nodes[0].Deliveries[0].State = "pending"
		nodes[0].Deliveries[0].Accepted = false
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("non-accepted outbox carrying a local Event passed validation")
		}
	})

	t.Run("inbox", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		outbox := &nodes[0].Deliveries[0]
		outbox.State, outbox.Accepted = "pending", false
		outbox.LocalEventID, outbox.LocalEventDigest = "", ""
		nodes[1].Deliveries[0].Accepted = false
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("non-accepted inbox carrying a local Event passed validation")
		}
	})

	t.Run("partial reference", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		nodes[0].Deliveries[0].LocalEventDigest = ""
		nodes[1].Deliveries[0].LocalEventDigest = ""
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("Receipt carrying a partial local Event reference passed validation")
		}
	})
}

func TestAcceptedOutboxReceiptRejectsUnrelatedGlobalEvent(t *testing.T) {
	t.Run("sender-local Event", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		origin := nodes[0].Events[0]
		setPairLocalEvent(nodes, origin)
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted outbox Receipt naming a sender-local Event passed validation")
		}
	})

	t.Run("third-node Event", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		third := nodes[1].Events[0]
		third.Node, third.ID, third.Digest = "platform", "event:third-node-effect", testDigest("third-node-effect")
		nodes = append(nodes, nodeEvidence{Role: "platform", Events: []eventEvidence{third}})
		global[third.ID] = third
		setPairLocalEvent(nodes, third)
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted outbox Receipt naming a third-node Event passed validation")
		}
	})
}

func TestAcceptedOutboxAndInboxReceiptsMustAgree(t *testing.T) {
	t.Run("local Event", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		alternate := nodes[1].Events[0]
		alternate.ID, alternate.Digest = "event:other-receiver-effect", testDigest("other-receiver-effect")
		nodes[1].Events = append(nodes[1].Events, alternate)
		global[alternate.ID] = alternate
		nodes[0].Deliveries[0].LocalEventID = alternate.ID
		nodes[0].Deliveries[0].LocalEventDigest = alternate.Digest
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted outbox and inbox Receipts naming different local Events passed validation")
		}
	})

	t.Run("outcome", func(t *testing.T) {
		nodes, global := acceptedOutboxReceiptFixture()
		nodes[1].Deliveries[0].Accepted = false
		nodes[1].Deliveries[0].LocalEventID = ""
		nodes[1].Deliveries[0].LocalEventDigest = ""
		if err := validateGlobalDeliveries(nodes, global); err == nil {
			t.Fatal("accepted outbox Receipt paired with a rejected inbox Receipt passed validation")
		}
	})
}

func TestDeliveryPairRequiresExactEnvelopeRouteAndOriginAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*deliveryEvidence)
	}{
		{name: "envelope", mutate: func(value *deliveryEvidence) {
			value.EnvelopeDigest = testDigest("different-envelope")
		}},
		{name: "route", mutate: func(value *deliveryEvidence) { value.RouteID = "route:other" }},
		{name: "origin authority", mutate: func(value *deliveryEvidence) {
			value.OriginSemanticKind = "other.request"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nodes, global := acceptedOutboxReceiptFixture()
			test.mutate(&nodes[1].Deliveries[0])
			if err := validateGlobalDeliveries(nodes, global); err == nil {
				t.Fatalf("Delivery pair with mismatched %s passed validation", test.name)
			}
		})
	}
}

func acceptedOutboxReceiptFixture() ([]nodeEvidence, map[string]eventEvidence) {
	accepted := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	origin := eventEvidence{Node: "lead", ID: "event:outbox-origin",
		Digest: testDigest("outbox-origin"), AcceptedAt: accepted, OriginSequence: 1,
		SourcePrincipal: "principal:lead", SemanticKind: "ops.request",
		PayloadBytes: len("inspect remote service"), Consequence: "handling.create",
		Targets: []string{"principal:lead", "remote/data"}}
	deliveryID := "delivery:" + strings.Repeat("e", 64)
	outbox := deliveryFromOrigin(origin, "lead", "outbox", deliveryID,
		"route:lead-data", testDigest("outbox-envelope"))
	remote := eventEvidence{Node: "data", ID: "event:remote-receipt-effect",
		Digest: testDigest("remote-receipt-effect"), AcceptedAt: accepted.Add(time.Minute),
		OriginSequence: 1, SourcePrincipal: "principal:lead-surrogate",
		OperationKey: deliveryID, RequestDigest: outbox.EnvelopeDigest,
		CausalDepth: outbox.OriginCausalDepth, SemanticKind: "ops.request",
		PayloadBytes: origin.PayloadBytes, Consequence: "handling.create",
		Targets:   []string{"principal:data"},
		Causation: []eventRefWire{{ID: origin.ID, Digest: origin.Digest}}}
	inbox := outbox
	inbox.Node, inbox.Direction = "data", "inbox"
	outbox.LocalEventID, outbox.LocalEventDigest, outbox.Accepted = remote.ID, remote.Digest, true
	inbox.LocalEventID, inbox.LocalEventDigest, inbox.Accepted = remote.ID, remote.Digest, true
	nodes := []nodeEvidence{{Role: "lead", Events: []eventEvidence{origin},
		Deliveries: []deliveryEvidence{outbox}}, {Role: "data", Events: []eventEvidence{remote},
		Deliveries: []deliveryEvidence{inbox}}}
	global := map[string]eventEvidence{origin.ID: origin, remote.ID: remote}
	return nodes, global
}

func setPairLocalEvent(nodes []nodeEvidence, event eventEvidence) {
	for nodeIndex := range nodes {
		for deliveryIndex := range nodes[nodeIndex].Deliveries {
			if nodes[nodeIndex].Deliveries[deliveryIndex].ID == nodes[0].Deliveries[0].ID {
				nodes[nodeIndex].Deliveries[deliveryIndex].LocalEventID = event.ID
				nodes[nodeIndex].Deliveries[deliveryIndex].LocalEventDigest = event.Digest
			}
		}
	}
}
