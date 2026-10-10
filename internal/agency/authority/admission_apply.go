package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

func insertEventTx(ctx context.Context, tx *sql.Tx, event agency.Event) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(
		event_id, event_digest, origin_sequence, source_principal_id,
		request_digest, causal_depth, accepted_at, canonical_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID().String(), event.Digest().String(), event.OriginSequence(), event.Source().String(),
		event.RequestDigest().String(), event.CausalDepth(), formatTime(event.AcceptedAt()),
		event.CanonicalJSON()); err != nil {
		return fmt.Errorf("admit Intent: persist Event: %w", err)
	}
	for _, digest := range event.Artifacts() {
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_artifacts(event_id, artifact_digest)
			VALUES(?, ?)`, event.ID().String(), digest.String()); err != nil {
			return fmt.Errorf("admit Intent: pin Artifact: %w", err)
		}
	}
	return nil
}

func applyDomainEffectTx(ctx context.Context, tx *sql.Tx, event agency.Event,
	claimAttachment agency.AttachmentID, handlingIDs []agency.HandlingID,
) error {
	consequence := event.Consequence()
	switch consequence {
	case agency.ConsequenceCreateHandlings:
		return createSuccessorHandlingsTx(ctx, tx, event, handlingIDs)
	case agency.ConsequenceAdvanceHandling:
		if err := settleSubjectTx(ctx, tx, event, claimAttachment, "open", ""); err != nil {
			return err
		}
		return createSuccessorHandlingsTx(ctx, tx, event, handlingIDs)
	case agency.ConsequenceResolveCompleted:
		if err := settleSubjectTx(ctx, tx, event, claimAttachment, "terminal", "completed"); err != nil {
			return err
		}
		return createSuccessorHandlingsTx(ctx, tx, event, handlingIDs)
	case agency.ConsequenceResolveDeclined:
		if err := settleSubjectTx(ctx, tx, event, claimAttachment, "terminal", "declined"); err != nil {
			return err
		}
		return createSuccessorHandlingsTx(ctx, tx, event, handlingIDs)
	case agency.ConsequenceResolveUnresolved:
		if err := settleSubjectTx(ctx, tx, event, claimAttachment, "terminal", "unresolved"); err != nil {
			return err
		}
		return createSuccessorHandlingsTx(ctx, tx, event, handlingIDs)
	case agency.ConsequencePublishReference:
		return publishReferenceTx(ctx, tx, event)
	case agency.ConsequenceSupersedeReference:
		return supersedeReferenceTx(ctx, tx, event)
	case agency.ConsequenceRetractReference:
		return retractReferenceTx(ctx, tx, event)
	case agency.ConsequenceObserveCompleted, agency.ConsequenceObserveDeclined,
		agency.ConsequenceObserveUnresolved:
		if len(handlingIDs) != 0 {
			return errors.New("admit peer observation: successor cardinality must be zero")
		}
		return nil
	default:
		return errors.New("admit Event: unknown consequence")
	}
}

func createSuccessorHandlingsTx(ctx context.Context, tx *sql.Tx, event agency.Event,
	handlingIDs []agency.HandlingID,
) error {
	targets := event.Targets()
	if localTargetCount(targets) != len(handlingIDs) {
		return errors.New("admit Intent: successor ID cardinality mismatch")
	}
	index := 0
	for _, target := range targets {
		if target.Destination() == agency.TargetDestinationRemote {
			continue
		}
		if target.Destination() != agency.TargetDestinationLocal || target.LocalPrincipal().IsZero() {
			return errors.New("admit Intent: invalid successor reached local apply")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO handlings(
			handling_id, target_principal_id, head_event_id, state, created_sequence)
			VALUES(?, ?, ?, 'open', ?)`, handlingIDs[index].String(),
			target.LocalPrincipal().String(), event.ID().String(), event.OriginSequence()); err != nil {
			return fmt.Errorf("admit Intent: create successor Handling: %w", err)
		}
		index++
	}
	return nil
}

func settleSubjectTx(ctx context.Context, tx *sql.Tx, event agency.Event,
	claimAttachment agency.AttachmentID, state, outcome string,
) error {
	subject, present := event.Subject()
	if !present || claimAttachment.IsZero() {
		return errors.New("admit Intent: subject effect lacks bound subject")
	}
	var outcomeValue any
	if outcome != "" {
		outcomeValue = outcome
	}
	result, err := tx.ExecContext(ctx, `UPDATE handlings SET
		head_event_id = ?, state = ?, outcome = ?, claim_attachment_id = NULL, claim_until = NULL
		WHERE handling_id = ? AND state = 'open' AND head_event_id = ?
		AND claim_attachment_id = ? AND claim_fence = ?`, event.ID().String(), state, outcomeValue,
		subject.HandlingID().String(), subject.Head().ID().String(), claimAttachment.String(),
		subject.Fence())
	if err != nil {
		return fmt.Errorf("admit Intent: settle subject: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return errors.New("admit Intent: subject settlement cardinality violated")
	}
	return nil
}

func publishReferenceTx(ctx context.Context, tx *sql.Tx, event agency.Event) error {
	expected, present := event.ExpectedReference()
	artifacts := event.Artifacts()
	if !present || !expected.IsAbsent() || len(artifacts) != 1 {
		return errors.New("admit Intent: malformed Reference publish")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reference_lineage(
		event_id, reference_key, state, artifact_digest) VALUES(?, ?, 'active', ?)`,
		event.ID().String(), expected.Key().String(), artifacts[0].String()); err != nil {
		return fmt.Errorf("admit Intent: append Reference publish lineage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_references(
		reference_key, head_event_id, state, artifact_digest) VALUES(?, ?, 'active', ?)`,
		expected.Key().String(), event.ID().String(), artifacts[0].String()); err != nil {
		return fmt.Errorf("admit Intent: publish Reference head: %w", err)
	}
	return nil
}

func supersedeReferenceTx(ctx context.Context, tx *sql.Tx, event agency.Event) error {
	expected, present := event.ExpectedReference()
	artifacts := event.Artifacts()
	if !present || expected.IsAbsent() || len(artifacts) != 1 {
		return errors.New("admit Intent: malformed Reference supersede")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reference_lineage(
		event_id, reference_key, previous_event_id, state, artifact_digest)
		VALUES(?, ?, ?, 'active', ?)`, event.ID().String(), expected.Key().String(),
		expected.Head().ID().String(), artifacts[0].String()); err != nil {
		return fmt.Errorf("admit Intent: append Reference supersede lineage: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE active_references
		SET head_event_id = ?, state = 'active', artifact_digest = ?
		WHERE reference_key = ? AND head_event_id = ?`, event.ID().String(), artifacts[0].String(),
		expected.Key().String(), expected.Head().ID().String())
	if err != nil {
		return fmt.Errorf("admit Intent: supersede Reference head: %w", err)
	}
	return requireOneRow(result, "Reference supersede")
}

func retractReferenceTx(ctx context.Context, tx *sql.Tx, event agency.Event) error {
	expected, present := event.ExpectedReference()
	if !present || expected.IsAbsent() || len(event.Artifacts()) != 0 {
		return errors.New("admit Intent: malformed Reference retract")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reference_lineage(
		event_id, reference_key, previous_event_id, state)
		VALUES(?, ?, ?, 'retracted')`, event.ID().String(), expected.Key().String(),
		expected.Head().ID().String()); err != nil {
		return fmt.Errorf("admit Intent: append Reference retract lineage: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE active_references
		SET head_event_id = ?, state = 'retracted', artifact_digest = NULL
		WHERE reference_key = ? AND head_event_id = ? AND state = 'active'`, event.ID().String(),
		expected.Key().String(), expected.Head().ID().String())
	if err != nil {
		return fmt.Errorf("admit Intent: retract Reference head: %w", err)
	}
	return requireOneRow(result, "Reference retract")
}

func requireOneRow(result sql.Result, operation string) error {
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("admit Intent: %s cardinality violated", operation)
	}
	return nil
}
