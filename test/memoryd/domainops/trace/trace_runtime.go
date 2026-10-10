package main

import (
	"errors"
	"time"

	"github.com/JayWanX/memory/test/memoryd/observer"
)

func appendRuntimeFacts(writer *observer.Writer, turns []turnSummary) error {
	for _, turn := range turns {
		if err := appendRuntimeTurnFacts(writer, turn); err != nil {
			return err
		}
	}
	return nil
}

func appendRuntimeTurnFacts(writer *observer.Writer, turn turnSummary) error {
	capturedAt, err := parseReportTime("turn captured_at", turn.CapturedAt)
	if err != nil {
		return err
	}
	for _, fact := range projectRuntimeFacts(turn, capturedAt) {
		// Sanitized turn counters establish observations, not causal
		// relationships. In particular, no runtime Fact causes an Event.
		if len(fact.Causes) != 0 {
			return errors.New("runtime observation unexpectedly carries a causal edge")
		}
		if _, err := writer.Append(fact); err != nil {
			return err
		}
	}
	return nil
}

func projectRuntimeFacts(turn turnSummary, capturedAt time.Time) []observer.Fact {
	facts := make([]observer.Fact, 0, 10)
	if turn.HookCues > 0 {
		value := true
		facts = append(facts, runtimeFact(turn, capturedAt, "hook", "runtime.hook.cue",
			observer.TruthObservation, observer.FactFields{HookCue: &value}))
	}
	if turn.CurrentReads > 0 {
		fields := observer.FactFields{Action: "current",
			HasCurrent:       boolPointer(turn.View.HasCurrent),
			OpenTotal:        intPointer(turn.View.OpenTotal),
			RelatedTotal:     intPointer(turn.View.RelatedTotal),
			RelatedProjected: intPointer(turn.View.RelatedProjected),
			Truncated:        boolPointer(turn.View.Truncated)}
		if turn.View.ReplyRequired != nil {
			fields.ReplyRequired = boolPointer(*turn.View.ReplyRequired)
		}
		facts = append(facts, runtimeFact(turn, capturedAt, "view", "runtime.view.received",
			observer.TruthDerivedProjection, fields))
	}
	facts = append(facts, projectDomainOperationFacts(turn, capturedAt)...)
	if turn.DelegateCalls > 0 {
		facts = append(facts, runtimeFact(turn, capturedAt, "delegate", "runtime.delegate.invoked",
			observer.TruthObservation, observer.FactFields{}))
	}
	if turn.IntentSubmits > 0 {
		fact := runtimeFact(turn, capturedAt, "intent", "runtime.intent.submitted",
			observer.TruthObservation, observer.FactFields{Action: "submit"})
		if len(turn.AcceptedEvents) == 1 {
			fact.References.Event = turn.AcceptedEvents[0].ID
			fact.References.EventDigest = turn.AcceptedEvents[0].Digest
		}
		facts = append(facts, fact)
	}
	facts = append(facts, projectIntentDenialFacts(turn, capturedAt)...)
	return append(facts, runtimeFact(turn, capturedAt, "ended", "runtime.turn.ended",
		observer.TruthObservation, observer.FactFields{}))
}

func projectDomainOperationFacts(turn turnSummary, capturedAt time.Time) []observer.Fact {
	operations := []struct {
		action string
		value  domainOperationSummary
	}{
		{action: "read", value: turn.DomainOperations.Read},
		{action: "probe", value: turn.DomainOperations.Probe},
		{action: "mutation", value: turn.DomainOperations.Mutation},
	}
	facts := make([]observer.Fact, 0, len(operations))
	for _, operation := range operations {
		if operation.value.Attempts == 0 {
			continue
		}
		fields := observer.FactFields{Action: operation.action,
			AttemptCount:   intPointer(operation.value.Attempts),
			SuccessCount:   intPointer(operation.value.Successes),
			ToolErrorCount: intPointer(operation.value.ToolErrors),
			InvalidCount:   intPointer(operation.value.InvalidResults),
			BatchedCount:   intPointer(operation.value.Batched)}
		facts = append(facts, runtimeFact(turn, capturedAt, "domain-"+operation.action,
			"runtime.domain.operation", observer.TruthObservation, fields))
	}
	return facts
}

func projectIntentDenialFacts(turn turnSummary, capturedAt time.Time) []observer.Fact {
	facts := make([]observer.Fact, 0, len(turn.SubmitControlDenials))
	for _, denial := range turn.SubmitControlDenials {
		fields := observer.FactFields{Action: "submit", Code: denial.Code,
			Count: intPointer(denial.Count)}
		facts = append(facts, runtimeFact(turn, capturedAt, "intent-denied-"+denial.Code,
			"runtime.intent.denied", observer.TruthObservation, fields))
	}
	return facts
}

func runtimeFact(turn turnSummary, capturedAt time.Time, suffix, kind string,
	truth observer.TruthClass, fields observer.FactFields) observer.Fact {
	return observer.Fact{ID: runtimeFactID(turn, suffix), CapturedAt: capturedAt,
		Source: observer.Source{Class: observer.SourceRuntime, Node: turn.Role},
		Agent:  turn.Role, Turn: turn.Turn, Kind: kind, Truth: truth, Fields: fields}
}

func intPointer(value int) *int { return &value }

func boolPointer(value bool) *bool { return &value }
