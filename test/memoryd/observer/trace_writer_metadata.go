package observer

import "fmt"

func validateFactMetadata(fields FactFields, sequence int) error {
	if !validOptionalTokens(fields.Code, fields.Episode, fields.GateID, fields.Role,
		fields.SemanticKind) || len(fields.Targets) > 16 || !validOptionalTokens(fields.Targets...) {
		return fmt.Errorf("trace writer: fact %d has invalid metadata token", sequence)
	}
	if !validGoalObservation(fields.GoalDigest, fields.GoalSatisfied) {
		return fmt.Errorf("trace writer: fact %d has invalid goal observation", sequence)
	}
	return nil
}

func validGoalObservation(digest string, satisfied *bool) bool {
	if digest == "" {
		return satisfied == nil
	}
	return satisfied != nil && digestPattern.MatchString(digest)
}
