package main

import "errors"

func validateEvolutionEvidence(proof evidence) error {
	nodes := make(map[string]nodeEvidence, len(proof.Nodes))
	for _, node := range proof.Nodes {
		nodes[node.Role] = node
	}
	consolidation, err := indexEvolutionSnapshot(proof.ConsolidationNodes)
	if err != nil {
		return err
	}
	episodeBoundary, err := indexEvolutionSnapshot(proof.BoundaryNodes)
	if err != nil {
		return err
	}
	boundary, err := validateEvolutionBoundary(nodes, consolidation, episodeBoundary,
		proof.Report.Protocol.Evolution.Boundary.Nodes)
	if err != nil {
		return err
	}
	total, err := validateEvolutionEffects(nodes, boundary,
		proof.Report.Protocol.Evolution.Effects)
	if err != nil {
		return err
	}
	if total != proof.Report.Protocol.Evolution.AcceptedReferenceUses {
		return errors.New("stopped authority Reference uses differ from sanitized live report")
	}
	demonstrated := proof.Report.Protocol.Evolution.Boundary.ActiveHeadCount > 0 && total > 0
	if proof.Report.Protocol.Evolution.Demonstrated != demonstrated {
		return errors.New("stopped authority does not prove the reported evolution observation")
	}
	return nil
}

func indexEvolutionSnapshot(values []nodeEvidence) (map[string]nodeEvidence, error) {
	if len(values) != len(domainRoles) {
		return nil, errors.New("evolution authority snapshot is incomplete")
	}
	result := make(map[string]nodeEvidence, len(values))
	for _, node := range values {
		if _, duplicate := result[node.Role]; duplicate {
			return nil, errors.New("evolution authority snapshot repeats a role")
		}
		result[node.Role] = node
	}
	for _, role := range domainRoles {
		if _, exists := result[role]; !exists {
			return nil, errors.New("evolution authority snapshot omits a role")
		}
	}
	return result, nil
}

func validateEvolutionBoundary(nodes, consolidation, episodeBoundary map[string]nodeEvidence,
	values []evolutionBoundaryNode,
) (map[string]evolutionBoundaryNode, error) {
	boundary := make(map[string]evolutionBoundaryNode, len(domainRoles))
	for _, value := range values {
		node, exists := nodes[value.Role]
		if !exists {
			return nil, errors.New("evolution boundary names an absent authority node")
		}
		start, hasStart := consolidation[value.Role]
		captured, hasBoundary := episodeBoundary[value.Role]
		if !hasStart || !hasBoundary {
			return nil, errors.New("evolution boundary lacks an authority snapshot")
		}
		if err := validateEvolutionSnapshotBoundary(start, captured, value); err != nil {
			return nil, err
		}
		if err := validateEvolutionBoundaryNode(node, value); err != nil {
			return nil, err
		}
		boundary[value.Role] = value
	}
	return boundary, nil
}

func validateEvolutionSnapshotBoundary(start, boundary nodeEvidence,
	value evolutionBoundaryNode,
) error {
	if authorityMaxSequence(start.Events) != value.ConsolidationAfterSequence ||
		authorityMaxSequence(boundary.Events) != value.MaxOriginSequence {
		return errors.New("reported evolution coordinates differ from stopped boundary snapshots")
	}
	expected, err := replayEvolutionHeads(boundary, value)
	if err != nil {
		return err
	}
	return validateReportedEvolutionHeads(expected, value.ActiveHeads)
}

func validateEvolutionBoundaryNode(node nodeEvidence, value evolutionBoundaryNode) error {
	if value.MaxOriginSequence > authorityMaxSequence(node.Events) {
		return errors.New("evolution boundary exceeds stopped authority sequence")
	}
	expected, err := replayEvolutionHeads(node, value)
	if err != nil {
		return err
	}
	return validateReportedEvolutionHeads(expected, value.ActiveHeads)
}

func authorityMaxSequence(events []eventEvidence) uint64 {
	var maximum uint64
	for _, event := range events {
		if event.OriginSequence > maximum {
			maximum = event.OriginSequence
		}
	}
	return maximum
}

func replayEvolutionHeads(node nodeEvidence,
	value evolutionBoundaryNode,
) (map[string]string, error) {
	events := make(map[string]eventEvidence, len(node.Events))
	for _, event := range node.Events {
		events[event.ID] = event
	}
	type replayedReference struct {
		event eventEvidence
		state string
	}
	latest := make(map[string]replayedReference, len(node.References))
	for _, reference := range node.References {
		event, exists := events[reference.EventID]
		if !exists || event.ReferenceKey == "" || event.OriginSequence > value.MaxOriginSequence {
			continue
		}
		current, exists := latest[event.ReferenceKey]
		if exists && event.OriginSequence == current.event.OriginSequence &&
			event.ID != current.event.ID {
			return nil, errors.New("evolution boundary has ambiguous Reference order")
		}
		if !exists || event.OriginSequence > current.event.OriginSequence {
			latest[event.ReferenceKey] = replayedReference{event: event, state: reference.State}
		}
	}
	expected := make(map[string]string)
	for _, reference := range latest {
		if reference.state == "active" &&
			reference.event.OriginSequence > value.ConsolidationAfterSequence {
			expected[reference.event.ID] = reference.event.Digest
		}
	}
	return expected, nil
}

func validateReportedEvolutionHeads(expected map[string]string,
	reported []evolutionReferenceHead,
) error {
	if len(reported) != len(expected) {
		return errors.New("evolution boundary omits active stopped Reference lineage")
	}
	seen := make(map[string]struct{}, len(reported))
	for _, head := range reported {
		if digest, exists := expected[head.EventID]; !exists || digest != head.EventDigest {
			return errors.New("evolution boundary head differs from stopped Reference lineage")
		}
		if _, duplicate := seen[head.EventID]; duplicate {
			return errors.New("evolution boundary repeats stopped Reference lineage")
		}
		seen[head.EventID] = struct{}{}
	}
	return nil
}

func validateEvolutionEffects(nodes map[string]nodeEvidence,
	boundary map[string]evolutionBoundaryNode, values []evolutionNodeSummary,
) (int, error) {
	total := 0
	for _, reported := range values {
		node, exists := nodes[reported.Role]
		base, hasBoundary := boundary[reported.Role]
		if !exists || !hasBoundary {
			return 0, errors.New("evolution effect names an absent authority boundary")
		}
		expected := collectEvolutionMatches(node, base)
		if err := validateEvolutionMatches(expected, reported.Matches); err != nil {
			return 0, err
		}
		total += len(expected)
	}
	return total, nil
}

func collectEvolutionMatches(node nodeEvidence,
	base evolutionBoundaryNode,
) map[string]evolutionMatchReport {
	expected := make(map[string]evolutionMatchReport)
	for _, event := range node.Events {
		if event.OriginSequence <= base.MaxOriginSequence {
			continue
		}
		for _, head := range base.ActiveHeads {
			if !eventUsesReferenceHead(event, head) {
				continue
			}
			match := evolutionMatchReport{EventID: event.ID,
				ReferenceEventID: head.EventID, ReferenceDigest: head.EventDigest}
			expected[evolutionMatchKey(match)] = match
		}
	}
	return expected
}

func validateEvolutionMatches(expected map[string]evolutionMatchReport,
	reported []evolutionMatchReport,
) error {
	if len(expected) != len(reported) {
		return errors.New("reported evolution effects differ from stopped authority")
	}
	seen := make(map[string]struct{}, len(reported))
	for _, match := range reported {
		key := evolutionMatchKey(match)
		if _, duplicate := seen[key]; duplicate || expected[key] != match {
			return errors.New("reported evolution match is not an exact accepted Event edge")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func eventUsesReferenceHead(event eventEvidence, head evolutionReferenceHead) bool {
	for _, causal := range event.Causation {
		if causal.ID == head.EventID && causal.Digest == head.EventDigest {
			return true
		}
	}
	return event.ReferenceHead == head.EventID && event.ReferenceDigest == head.EventDigest
}

func evolutionMatchKey(match evolutionMatchReport) string {
	return match.EventID + "\x00" + match.ReferenceEventID + "\x00" + match.ReferenceDigest
}
