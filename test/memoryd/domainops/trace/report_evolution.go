package main

import (
	"errors"
	"slices"

	"github.com/JayWanX/memory/internal/agency"
)

func validateEvolutionSummary(summary evolutionSummary) error {
	if summary.Boundary.ActiveHeadCount < 0 || summary.AcceptedReferenceUses < 0 ||
		len(summary.Boundary.Nodes) != len(domainRoles) ||
		len(summary.Effects) != len(domainRoles) {
		return errors.New("sanitized live report has incomplete evolution evidence")
	}
	boundaryByRole, headCount, err := validateEvolutionBoundarySummary(summary.Boundary.Nodes)
	if err != nil {
		return err
	}
	if headCount != summary.Boundary.ActiveHeadCount {
		return errors.New("sanitized live report evolution head total is inconsistent")
	}
	effectTotal, err := validateEvolutionEffectSummary(boundaryByRole, summary.Effects)
	if err != nil {
		return err
	}
	if effectTotal != summary.AcceptedReferenceUses {
		return errors.New("sanitized live report evolution effect total is inconsistent")
	}
	demonstrated := summary.Boundary.ActiveHeadCount > 0 && summary.AcceptedReferenceUses > 0
	if summary.Demonstrated != demonstrated {
		return errors.New("sanitized live report evolution observation is inconsistent")
	}
	return nil
}

func validateEvolutionBoundarySummary(values []evolutionBoundaryNode) (
	map[string]evolutionBoundaryNode, int, error,
) {
	byRole := make(map[string]evolutionBoundaryNode, len(domainRoles))
	headCount := 0
	for _, node := range values {
		if !slices.Contains(domainRoles, node.Role) {
			return nil, 0, errors.New("sanitized live report has an unknown evolution boundary role")
		}
		if _, duplicate := byRole[node.Role]; duplicate {
			return nil, 0, errors.New("sanitized live report repeats an evolution boundary role")
		}
		if node.ConsolidationAfterSequence > node.MaxOriginSequence {
			return nil, 0, errors.New("sanitized live report has a regressed evolution boundary")
		}
		if err := validateEvolutionHeads(node.ActiveHeads); err != nil {
			return nil, 0, err
		}
		headCount += len(node.ActiveHeads)
		byRole[node.Role] = node
	}
	return byRole, headCount, nil
}

func validateEvolutionHeads(values []evolutionReferenceHead) error {
	seen := make(map[string]struct{}, len(values))
	for _, head := range values {
		if _, err := agency.NewEventID(head.EventID); err != nil {
			return err
		}
		if _, err := agency.ParseDigest(head.EventDigest); err != nil {
			return err
		}
		if _, duplicate := seen[head.EventID]; duplicate {
			return errors.New("sanitized live report repeats a boundary Reference head")
		}
		seen[head.EventID] = struct{}{}
	}
	return nil
}

func validateEvolutionEffectSummary(boundaryByRole map[string]evolutionBoundaryNode,
	values []evolutionNodeSummary,
) (int, error) {
	total := 0
	seen := make(map[string]struct{}, len(domainRoles))
	for _, node := range values {
		boundary, exists := boundaryByRole[node.Role]
		if !exists || node.BoundarySequence != boundary.MaxOriginSequence ||
			node.ActiveHeadCount != len(boundary.ActiveHeads) ||
			node.AcceptedReferenceUses != len(node.Matches) {
			return 0, errors.New("sanitized live report evolution node differs from its boundary")
		}
		if _, duplicate := seen[node.Role]; duplicate {
			return 0, errors.New("sanitized live report repeats an evolution effect role")
		}
		seen[node.Role] = struct{}{}
		if err := validateEvolutionMatchValues(node.Matches); err != nil {
			return 0, err
		}
		total += node.AcceptedReferenceUses
	}
	return total, nil
}

func validateEvolutionMatchValues(values []evolutionMatchReport) error {
	for _, match := range values {
		if _, err := agency.NewEventID(match.EventID); err != nil {
			return err
		}
		if _, err := agency.NewEventID(match.ReferenceEventID); err != nil {
			return err
		}
		if _, err := agency.ParseDigest(match.ReferenceDigest); err != nil {
			return err
		}
	}
	return nil
}
