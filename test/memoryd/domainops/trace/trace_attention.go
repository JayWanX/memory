package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/test/memoryd/observer"
)

func appendSuccessfulAttentionFacts(writer *observer.Writer, values []attentionEnvelope,
	capturedAt time.Time,
) ([]string, error) {
	var facts []string
	for _, value := range values {
		if value.Status != "outcome_observed" || value.Goal == nil || !value.Goal.Satisfied {
			return nil, errors.New("successful trace has no satisfied attention outcome")
		}
		current, err := appendAttentionSnapshot(writer, &value, len(value.Waves)+1,
			value.TurnsUsed, "test.attention.outcome", value.Final, value.Goal, capturedAt)
		if err != nil {
			return nil, err
		}
		facts = append(facts, current...)
	}
	if len(facts) == 0 {
		return nil, errors.New("successful trace omits attention outcome evidence")
	}
	return facts, nil
}

func appendFailedAttentionFacts(writer *observer.Writer, value *attentionEnvelope,
	capturedAt time.Time,
) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	used := 0
	for _, wave := range value.Waves {
		if _, err := appendAttentionSnapshot(writer, value, wave.Wave, used,
			"test.attention.wave", wave.Nodes, nil, capturedAt); err != nil {
			return nil, err
		}
		for _, node := range wave.Nodes {
			if node.OpenUnclaimed > 0 {
				used++
			}
		}
	}
	finalKind := "test.attention.exhausted"
	switch value.Status {
	case "quiescent_without_outcome":
		finalKind = "test.attention.quiescent"
	case "claim_occupied":
		finalKind = "test.attention.occupied"
	}
	return appendAttentionSnapshot(writer, value, len(value.Waves)+1, value.TurnsUsed,
		finalKind, value.Final, value.Goal, capturedAt)
}

func appendAttentionSnapshot(writer *observer.Writer, value *attentionEnvelope,
	wave, used int, kind string, nodes []attentionNode, goal *attentionGoal,
	capturedAt time.Time,
) ([]string, error) {
	goalDigest := ""
	if goal != nil {
		canonical, err := json.Marshal(goal)
		if err != nil {
			return nil, err
		}
		goalDigest = agency.Sum(canonical).String()
	}
	facts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		factID := hashedFactID("attention", value.Episode, strconv.Itoa(wave), node.Role, kind)
		fields := observer.FactFields{
			Episode:        value.Episode,
			Role:           node.Role,
			Round:          intPointer(wave),
			OpenUnclaimed:  intPointer(node.OpenUnclaimed),
			OccupiedClaims: intPointer(node.OccupiedClaims),
			TurnLimit:      intPointer(value.TurnLimit),
			TurnsUsed:      intPointer(used),
		}
		if goal != nil {
			fields.GoalDigest = goalDigest
			fields.GoalSatisfied = boolPointer(goal.Satisfied)
		}
		if _, err := writer.Append(observer.Fact{ID: factID, CapturedAt: capturedAt,
			Source: observer.Source{Class: observer.SourceOracle, Node: "runner"},
			Kind:   kind, Truth: observer.TruthAssertion, Fields: fields}); err != nil {
			return nil, err
		}
		facts = append(facts, factID)
	}
	return facts, nil
}
