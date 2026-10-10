package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/JayWanX/memory/test/memoryd/observer"
)

func writeTrace(destination io.Writer, proof evidence) error {
	if proof.Scenario.Digest == "" {
		return errors.New("scenario evidence is required")
	}
	startedAt, _ := parseReportTime("started_at", proof.Report.Run.StartedAt)
	finishedAt, _ := parseReportTime("finished_at", proof.Report.Run.FinishedAt)
	participants := make([]observer.Participant, 0, len(domainRoles))
	for _, role := range domainRoles {
		participants = append(participants, observer.Participant{Node: role, Agent: role,
			Runtime: "pi", Model: proof.Report.Model})
	}
	writer, err := observer.NewWriter(destination, observer.Run{
		ID:        proof.Report.Run.ID,
		Scenario:  observer.Scenario{ID: scenarioID, Digest: proof.Scenario.Digest},
		StartedAt: startedAt, CandidateDigest: proof.Report.Run.CandidateDigest,
		Participants: participants,
	})
	if err != nil {
		return err
	}
	if err := appendRuntimeFacts(writer, proof.Report.Turns); err != nil {
		return err
	}
	artifactFacts, err := appendArtifactFacts(writer, proof.Nodes)
	if err != nil {
		return err
	}
	eventFacts, orderedEvents, err := appendEventFacts(writer, proof.Nodes, proof.Report.Turns)
	if err != nil {
		return err
	}
	if err := appendDomainEffectFacts(writer, proof.Nodes, eventFacts, orderedEvents); err != nil {
		return err
	}
	receiptFacts, err := appendReceiptFacts(writer, proof.Nodes, eventFacts)
	if err != nil {
		return err
	}
	deliveryFacts, err := appendDeliveryFacts(writer, proof.Nodes, eventFacts)
	if err != nil {
		return err
	}
	attentionFacts, err := appendSuccessfulAttentionFacts(writer,
		proof.Report.Protocol.Attention, finishedAt)
	if err != nil {
		return err
	}
	evolutionFacts, err := evolutionEffectFacts(proof.Report.Protocol.Evolution, eventFacts)
	if err != nil {
		return err
	}
	gateFacts, err := appendGateFacts(writer, finishedAt, receiptFacts, deliveryFacts,
		attentionFacts, evolutionFacts, proof.Report.Protocol.Evolution.Demonstrated,
		len(artifactFacts))
	if err != nil {
		return err
	}
	gates := []observer.Gate{
		{ID: "scenario.recovery", Status: observer.GatePass,
			Evidence: []string{gateFacts["scenario.recovery"]}},
		{ID: "scenario.service-receipts", Status: observer.GatePass,
			Evidence: []string{gateFacts["scenario.service-receipts"]}},
		{ID: "r7.operation-receipts", Status: observer.GatePass,
			Evidence: []string{gateFacts["r7.operation-receipts"]}},
		{ID: "r7.peer-accepted-effect", Status: observer.GatePass,
			Evidence: []string{gateFacts["r7.peer-accepted-effect"]}},
		{ID: "r7.delivery-quiescence", Status: observer.GatePass,
			Evidence: []string{gateFacts["r7.delivery-quiescence"]}},
		{ID: "scenario.isolation", Status: observer.GatePass,
			Evidence: []string{gateFacts["scenario.isolation"]}},
		{ID: "scenario.evolution", Status: evolutionGateStatus(
			proof.Report.Protocol.Evolution.Demonstrated),
			Evidence: []string{gateFacts["scenario.evolution"]}},
	}
	return writer.Finish(observer.Result{Status: observer.ResultPassed,
		FinishedAt: finishedAt, Gates: gates})
}

func evolutionEffectFacts(summary evolutionSummary, eventFacts map[string]string) ([]string, error) {
	if !summary.Demonstrated {
		if summary.AcceptedReferenceUses != 0 || evolutionMatchCount(summary.Effects) != 0 {
			return nil, errors.New("evolution is not demonstrated but reports accepted Reference use")
		}
		return nil, nil
	}
	if summary.Boundary.ActiveHeadCount < 1 || summary.AcceptedReferenceUses < 1 ||
		evolutionMatchCount(summary.Effects) != summary.AcceptedReferenceUses {
		return nil, errors.New("demonstrated evolution omits accepted Reference use")
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, summary.AcceptedReferenceUses)
	for _, node := range summary.Effects {
		for _, match := range node.Matches {
			effectFact, effectExists := eventFacts[match.EventID]
			referenceFact, referenceExists := eventFacts[match.ReferenceEventID]
			if !effectExists || !referenceExists {
				return nil, errors.New("evolution effect has no accepted Event Fact")
			}
			for _, fact := range []string{referenceFact, effectFact} {
				if _, duplicate := seen[fact]; duplicate {
					continue
				}
				seen[fact] = struct{}{}
				result = append(result, fact)
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("evolution effect evidence is empty")
	}
	return result, nil
}

func evolutionMatchCount(values []evolutionNodeSummary) int {
	total := 0
	for _, value := range values {
		total += len(value.Matches)
	}
	return total
}

func evolutionGateStatus(demonstrated bool) observer.GateStatus {
	if demonstrated {
		return observer.GatePass
	}
	return observer.GateNotApplicable
}

func appendArtifactFacts(writer *observer.Writer, nodes []nodeEvidence) ([]string, error) {
	var facts []string
	for _, node := range nodes {
		for _, artifact := range node.Artifacts {
			factID := hashedFactID("r7.artifact", node.Role, artifact.Digest)
			byteSize := artifact.ByteSize
			if _, err := writer.Append(observer.Fact{ID: factID, CapturedAt: artifact.VerifiedAt,
				Source: observer.Source{Class: observer.SourceR7Authority, Node: node.Role},
				Kind:   "r7.artifact.verified", Truth: observer.TruthAcceptedLocalFact,
				References: observer.References{Artifact: artifact.Digest},
				Fields:     observer.FactFields{ByteSize: &byteSize}}); err != nil {
				return nil, err
			}
			facts = append(facts, factID)
		}
	}
	return facts, nil
}

func appendEventFacts(writer *observer.Writer, nodes []nodeEvidence, turns []turnSummary) (
	map[string]string, []eventEvidence, error,
) {
	ordered, err := topologicalEvents(nodes)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]eventEvidence, len(ordered))
	factByEvent := make(map[string]string, len(ordered))
	turnByEvent := make(map[string]string)
	for _, turn := range turns {
		for _, event := range turn.AcceptedEvents {
			turnByEvent[event.ID] = turn.Turn
		}
	}
	for _, event := range ordered {
		byID[event.ID] = event
		factID := hashedFactID("r7.event", event.Node, event.ID, event.Digest)
		causes := make([]string, 0, len(event.Causation))
		for _, causal := range event.Causation {
			if _, known := byID[causal.ID]; known {
				if causeFact := factByEvent[causal.ID]; causeFact != "" {
					causes = append(causes, causeFact)
				}
			}
		}
		artifactCount := len(event.Artifacts)
		targetCount := len(event.Targets)
		payloadBytes := event.PayloadBytes
		references := observer.References{Event: event.ID, EventDigest: event.Digest}
		references.Principal = event.SourcePrincipal
		if len(event.Artifacts) > 0 {
			references.Artifact = event.Artifacts[0]
		}
		if event.Correlation != nil {
			references.Correlation = event.Correlation.ID
		}
		if event.ReferenceHead != "" {
			references.ReferenceHead = event.ReferenceHead
		}
		turn := turnByEvent[event.ID]
		agent := ""
		if turn != "" {
			agent = event.Node
		}
		if _, err := writer.Append(observer.Fact{ID: factID, CapturedAt: event.AcceptedAt,
			Source: observer.Source{Class: observer.SourceR7Authority, Node: event.Node},
			Agent:  agent, Turn: turn, Kind: "r7.event.accepted",
			Truth: observer.TruthAcceptedLocalFact, Causes: causes,
			References: references, Fields: observer.FactFields{SemanticKind: event.SemanticKind,
				Consequence: event.Consequence, ArtifactCount: &artifactCount,
				PayloadBytes: &payloadBytes, TargetCount: &targetCount,
				Targets: append([]string{}, event.Targets...)}}); err != nil {
			return nil, nil, err
		}
		factByEvent[event.ID] = factID
	}
	return factByEvent, ordered, nil
}

func appendGateFacts(writer *observer.Writer, capturedAt time.Time, receiptFacts,
	deliveryFacts, attentionFacts, evolutionFacts []string, evolutionDemonstrated bool,
	artifactCount int,
) (map[string]string, error) {
	if len(receiptFacts) == 0 || len(deliveryFacts) == 0 || len(attentionFacts) == 0 {
		return nil, errors.New("gate evidence is incomplete")
	}
	type gateSpec struct {
		id     string
		status string
		code   string
		causes []string
	}
	limit := func(values []string) []string {
		if len(values) > 16 {
			return append([]string{}, values[:16]...)
		}
		return append([]string{}, values...)
	}
	evolutionStatus, evolutionCode := "not_applicable", "no-reference-evolution-claimed"
	if evolutionDemonstrated {
		evolutionStatus, evolutionCode = "pass", "cross-session-reference-use"
	}
	gates := []gateSpec{
		{id: "scenario.recovery", status: "pass", code: "external-recovery",
			causes: limit(attentionFacts)},
		{id: "scenario.service-receipts", status: "pass", code: "exact-service-correspondence"},
		{id: "r7.operation-receipts", status: "pass", code: "canonical-operation-receipts",
			causes: limit(receiptFacts)},
		{id: "r7.peer-accepted-effect", status: "pass", code: "peer-readmission",
			causes: limit(deliveryFacts)},
		{id: "r7.delivery-quiescence", status: "pass", code: "bounded-empty-delivery-barriers"},
		{id: "scenario.isolation", status: "pass", code: "isolated-runtime"},
		{id: "scenario.evolution", status: evolutionStatus, code: evolutionCode,
			causes: limit(evolutionFacts)},
	}
	if artifactCount < 0 {
		return nil, errors.New("invalid Artifact evidence count")
	}
	result := make(map[string]string, len(gates))
	for _, gate := range gates {
		factID := hashedFactID("gate", gate.id)
		if _, err := writer.Append(observer.Fact{ID: factID, CapturedAt: capturedAt,
			Source: observer.Source{Class: observer.SourceOracle, Node: "runner"},
			Kind:   "test.gate.checked", Truth: observer.TruthAssertion, Causes: gate.causes,
			Fields: observer.FactFields{GateID: gate.id, Status: gate.status, Code: gate.code},
		}); err != nil {
			return nil, err
		}
		result[gate.id] = factID
	}
	return result, nil
}

func topologicalEvents(nodes []nodeEvidence) ([]eventEvidence, error) {
	all := make(map[string]eventEvidence)
	indegree := make(map[string]int)
	children := make(map[string][]string)
	for _, node := range nodes {
		for _, event := range node.Events {
			all[event.ID] = event
			indegree[event.ID] = 0
		}
	}
	for _, event := range all {
		seen := make(map[string]struct{})
		for _, causal := range event.Causation {
			if _, known := all[causal.ID]; !known {
				continue
			}
			if _, duplicate := seen[causal.ID]; duplicate {
				return nil, fmt.Errorf("Event %q repeats a causal Event", event.ID)
			}
			seen[causal.ID] = struct{}{}
			indegree[event.ID]++
			children[causal.ID] = append(children[causal.ID], event.ID)
		}
	}
	ready := make([]eventEvidence, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, all[id])
		}
	}
	sortEvents(ready)
	ordered := make([]eventEvidence, 0, len(all))
	for len(ready) > 0 {
		event := ready[0]
		ready = ready[1:]
		ordered = append(ordered, event)
		for _, child := range children[event.ID] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, all[child])
				sortEvents(ready)
			}
		}
	}
	if len(ordered) != len(all) {
		return nil, errors.New("accepted Event causation contains a cycle")
	}
	return ordered, nil
}

func sortEvents(values []eventEvidence) {
	slices.SortFunc(values, func(left, right eventEvidence) int {
		if left.CausalDepth != right.CausalDepth {
			return int(left.CausalDepth) - int(right.CausalDepth)
		}
		if left.Node != right.Node {
			return compareStrings(left.Node, right.Node)
		}
		if left.OriginSequence < right.OriginSequence {
			return -1
		}
		if left.OriginSequence > right.OriginSequence {
			return 1
		}
		return compareStrings(left.ID, right.ID)
	})
}

func hashedFactID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return "trace:" + prefix + "." + hex.EncodeToString(hash.Sum(nil))
}

func runtimeFactID(turn turnSummary, suffix string) string {
	return hashedFactID("runtime", turn.Role, turn.Turn, suffix)
}
