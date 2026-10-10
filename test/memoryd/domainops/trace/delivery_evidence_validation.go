package main

import (
	"errors"
	"slices"
)

func validateGlobalDeliveries(nodes []nodeEvidence, global map[string]eventEvidence) error {
	pairs, err := indexGlobalDeliveryPairs(nodes)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if err := validateNodeDeliveries(node, global, pairs); err != nil {
			return err
		}
	}
	return nil
}

type globalDeliveryPair struct {
	outbox    deliveryEvidence
	inbox     deliveryEvidence
	hasOutbox bool
	hasInbox  bool
}

func indexGlobalDeliveryPairs(nodes []nodeEvidence) (map[string]*globalDeliveryPair, error) {
	pairs := make(map[string]*globalDeliveryPair)
	for _, node := range nodes {
		for _, delivery := range node.Deliveries {
			if err := addGlobalDeliveryAuthority(pairs, delivery); err != nil {
				return nil, err
			}
		}
	}
	if err := validateGlobalDeliveryPairAuthorities(pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}

func addGlobalDeliveryAuthority(pairs map[string]*globalDeliveryPair,
	delivery deliveryEvidence,
) error {
	pair := pairs[delivery.ID]
	if pair == nil {
		pair = &globalDeliveryPair{}
		pairs[delivery.ID] = pair
	}
	switch delivery.Direction {
	case "outbox":
		return addGlobalOutboxAuthority(pair, delivery)
	case "inbox":
		return addGlobalInboxAuthority(pair, delivery)
	default:
		return errors.New("peer Delivery has an invalid evidence direction")
	}
}

func addGlobalOutboxAuthority(pair *globalDeliveryPair, delivery deliveryEvidence) error {
	if pair.hasOutbox {
		return errors.New("peer Delivery has more than one outbox authority")
	}
	pair.outbox, pair.hasOutbox = delivery, true
	return nil
}

func addGlobalInboxAuthority(pair *globalDeliveryPair, delivery deliveryEvidence) error {
	if pair.hasInbox {
		return errors.New("peer Delivery has more than one inbox authority")
	}
	pair.inbox, pair.hasInbox = delivery, true
	return nil
}

func validateGlobalDeliveryPairAuthorities(pairs map[string]*globalDeliveryPair) error {
	for _, pair := range pairs {
		if pair.hasOutbox && pair.hasInbox && !sameDeliveryAuthority(pair.outbox, pair.inbox) {
			return errors.New("peer Delivery outbox and inbox authority differ")
		}
		if err := validateGlobalDeliveryPairReceipts(pair); err != nil {
			return err
		}
	}
	return nil
}

func validateGlobalDeliveryPairReceipts(pair *globalDeliveryPair) error {
	if pair.hasInbox && pair.inbox.Accepted && !pair.hasOutbox {
		return errors.New("accepted inbox Receipt has no unique sender outbox authority")
	}
	if pair.hasOutbox && pair.outbox.State == "settled" &&
		(!pair.hasInbox || pair.inbox.State != "settled") {
		return errors.New("settled outbox Receipt has no settled receiver inbox authority")
	}
	if !pair.hasOutbox || !pair.hasInbox ||
		pair.outbox.State != "settled" || pair.inbox.State != "settled" {
		return nil
	}
	if pair.outbox.Accepted != pair.inbox.Accepted {
		return errors.New("settled outbox and inbox Receipt outcomes differ")
	}
	if pair.outbox.Accepted && (pair.outbox.LocalEventID != pair.inbox.LocalEventID ||
		pair.outbox.LocalEventDigest != pair.inbox.LocalEventDigest) {
		return errors.New("accepted outbox and inbox Receipts name different local effects")
	}
	return nil
}

func sameDeliveryAuthority(outbox, inbox deliveryEvidence) bool {
	return outbox.ID == inbox.ID && outbox.RouteID == inbox.RouteID &&
		outbox.EnvelopeDigest == inbox.EnvelopeDigest &&
		outbox.OriginEventID == inbox.OriginEventID &&
		outbox.OriginEventDigest == inbox.OriginEventDigest &&
		outbox.OriginSequence == inbox.OriginSequence &&
		outbox.OriginAcceptedAt.Equal(inbox.OriginAcceptedAt) &&
		outbox.OriginSource == inbox.OriginSource &&
		outbox.OriginConsequence == inbox.OriginConsequence &&
		outbox.OriginTargetCount == inbox.OriginTargetCount &&
		outbox.OriginCausalDepth == inbox.OriginCausalDepth &&
		outbox.OriginSemanticKind == inbox.OriginSemanticKind &&
		outbox.OriginPayloadBytes == inbox.OriginPayloadBytes &&
		slices.Equal(outbox.OriginArtifacts, inbox.OriginArtifacts) &&
		eventRefsEqual(outbox.OriginCausation, inbox.OriginCausation) &&
		optionalEventRefsEqual(outbox.OriginCorrelation, inbox.OriginCorrelation) &&
		outbox.InReplyToDeliveryID == inbox.InReplyToDeliveryID
}

type nodeDeliveryValidation struct {
	events          map[string]eventEvidence
	outbox          map[string]deliveryEvidence
	handlings       map[string]handlingEvidence
	acceptedReplies map[string]struct{}
}

func validateNodeDeliveries(node nodeEvidence, global map[string]eventEvidence,
	pairs map[string]*globalDeliveryPair,
) error {
	validation := indexNodeDeliveryValidation(node)
	for _, delivery := range node.Deliveries {
		if err := validateCollectedDelivery(delivery, global, &validation, pairs[delivery.ID]); err != nil {
			return err
		}
	}
	return nil
}

func indexNodeDeliveryValidation(node nodeEvidence) nodeDeliveryValidation {
	validation := nodeDeliveryValidation{
		events:          make(map[string]eventEvidence, len(node.Events)),
		outbox:          make(map[string]deliveryEvidence),
		handlings:       make(map[string]handlingEvidence, len(node.Handlings)),
		acceptedReplies: make(map[string]struct{}),
	}
	for _, event := range node.Events {
		validation.events[event.ID] = event
	}
	for _, handling := range node.Handlings {
		validation.handlings[handling.ID] = handling
	}
	for _, delivery := range node.Deliveries {
		if delivery.Direction == "outbox" {
			validation.outbox[delivery.ID] = delivery
		}
	}
	return validation
}

func validateCollectedDelivery(delivery deliveryEvidence, global map[string]eventEvidence,
	validation *nodeDeliveryValidation, pair *globalDeliveryPair,
) error {
	origin, exists := global[delivery.OriginEventID]
	if !exists || origin.Digest != delivery.OriginEventDigest {
		return errors.New("peer Delivery has no exact collected origin Event")
	}
	if delivery.EnvelopeDigest != "" {
		if err := validateDeliveryOrigin(origin, delivery); err != nil {
			return err
		}
	}
	return validateDeliveryLocalEffect(delivery, global, validation, pair)
}

func validateDeliveryLocalEffect(delivery deliveryEvidence, global map[string]eventEvidence,
	validation *nodeDeliveryValidation, pair *globalDeliveryPair,
) error {
	if delivery.Direction != "inbox" && delivery.Direction != "outbox" {
		return errors.New("peer Delivery has an invalid evidence direction")
	}
	hasLocalEvent := delivery.LocalEventID != "" || delivery.LocalEventDigest != ""
	if (delivery.LocalEventID == "") != (delivery.LocalEventDigest == "") {
		return errors.New("peer Receipt has a partial local Event reference")
	}
	if delivery.Accepted && delivery.State != "settled" {
		return errors.New("accepted Delivery does not have a settled Receipt")
	}
	if !delivery.Accepted && hasLocalEvent {
		return errors.New("only an accepted Delivery may name a Receipt local Event")
	}
	if delivery.LocalEventID == "" {
		if delivery.Accepted {
			return errors.New("accepted Delivery has no exact Receipt local Event")
		}
		return nil
	}
	local, exists := global[delivery.LocalEventID]
	if !exists || local.Digest != delivery.LocalEventDigest {
		return errors.New("peer Receipt local Event differs from collected Event")
	}
	if delivery.Direction == "outbox" {
		return validateAcceptedOutboxEffect(delivery, local, pair)
	}
	return validateReadmittedEvent(local, delivery, validation)
}

func validateAcceptedOutboxEffect(outbox deliveryEvidence, local eventEvidence,
	pair *globalDeliveryPair,
) error {
	if pair == nil || !pair.hasOutbox || !pair.hasInbox {
		return errors.New("accepted outbox Receipt has no unique receiver inbox authority")
	}
	if local.Node != pair.inbox.Node || local.Node == outbox.Node {
		return errors.New("accepted outbox Receipt local Event is not receiver-local")
	}
	return nil
}

func validateDeliveryOrigin(origin eventEvidence, delivery deliveryEvidence) error {
	if origin.OriginSequence != delivery.OriginSequence ||
		!origin.AcceptedAt.Equal(delivery.OriginAcceptedAt) ||
		origin.SourcePrincipal != delivery.OriginSource ||
		origin.Consequence != delivery.OriginConsequence ||
		len(origin.Targets) != delivery.OriginTargetCount ||
		int(origin.CausalDepth)+1 != int(delivery.OriginCausalDepth) ||
		origin.SemanticKind != delivery.OriginSemanticKind ||
		origin.PayloadBytes != delivery.OriginPayloadBytes ||
		!slices.Equal(origin.Artifacts, delivery.OriginArtifacts) ||
		!eventRefsEqual(origin.Causation, delivery.OriginCausation) ||
		!optionalEventRefsEqual(origin.Correlation, delivery.OriginCorrelation) {
		return errors.New("peer Delivery signed origin differs from collected Event authority")
	}
	return nil
}

func validateReadmittedEvent(local eventEvidence, delivery deliveryEvidence,
	validation *nodeDeliveryValidation,
) error {
	nodeLocal, exists := validation.events[local.ID]
	if !exists || nodeLocal.Digest != local.Digest || local.Node != delivery.Node ||
		local.OperationKey != delivery.ID || local.RequestDigest != delivery.EnvelopeDigest ||
		local.CausalDepth != delivery.OriginCausalDepth ||
		local.SemanticKind != delivery.OriginSemanticKind ||
		local.PayloadBytes != delivery.OriginPayloadBytes ||
		!slices.Equal(local.Artifacts, delivery.OriginArtifacts) ||
		len(local.Causation) != 1 || local.Causation[0].ID != delivery.OriginEventID ||
		local.Causation[0].Digest != delivery.OriginEventDigest ||
		!optionalEventRefsEqual(local.Correlation, delivery.OriginCorrelation) {
		return errors.New("readmitted local Event differs from signed peer candidate")
	}
	if delivery.InReplyToDeliveryID == "" {
		return validateOrdinaryReadmittedEvent(local)
	}
	return validateTerminalReplyObservation(local, delivery, validation)
}

func validateOrdinaryReadmittedEvent(local eventEvidence) error {
	if local.InReplyToDelivery != "" || local.Consequence != "handling.create" ||
		len(local.Targets) != 1 || local.SubjectHandling != "" || local.ReferenceKey != "" {
		return errors.New("ordinary peer Delivery did not create exactly one local Handling Event")
	}
	return nil
}

func validateTerminalReplyObservation(local eventEvidence, delivery deliveryEvidence,
	validation *nodeDeliveryValidation,
) error {
	if _, duplicate := validation.acceptedReplies[delivery.InReplyToDeliveryID]; duplicate {
		return errors.New("more than one accepted terminal observation names one outbound Delivery")
	}
	validation.acceptedReplies[delivery.InReplyToDeliveryID] = struct{}{}
	request, exists := validation.outbox[delivery.InReplyToDeliveryID]
	if !exists || request.RouteID != delivery.RouteID || request.ReplyAnchorHandlingID == "" ||
		request.ExpectedReplyRootID == "" || request.ExpectedReplyRootDigest == "" {
		return errors.New("terminal observation has no exact local outbound reply binding")
	}
	if err := validateTerminalObservationAnchor(local, request, validation); err != nil {
		return err
	}
	if delivery.OriginCorrelation == nil ||
		delivery.OriginCorrelation.ID != request.ExpectedReplyRootID ||
		delivery.OriginCorrelation.Digest != request.ExpectedReplyRootDigest {
		return errors.New("terminal observation correlation differs from outbound reply root")
	}
	return validateTerminalObservationShape(local, delivery)
}

func validateTerminalObservationAnchor(local eventEvidence, request deliveryEvidence,
	validation *nodeDeliveryValidation,
) error {
	anchor, exists := validation.handlings[request.ReplyAnchorHandlingID]
	if !exists || anchor.CreatedSequence >= local.OriginSequence || anchor.HeadEventID == local.ID {
		return errors.New("terminal observation has no older unchanged local reply anchor")
	}
	head, exists := validation.events[anchor.HeadEventID]
	if !exists || head.Node != local.Node {
		return errors.New("terminal observation reply anchor head is not same-node Event authority")
	}
	switch anchor.State {
	case "open":
		return nil
	case "terminal":
		// A stopped snapshot may contain a later explicit local decision. It
		// cannot prove the earlier peer observation was admitted against an
		// open anchor unless that terminal head is strictly later.
	default:
		return errors.New("terminal observation reply anchor has an invalid durable state")
	}
	if head.SubjectHandling != anchor.ID || head.OriginSequence <= local.OriginSequence ||
		!isTerminalHandlingConsequence(head.Consequence) {
		return errors.New("terminal observation reply anchor was not closed by a later exact local Event")
	}
	return nil
}

func isTerminalHandlingConsequence(consequence string) bool {
	switch consequence {
	case "handling.resolve.completed", "handling.resolve.declined", "handling.resolve.unresolved":
		return true
	default:
		return false
	}
}

func validateTerminalObservationShape(local eventEvidence, delivery deliveryEvidence) error {
	wantConsequence, ok := observationConsequence(delivery.OriginConsequence)
	if !ok || local.Consequence != wantConsequence || local.InReplyToDelivery != delivery.InReplyToDeliveryID ||
		len(local.Targets) != 0 || local.SubjectHandling != "" || local.ReferenceKey != "" {
		return errors.New("terminal reply did not produce the exact zero-target observation Event")
	}
	return nil
}

func observationConsequence(origin string) (string, bool) {
	switch origin {
	case "handling.resolve.completed":
		return "observation.completed", true
	case "handling.resolve.declined":
		return "observation.declined", true
	case "handling.resolve.unresolved":
		return "observation.unresolved", true
	default:
		return "", false
	}
}

func eventRefsEqual(left, right []eventRefWire) bool {
	return slices.EqualFunc(left, right, func(a, b eventRefWire) bool {
		return a == b
	})
}

func optionalEventRefsEqual(left, right *eventRefWire) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
