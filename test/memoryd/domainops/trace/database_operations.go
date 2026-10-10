package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/JayWanX/memory/internal/agency"
)

type operationRow struct {
	Actor, OperationKey, RequestDigest, Outcome, ReceiptDigest, RecordedAt string
	EventID                                                                sql.NullString
	Canonical                                                              []byte
}

func loadOperations(ctx context.Context, db *sql.DB, role string,
	events map[string]eventEvidence,
) ([]operationEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT actor_principal_id, operation_key,
		request_digest, outcome, event_id, receipt_digest, receipt_json, recorded_at
		FROM operations ORDER BY recorded_at, actor_principal_id, operation_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []operationEvidence
	for rows.Next() {
		var row operationRow
		if err := rows.Scan(&row.Actor, &row.OperationKey, &row.RequestDigest, &row.Outcome,
			&row.EventID, &row.ReceiptDigest, &row.Canonical, &row.RecordedAt); err != nil {
			return nil, err
		}
		value, err := parseOperationRow(role, row, events)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func parseOperationRow(role string, row operationRow,
	events map[string]eventEvidence,
) (operationEvidence, error) {
	if _, err := agency.NewAgentPrincipalID(row.Actor); err != nil {
		return operationEvidence{}, err
	}
	receipt, err := agency.ParseReceiptCanonicalJSON(row.Canonical)
	if err != nil || receipt.Digest().String() != row.ReceiptDigest ||
		receipt.OperationKey().String() != row.OperationKey ||
		receipt.RequestDigest().String() != row.RequestDigest ||
		receipt.Outcome().String() != row.Outcome {
		return operationEvidence{}, fmt.Errorf("%s operation Receipt differs from durable columns", role)
	}
	storedAt, err := parseStoredTime("operation recorded_at", row.RecordedAt)
	if err != nil || !storedAt.Equal(receipt.RecordedAt()) {
		return operationEvidence{}, fmt.Errorf("%s operation Receipt time differs from durable authority", role)
	}
	value := operationEvidence{Node: role, Digest: row.ReceiptDigest, Outcome: row.Outcome,
		RecordedAt: storedAt, Code: receipt.Code().String()}
	if row.Outcome == "accepted" {
		return bindAcceptedOperation(role, row, receipt, value, events)
	}
	if row.EventID.Valid {
		return operationEvidence{}, fmt.Errorf("%s rejected Receipt unexpectedly names an Event", role)
	}
	return value, nil
}

func bindAcceptedOperation(role string, row operationRow, receipt agency.Receipt,
	value operationEvidence, events map[string]eventEvidence,
) (operationEvidence, error) {
	if !row.EventID.Valid {
		return operationEvidence{}, fmt.Errorf("%s accepted Receipt has no Event", role)
	}
	event, exists := events[row.EventID.String]
	reference, present := receipt.Event()
	if !exists || !present || reference.ID().String() != event.ID ||
		reference.Digest().String() != event.Digest || event.SourcePrincipal != row.Actor ||
		event.OperationKey != row.OperationKey || event.RequestDigest != row.RequestDigest ||
		value.RecordedAt.Before(event.AcceptedAt) {
		return operationEvidence{}, fmt.Errorf("%s accepted Receipt does not bind its exact Event", role)
	}
	value.EventID, value.EventDigest = event.ID, event.Digest
	return value, nil
}
