package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/JayWanX/memory/test/memoryd/observer"
)

func appendDomainEffectFacts(writer *observer.Writer, nodes []nodeEvidence,
	eventFacts map[string]string, ordered []eventEvidence,
) error {
	handlingsByNodeSequence := make(map[string][]handlingEvidence)
	createdHandlingFacts := make(map[string]struct{})
	referencesByNodeEvent := make(map[string]referenceEvidence)
	for _, node := range nodes {
		for _, handling := range node.Handlings {
			key := fmt.Sprintf("%s:%d", node.Role, handling.CreatedSequence)
			handlingsByNodeSequence[key] = append(handlingsByNodeSequence[key], handling)
		}
		for _, reference := range node.References {
			referencesByNodeEvent[node.Role+"\x00"+reference.EventID] = reference
		}
	}
	for _, event := range ordered {
		if err := appendEventDomainEffect(writer, event, eventFacts[event.ID],
			handlingsByNodeSequence, createdHandlingFacts, referencesByNodeEvent); err != nil {
			return err
		}
	}
	for _, node := range nodes {
		for _, handling := range node.Handlings {
			if _, exists := createdHandlingFacts[node.Role+"\x00"+handling.ID]; !exists {
				return fmt.Errorf("Handling %q has no observable creating Event", handling.ID)
			}
		}
	}
	return nil
}

func appendEventDomainEffect(writer *observer.Writer, event eventEvidence, eventFact string,
	handlings map[string][]handlingEvidence, created map[string]struct{},
	references map[string]referenceEvidence,
) error {
	switch event.Consequence {
	case "handling.create":
		return appendCreatedHandlingFacts(writer, event, eventFact, handlings, created)
	case "handling.advance":
		if err := appendHandlingFact(writer, event, event.SubjectHandling,
			"r7.handling.advanced", "open", "", eventFact); err != nil {
			return err
		}
		return appendCreatedHandlingFacts(writer, event, eventFact, handlings, created)
	case "handling.resolve.completed", "handling.resolve.declined", "handling.resolve.unresolved":
		outcome := event.Consequence[len("handling.resolve."):]
		if err := appendHandlingFact(writer, event, event.SubjectHandling,
			"r7.handling.resolved", "terminal", outcome, eventFact); err != nil {
			return err
		}
		return appendCreatedHandlingFacts(writer, event, eventFact, handlings, created)
	case "reference.publish", "reference.supersede", "reference.retract":
		reference, exists := references[event.Node+"\x00"+event.ID]
		if !exists {
			return fmt.Errorf("Reference Event %q has no durable lineage row", event.ID)
		}
		return appendReferenceFact(writer, event, reference, eventFact)
	default:
		return nil
	}
}

func appendCreatedHandlingFacts(writer *observer.Writer, event eventEvidence, cause string,
	handlings map[string][]handlingEvidence, created map[string]struct{},
) error {
	key := fmt.Sprintf("%s:%d", event.Node, event.OriginSequence)
	for _, handling := range handlings[key] {
		if handling.ID == event.SubjectHandling {
			continue
		}
		if err := appendHandlingFact(writer, event, handling.ID,
			"r7.handling.created", "open", "", cause); err != nil {
			return err
		}
		created[event.Node+"\x00"+handling.ID] = struct{}{}
	}
	return nil
}

func appendReferenceFact(writer *observer.Writer, event eventEvidence,
	reference referenceEvidence, cause string,
) error {
	kind, state := "r7.reference.published", "active"
	if event.Consequence == "reference.supersede" {
		kind = "r7.reference.superseded"
	}
	if event.Consequence == "reference.retract" {
		kind, state = "r7.reference.retracted", "retracted"
	}
	refs := observer.References{Event: event.ID, EventDigest: event.Digest,
		ReferenceHead: event.ID}
	if reference.ArtifactDigest != "" {
		refs.Artifact = reference.ArtifactDigest
	}
	_, err := writer.Append(observer.Fact{
		ID: hashedFactID("r7.reference", event.Node, event.ID), CapturedAt: event.AcceptedAt,
		Source: observer.Source{Class: observer.SourceR7Authority, Node: event.Node},
		Kind:   kind, Truth: observer.TruthAcceptedLocalFact,
		Causes: []string{cause}, References: refs,
		Fields: observer.FactFields{State: state, SemanticKind: event.SemanticKind},
	})
	return err
}

func appendHandlingFact(writer *observer.Writer, event eventEvidence, handlingID,
	kind, state, outcome, cause string,
) error {
	if handlingID == "" {
		return errors.New("Handling effect has no durable Handling identity")
	}
	_, err := writer.Append(observer.Fact{
		ID:         hashedFactID("r7.handling", event.Node, event.ID, handlingID),
		CapturedAt: event.AcceptedAt,
		Source:     observer.Source{Class: observer.SourceR7Authority, Node: event.Node},
		Kind:       kind, Truth: observer.TruthAcceptedLocalFact, Causes: []string{cause},
		References: observer.References{Event: event.ID, EventDigest: event.Digest,
			Handling: handlingID},
		Fields: observer.FactFields{State: state, Outcome: outcome,
			SemanticKind: event.SemanticKind},
	})
	return err
}

func appendReceiptFacts(writer *observer.Writer, nodes []nodeEvidence,
	eventFacts map[string]string,
) ([]string, error) {
	var facts []string
	for _, node := range nodes {
		for _, receipt := range node.Operations {
			kind := "r7.receipt.rejected"
			causes := []string(nil)
			refs := observer.References{}
			if receipt.Outcome == "accepted" {
				kind = "r7.receipt.accepted"
				causes = []string{eventFacts[receipt.EventID]}
				refs.Event, refs.EventDigest = receipt.EventID, receipt.EventDigest
			}
			factID := hashedFactID("r7.receipt", node.Role, receipt.Digest)
			if _, err := writer.Append(observer.Fact{ID: factID, CapturedAt: receipt.RecordedAt,
				Source: observer.Source{Class: observer.SourceR7Authority, Node: node.Role},
				Kind:   kind, Truth: observer.TruthAcceptedLocalFact, Causes: causes, References: refs,
				Fields: observer.FactFields{Outcome: receipt.Outcome, Code: receipt.Code}}); err != nil {
				return nil, err
			}
			facts = append(facts, factID)
		}
	}
	return facts, nil
}

func appendDeliveryFacts(writer *observer.Writer, nodes []nodeEvidence,
	eventFacts map[string]string,
) ([]string, error) {
	var readmittedFacts []string
	for _, node := range nodes {
		for _, delivery := range node.Deliveries {
			causes := knownEventCauses(delivery, eventFacts)
			refs := observer.References{Delivery: delivery.ID, Event: delivery.OriginEventID,
				EventDigest: delivery.OriginEventDigest}
			state, kind := delivery.State, "r7.delivery.pending"
			if state == "staged" {
				state = "pending"
			}
			if delivery.State == "settled" {
				kind = "r7.delivery.settled"
			}
			if delivery.State == "expired" {
				kind = "r7.delivery.expired"
			}
			if delivery.Direction == "inbox" && delivery.Accepted {
				readmittedID := hashedFactID("r7.delivery.readmitted", node.Role, delivery.ID)
				localRefs := refs
				localRefs.Event, localRefs.EventDigest = delivery.LocalEventID, delivery.LocalEventDigest
				authenticated := true
				if _, err := writer.Append(observer.Fact{ID: readmittedID,
					CapturedAt: delivery.CapturedAt,
					Source:     observer.Source{Class: observer.SourceR7Authority, Node: node.Role},
					Kind:       "r7.delivery.readmitted", Truth: observer.TruthAcceptedLocalFact,
					Causes: causes, References: localRefs,
					Fields: observer.FactFields{State: "settled", Authenticated: &authenticated,
						Code: "inbox"}}); err != nil {
					return nil, err
				}
				readmittedFacts = append(readmittedFacts, readmittedID)
				causes = []string{readmittedID}
			}
			if _, err := writer.Append(observer.Fact{
				ID:         hashedFactID("r7.delivery", node.Role, delivery.Direction, delivery.ID),
				CapturedAt: delivery.CapturedAt,
				Source:     observer.Source{Class: observer.SourceR7Authority, Node: node.Role},
				Kind:       kind, Truth: observer.TruthAcceptedLocalFact, Causes: causes, References: refs,
				Fields: observer.FactFields{State: state, Code: delivery.Direction},
			}); err != nil {
				return nil, err
			}
		}
	}
	return readmittedFacts, nil
}

func knownEventCauses(delivery deliveryEvidence, eventFacts map[string]string) []string {
	values := []string{eventFacts[delivery.OriginEventID], eventFacts[delivery.LocalEventID]}
	result := make([]string, 0, 2)
	for _, value := range values {
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}
