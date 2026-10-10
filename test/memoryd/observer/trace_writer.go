package observer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
)

// Writer serializes one complete metadata-only trace. It has one caller and is
// intentionally not concurrency-safe. A write error is terminal because a
// partially written JSONL stream cannot be repaired in place.
type Writer struct {
	destination io.Writer
	digest      hash.Hash
	seen        map[string]struct{}
	gateFacts   map[string]gateAssertion
	count       int
	closed      bool
	failure     error
}

type runLine struct {
	Schema          string        `json:"schema"`
	Version         int           `json:"version"`
	Record          string        `json:"record"`
	RunID           string        `json:"run_id"`
	Scenario        Scenario      `json:"scenario"`
	Redaction       string        `json:"redaction"`
	StartedAt       string        `json:"started_at"`
	CandidateDigest string        `json:"candidate_digest,omitempty"`
	Participants    []Participant `json:"participants"`
}

type factLine struct {
	Schema     string     `json:"schema"`
	Version    int        `json:"version"`
	Record     string     `json:"record"`
	Sequence   int        `json:"seq"`
	ID         string     `json:"id"`
	CapturedAt string     `json:"captured_at"`
	Source     Source     `json:"source"`
	Agent      string     `json:"agent,omitempty"`
	Turn       string     `json:"turn,omitempty"`
	Kind       string     `json:"kind"`
	Truth      TruthClass `json:"truth"`
	Causes     []string   `json:"causes"`
	Refs       References `json:"refs"`
	Facts      FactFields `json:"facts"`
}

type resultLine struct {
	Schema      string       `json:"schema"`
	Version     int          `json:"version"`
	Record      string       `json:"record"`
	Status      ResultStatus `json:"status"`
	FinishedAt  string       `json:"finished_at"`
	RecordCount int          `json:"record_count"`
	TraceDigest string       `json:"trace_digest"`
	Gates       []Gate       `json:"gates"`
}

// NewWriter validates and writes the run header. The destination receives
// canonical one-record-per-line JSON and must not be shared with another
// writer while the trace is open.
func NewWriter(destination io.Writer, run Run) (*Writer, error) {
	if destination == nil {
		return nil, fmt.Errorf("trace writer: destination is required")
	}
	startedAt, err := validateWriterRun(run)
	if err != nil {
		return nil, err
	}
	writer := &Writer{
		destination: destination,
		digest:      sha256.New(),
		seen:        make(map[string]struct{}),
		gateFacts:   make(map[string]gateAssertion),
	}
	line := runLine{
		Schema: traceSchema, Version: traceVersion, Record: "run", RunID: run.ID,
		Scenario: run.Scenario, Redaction: "metadata", StartedAt: startedAt,
		CandidateDigest: run.CandidateDigest,
		Participants:    append([]Participant{}, run.Participants...),
	}
	if err := writer.writePrefix(line); err != nil {
		return nil, err
	}
	return writer, nil
}

// Append validates a sanitized Fact, assigns its contiguous sequence, and
// writes it. Every Cause must already have been appended to this Writer.
func (writer *Writer) Append(fact Fact) (int, error) {
	if err := writer.ready(); err != nil {
		return 0, err
	}
	if writer.count >= maxTraceFacts {
		return 0, fmt.Errorf("trace writer: fact count exceeds %d", maxTraceFacts)
	}
	sequence := writer.count + 1
	capturedAt, err := writer.validateFact(fact, sequence)
	if err != nil {
		return 0, err
	}
	line := factLine{
		Schema: traceSchema, Version: traceVersion, Record: "fact", Sequence: sequence,
		ID: fact.ID, CapturedAt: capturedAt, Source: fact.Source, Agent: fact.Agent,
		Turn: fact.Turn, Kind: fact.Kind, Truth: fact.Truth,
		Causes: append([]string{}, fact.Causes...), Refs: fact.References, Facts: fact.Fields,
	}
	if err := writer.writePrefix(line); err != nil {
		return 0, err
	}
	writer.count = sequence
	writer.seen[fact.ID] = struct{}{}
	if fact.Kind == "test.gate.checked" {
		writer.gateFacts[fact.ID] = gateAssertion{ID: fact.Fields.GateID,
			Status: GateStatus(fact.Fields.Status)}
	}
	return sequence, nil
}

// Finish validates the terminal gates, writes the footer, and closes the
// Writer. The digest covers the exact header and Fact bytes, including their
// line terminators, but not the footer itself.
func (writer *Writer) Finish(result Result) error {
	if err := writer.ready(); err != nil {
		return err
	}
	finishedAt, err := writer.validateResult(result)
	if err != nil {
		return err
	}
	sum := writer.digest.Sum(nil)
	line := resultLine{
		Schema: traceSchema, Version: traceVersion, Record: "result", Status: result.Status,
		FinishedAt: finishedAt, RecordCount: writer.count,
		TraceDigest: "sha256:" + hex.EncodeToString(sum), Gates: cloneGates(result.Gates),
	}
	if err := writer.writeFooter(line); err != nil {
		return err
	}
	writer.closed = true
	return nil
}

func (writer *Writer) ready() error {
	if writer == nil {
		return fmt.Errorf("trace writer: nil writer")
	}
	if writer.failure != nil {
		return writer.failure
	}
	if writer.closed {
		return fmt.Errorf("trace writer: already finished")
	}
	return nil
}

func (writer *Writer) writePrefix(value any) error {
	line, err := encodeLine(value)
	if err != nil {
		return err
	}
	if err := writeFull(writer.destination, line); err != nil {
		writer.failure = fmt.Errorf("trace writer: write prefix: %w", err)
		return writer.failure
	}
	if _, err := writer.digest.Write(line); err != nil {
		writer.failure = fmt.Errorf("trace writer: hash prefix: %w", err)
		return writer.failure
	}
	return nil
}

func (writer *Writer) writeFooter(value any) error {
	line, err := encodeLine(value)
	if err != nil {
		return err
	}
	if err := writeFull(writer.destination, line); err != nil {
		writer.failure = fmt.Errorf("trace writer: write footer: %w", err)
		return writer.failure
	}
	return nil
}

func encodeLine(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("trace writer: encode record: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > maxTraceLine {
		return nil, fmt.Errorf("trace writer: encoded record exceeds %d bytes", maxTraceLine)
	}
	return append(encoded, '\n'), nil
}

func writeFull(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if written < 0 || written > len(data) {
			return fmt.Errorf("invalid write count %d", written)
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func cloneGates(gates []Gate) []Gate {
	result := make([]Gate, len(gates))
	for index, gate := range gates {
		result[index] = gate
		result[index].Evidence = append([]string{}, gate.Evidence...)
	}
	return result
}
