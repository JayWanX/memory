package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/JayWanX/memory/internal/agency"
	_ "modernc.org/sqlite"
)

const (
	authorityApplicationID = 0x4d4e5237
	authoritySchemaVersion = 13
)

type evidence struct {
	Report             liveReport
	Scenario           scenarioEvidence
	Nodes              []nodeEvidence
	ConsolidationNodes []nodeEvidence
	BoundaryNodes      []nodeEvidence
}

type nodeEvidence struct {
	Role        string
	Events      []eventEvidence
	Artifacts   []artifactEvidence
	Operations  []operationEvidence
	Handlings   []handlingEvidence
	References  []referenceEvidence
	Deliveries  []deliveryEvidence
	PeerEffects int
}

type artifactEvidence struct {
	Node       string
	Digest     string
	ByteSize   int64
	VerifiedAt time.Time
}

type operationEvidence struct {
	Node        string
	Digest      string
	Outcome     string
	RecordedAt  time.Time
	EventID     string
	EventDigest string
	Code        string
}

type handlingEvidence struct {
	Node            string
	ID              string
	TargetPrincipal string
	HeadEventID     string
	State           string
	Outcome         string
	CreatedSequence uint64
}

type referenceEvidence struct {
	Node            string
	EventID         string
	PreviousEventID string
	State           string
	ArtifactDigest  string
}

type deliveryEvidence struct {
	Node                    string
	Direction               string
	ID                      string
	RouteID                 string
	State                   string
	CapturedAt              time.Time
	EnvelopeDigest          string
	OriginEventID           string
	OriginEventDigest       string
	OriginSequence          uint64
	OriginAcceptedAt        time.Time
	OriginSource            string
	OriginConsequence       string
	OriginTargetCount       int
	OriginCausalDepth       uint16
	OriginSemanticKind      string
	OriginPayloadBytes      int
	OriginArtifacts         []string
	OriginCausation         []eventRefWire
	OriginCorrelation       *eventRefWire
	InReplyToDeliveryID     string
	ReplyAnchorHandlingID   string
	ExpectedReplyRootID     string
	ExpectedReplyRootDigest string
	LocalEventID            string
	LocalEventDigest        string
	Accepted                bool
}

func loadEvidence(reportPath, authorityRoot, consolidationRoot, boundaryRoot string) (evidence, error) {
	report, err := loadReport(reportPath)
	if err != nil {
		return evidence{}, err
	}
	nodes, err := loadAuthorityNodes(authorityRoot)
	if err != nil {
		return evidence{}, err
	}
	consolidation, err := loadAuthorityNodes(consolidationRoot)
	if err != nil {
		return evidence{}, fmt.Errorf("load consolidation authority: %w", err)
	}
	boundary, err := loadAuthorityNodes(boundaryRoot)
	if err != nil {
		return evidence{}, fmt.Errorf("load evolution boundary authority: %w", err)
	}
	proof := evidence{Report: report, Nodes: nodes,
		ConsolidationNodes: consolidation, BoundaryNodes: boundary}
	if err := validateCombinedEvidence(proof); err != nil {
		return evidence{}, err
	}
	return proof, nil
}

func loadAuthorityNodes(authorityRoot string) ([]nodeEvidence, error) {
	root, err := inspectAuthorityRoot(authorityRoot)
	if err != nil {
		return nil, err
	}
	nodes := make([]nodeEvidence, 0, len(domainRoles))
	for _, role := range domainRoles {
		node, err := loadNodeDatabase(role, filepath.Join(root, role, "agency.db"))
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := validateAuthoritySummary(nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

func inspectAuthorityRoot(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("authority root is not a real directory")
	}
	return path, nil
}

func loadNodeDatabase(role, path string) (nodeEvidence, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nodeEvidence{}, fmt.Errorf("%s authority database is not a regular stopped snapshot", role)
	}
	databaseURL := url.URL{Scheme: "file", Path: path}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(ON)")
	query.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nodeEvidence{}, fmt.Errorf("open %s authority database: %w", role, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := validateDatabaseIdentity(ctx, db, role); err != nil {
		return nodeEvidence{}, err
	}
	node := nodeEvidence{Role: role}
	if node.Events, err = loadEvents(ctx, db, role); err != nil {
		return nodeEvidence{}, err
	}
	events := make(map[string]eventEvidence, len(node.Events))
	for _, event := range node.Events {
		events[event.ID] = event
	}
	if node.Artifacts, err = loadArtifacts(ctx, db, role, events); err != nil {
		return nodeEvidence{}, err
	}
	if node.Operations, err = loadOperations(ctx, db, role, events); err != nil {
		return nodeEvidence{}, err
	}
	if node.Handlings, err = loadHandlings(ctx, db, role, events); err != nil {
		return nodeEvidence{}, err
	}
	if node.References, err = loadReferences(ctx, db, role, events); err != nil {
		return nodeEvidence{}, err
	}
	if node.Deliveries, node.PeerEffects, err = loadDeliveries(ctx, db, role, events); err != nil {
		return nodeEvidence{}, err
	}
	return node, nil
}

func validateDatabaseIdentity(ctx context.Context, db *sql.DB, role string) error {
	var applicationID, version int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if applicationID != authorityApplicationID || version != authoritySchemaVersion {
		return fmt.Errorf("%s authority database identity = (%d,%d), want (%d,%d)", role,
			applicationID, version, authorityApplicationID, authoritySchemaVersion)
	}
	var quickCheck string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&quickCheck); err != nil {
		return err
	}
	if quickCheck != "ok" {
		return fmt.Errorf("%s authority quick_check failed: %s", role, quickCheck)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%s authority database has a foreign-key violation", role)
	}
	return rows.Err()
}

func loadEvents(ctx context.Context, db *sql.DB, role string) ([]eventEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT event_id, event_digest, origin_sequence,
		source_principal_id, request_digest, causal_depth, accepted_at, canonical_json
		FROM events ORDER BY origin_sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []eventEvidence
	for rows.Next() {
		var row storedEventRow
		if err := rows.Scan(&row.ID, &row.Digest, &row.OriginSequence, &row.SourcePrincipal,
			&row.RequestDigest, &row.CausalDepth, &row.AcceptedAt, &row.Canonical); err != nil {
			return nil, err
		}
		event, err := parseStoredEvent(role, row)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func loadArtifacts(ctx context.Context, db *sql.DB, role string,
	events map[string]eventEvidence,
) ([]artifactEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT verified.digest, verified.byte_size,
		verified.verified_at, links.event_id FROM verified_artifacts AS verified
		LEFT JOIN event_artifacts AS links ON links.artifact_digest = verified.digest
		ORDER BY verified.digest, links.event_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	unique := make(map[string]artifactEvidence)
	byEvent := make(map[string][]string)
	for rows.Next() {
		var digest, verifiedAt string
		var byteSize int64
		var eventID sql.NullString
		if err := rows.Scan(&digest, &byteSize, &verifiedAt, &eventID); err != nil {
			return nil, err
		}
		if _, err := agency.ParseDigest(digest); err != nil || byteSize < 0 {
			return nil, fmt.Errorf("%s has an invalid verified Artifact", role)
		}
		timestamp, err := parseStoredTime("Artifact verified_at", verifiedAt)
		if err != nil {
			return nil, err
		}
		unique[digest] = artifactEvidence{Node: role, Digest: digest,
			ByteSize: byteSize, VerifiedAt: timestamp}
		if eventID.Valid {
			if _, exists := events[eventID.String]; !exists {
				return nil, fmt.Errorf("%s Artifact links an unknown Event", role)
			}
			byEvent[eventID.String] = append(byEvent[eventID.String], digest)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for eventID, event := range events {
		if !equalStringsAsSet(event.Artifacts, byEvent[eventID]) {
			return nil, fmt.Errorf("%s Event %q Artifact closure differs from authority links", role, eventID)
		}
	}
	result := make([]artifactEvidence, 0, len(unique))
	for _, artifact := range unique {
		result = append(result, artifact)
	}
	slices.SortFunc(result, func(left, right artifactEvidence) int {
		return compareStrings(left.Digest, right.Digest)
	})
	return result, nil
}

func loadHandlings(ctx context.Context, db *sql.DB, role string,
	events map[string]eventEvidence,
) ([]handlingEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT handling_id, target_principal_id,
		head_event_id, state, COALESCE(outcome, ''), created_sequence
		FROM handlings ORDER BY created_sequence, handling_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []handlingEvidence
	for rows.Next() {
		var value handlingEvidence
		var created int64
		value.Node = role
		if err := rows.Scan(&value.ID, &value.TargetPrincipal, &value.HeadEventID,
			&value.State, &value.Outcome, &created); err != nil {
			return nil, err
		}
		if _, err := agency.NewHandlingID(value.ID); err != nil {
			return nil, err
		}
		if _, err := agency.NewAgentPrincipalID(value.TargetPrincipal); err != nil {
			return nil, err
		}
		if _, exists := events[value.HeadEventID]; !exists || created <= 0 {
			return nil, fmt.Errorf("%s Handling has invalid durable Event authority", role)
		}
		value.CreatedSequence = uint64(created)
		result = append(result, value)
	}
	return result, rows.Err()
}

func loadReferences(ctx context.Context, db *sql.DB, role string,
	events map[string]eventEvidence,
) ([]referenceEvidence, error) {
	rows, err := db.QueryContext(ctx, `SELECT event_id, reference_key,
		previous_event_id, state, artifact_digest FROM reference_lineage ORDER BY event_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []referenceEvidence
	for rows.Next() {
		var eventID, key, state string
		var previous, artifact sql.NullString
		if err := rows.Scan(&eventID, &key, &previous, &state, &artifact); err != nil {
			return nil, err
		}
		event, exists := events[eventID]
		if !exists || event.ReferenceKey != key || event.ReferenceHead != previous.String {
			return nil, fmt.Errorf("%s Reference lineage differs from its exact Event CAS", role)
		}
		if previous.Valid {
			previousEvent, found := events[previous.String]
			if !found || previousEvent.Digest != event.ReferenceDigest {
				return nil, fmt.Errorf("%s Reference lineage head digest is not exact", role)
			}
		}
		value := referenceEvidence{Node: role, EventID: eventID,
			PreviousEventID: previous.String, State: state, ArtifactDigest: artifact.String}
		if state == "active" && (len(event.Artifacts) != 1 || artifact.String != event.Artifacts[0]) {
			return nil, fmt.Errorf("%s active Reference has inconsistent Artifact authority", role)
		}
		if state == "retracted" && artifact.Valid {
			return nil, fmt.Errorf("%s retracted Reference retains an Artifact", role)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func compareStrings(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
