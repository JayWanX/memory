package agencyclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	journalSchema          = "memory.agency.client-journal"
	journalVersion         = 2
	journalCredentialBytes = 32
	maxJournalBytes        = 32 << 10
	// A non-multiple-of-three length makes the final unpadded base64url
	// character draw from an alphanumeric-only subset. OperationKey tokens
	// deliberately reject a trailing '-' or '_'.
	currentOperationEntropy   = 32
	admissionOperationPrefix  = "admit:"
	currentOperationPrefix    = "current:"
	operationDerivationDomain = "memory/r7/agency-cli/admission-operation/v1"
	currentProjectionEmpty    = "empty"
	currentProjectionSubject  = "subject"
)

var (
	errCandidateNotCaptured = errors.New("Intent names an uncaptured Artifact candidate")
	errCandidateRepeated    = errors.New("Intent repeats an Artifact candidate")
)

type capturedBinding struct {
	Handle agency.OpaqueHandle
	Digest agency.Digest
}

type clientJournal struct {
	Attachment        attachment
	BoundaryDigest    agency.Digest
	CurrentOperation  agency.OperationKey
	CurrentProjection string
	Candidates        []capturedBinding
	fileName          string
	fileDigest        [sha256.Size]byte
}

type journalWire struct {
	Schema            string          `json:"schema"`
	Version           int             `json:"version"`
	Attachment        string          `json:"attachment"`
	BoundaryDigest    string          `json:"boundary_digest"`
	Credential        string          `json:"credential"`
	ExpiresAt         string          `json:"expires_at"`
	CurrentOperation  string          `json:"current_operation,omitempty"`
	CurrentProjection string          `json:"current_projection,omitempty"`
	Candidates        []candidateWire `json:"candidates,omitempty"`
}

type candidateWire struct {
	Handle string `json:"handle"`
	Digest string `json:"digest"`
}

func newClientJournal(value attachment, boundary agency.Digest) (clientJournal, error) {
	journal := clientJournal{Attachment: value, BoundaryDigest: boundary}
	if err := journal.validate(); err != nil {
		return clientJournal{}, err
	}
	return journal, nil
}

func (journal clientJournal) validate() error {
	if _, err := agency.NewAttachmentID(journal.Attachment.ID); err != nil ||
		len(journal.Attachment.Credential) != journalCredentialBytes ||
		journal.Attachment.ExpiresAt.IsZero() || journal.BoundaryDigest.IsZero() {
		return errors.New("R7 client journal attachment is invalid")
	}
	if !journal.CurrentOperation.IsZero() &&
		!strings.HasPrefix(journal.CurrentOperation.String(), currentOperationPrefix) {
		return errors.New("R7 client journal Current operation is invalid")
	}
	if journal.CurrentProjection != "" && journal.CurrentOperation.IsZero() {
		return errors.New("R7 client journal Current projection has no operation")
	}
	if journal.CurrentProjection != "" && journal.CurrentProjection != currentProjectionEmpty &&
		journal.CurrentProjection != currentProjectionSubject {
		return errors.New("R7 client journal Current projection is invalid")
	}
	if len(journal.Candidates) > agency.MaxArtifactInputs {
		return errors.New("R7 client journal candidate bound is exceeded")
	}
	previous := ""
	for _, candidate := range journal.Candidates {
		if candidate.Handle.IsZero() || candidate.Digest.IsZero() || candidate.Handle.String() <= previous {
			return errors.New("R7 client journal candidate set is invalid")
		}
		previous = candidate.Handle.String()
	}
	return nil
}

func (journal clientJournal) canonical() ([]byte, error) {
	if err := journal.validate(); err != nil {
		return nil, err
	}
	candidates := make([]candidateWire, len(journal.Candidates))
	for index, candidate := range journal.Candidates {
		candidates[index] = candidateWire{Handle: candidate.Handle.String(), Digest: candidate.Digest.String()}
	}
	wire := journalWire{Schema: journalSchema, Version: journalVersion,
		Attachment:     journal.Attachment.ID,
		BoundaryDigest: journal.BoundaryDigest.String(),
		Credential:     base64.RawURLEncoding.EncodeToString(journal.Attachment.Credential),
		ExpiresAt:      journal.Attachment.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Candidates:     candidates}
	if !journal.CurrentOperation.IsZero() {
		wire.CurrentOperation = journal.CurrentOperation.String()
	}
	wire.CurrentProjection = journal.CurrentProjection
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > maxJournalBytes {
		return nil, errors.New("R7 client journal cannot be encoded")
	}
	return append(raw, '\n'), nil
}

func parseClientJournal(raw []byte) (clientJournal, error) {
	if len(raw) < 3 || len(raw) > maxJournalBytes || raw[len(raw)-1] != '\n' {
		return clientJournal{}, errors.New("R7 client journal has invalid framing")
	}
	wire, err := decodeJournalWire(raw[:len(raw)-1])
	if err != nil {
		return clientJournal{}, err
	}
	journal, err := journalFromWire(wire)
	if err != nil {
		return clientJournal{}, err
	}
	canonical, err := journal.canonical()
	if err != nil || !bytes.Equal(canonical, raw) {
		journal.clear()
		return clientJournal{}, errors.New("R7 client journal is not canonical")
	}
	journal.fileDigest = sha256.Sum256(raw)
	return journal, nil
}

func decodeJournalWire(raw []byte) (journalWire, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire journalWire
	if err := decoder.Decode(&wire); err != nil {
		return journalWire{}, errors.New("R7 client journal has invalid fields")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return journalWire{}, errors.New("R7 client journal contains a trailing value")
	}
	if wire.Schema != journalSchema || wire.Version != journalVersion {
		return journalWire{}, errors.New("R7 client journal schema is unsupported")
	}
	return wire, nil
}

func journalFromWire(wire journalWire) (clientJournal, error) {
	attachmentID, attachmentErr := agency.NewAttachmentID(wire.Attachment)
	boundaryDigest, boundaryErr := agency.ParseDigest(wire.BoundaryDigest)
	credential, credentialErr := base64.RawURLEncoding.Strict().DecodeString(wire.Credential)
	expiresAt, expiryErr := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if attachmentErr != nil || boundaryErr != nil || boundaryDigest.IsZero() ||
		credentialErr != nil || expiryErr != nil ||
		len(credential) != journalCredentialBytes || wire.ExpiresAt != expiresAt.UTC().Format(time.RFC3339Nano) {
		clear(credential)
		return clientJournal{}, errors.New("R7 client journal attachment is corrupt")
	}
	journal := clientJournal{Attachment: attachment{ID: attachmentID.String(),
		Credential: credential, ExpiresAt: expiresAt}, BoundaryDigest: boundaryDigest}
	if wire.CurrentOperation != "" {
		operation, err := agency.NewOperationKey(wire.CurrentOperation)
		if err != nil {
			journal.clear()
			return clientJournal{}, errors.New("R7 client journal Current operation is corrupt")
		}
		journal.CurrentOperation = operation
	}
	journal.CurrentProjection = wire.CurrentProjection
	journal.Candidates = make([]capturedBinding, len(wire.Candidates))
	for index, candidate := range wire.Candidates {
		handle, handleErr := agency.NewOpaqueHandle(candidate.Handle)
		digest, digestErr := agency.ParseDigest(candidate.Digest)
		if handleErr != nil || digestErr != nil {
			journal.clear()
			return clientJournal{}, errors.New("R7 client journal candidate is corrupt")
		}
		journal.Candidates[index] = capturedBinding{Handle: handle, Digest: digest}
	}
	if err := journal.validate(); err != nil {
		journal.clear()
		return clientJournal{}, err
	}
	return journal, nil
}

func (journal *clientJournal) clear() {
	if journal == nil {
		return
	}
	clear(journal.Attachment.Credential)
	journal.Attachment.Credential = nil
}

func (journal *clientJournal) addCandidate(capture artifactCapture) error {
	handle, handleErr := agency.NewOpaqueHandle(capture.Handle)
	digest, digestErr := agency.ParseDigest(capture.Digest)
	if journal == nil || handleErr != nil || digestErr != nil {
		return errors.New("R7 captured candidate is invalid")
	}
	for _, candidate := range journal.Candidates {
		if candidate.Handle == handle {
			if candidate.Digest != digest {
				return errors.New("R7 captured candidate handle changed digest")
			}
			return nil
		}
	}
	if len(journal.Candidates) >= agency.MaxArtifactInputs {
		return errors.New("R7 captured candidate bound is exceeded")
	}
	journal.Candidates = append(journal.Candidates,
		capturedBinding{Handle: handle, Digest: digest})
	sort.Slice(journal.Candidates, func(i, j int) bool {
		return journal.Candidates[i].Handle.String() < journal.Candidates[j].Handle.String()
	})
	return journal.validate()
}

func (journal clientJournal) bindCandidates(intent agency.AgentIntent) (
	[]candidateBinding, error,
) {
	byHandle := make(map[string]agency.Digest, len(journal.Candidates))
	for _, candidate := range journal.Candidates {
		byHandle[candidate.Handle.String()] = candidate.Digest
	}
	result := make([]candidateBinding, 0, len(intent.Artifacts()))
	seen := make(map[string]struct{})
	for _, input := range intent.Artifacts() {
		if input.Kind() != agency.ArtifactInputCandidate {
			continue
		}
		handle := input.Handle().String()
		digest, exists := byHandle[handle]
		if !exists {
			return nil, errCandidateNotCaptured
		}
		if _, duplicate := seen[handle]; duplicate {
			return nil, errCandidateRepeated
		}
		seen[handle] = struct{}{}
		result = append(result, candidateBinding{Handle: input.Handle().String(),
			Digest: digest.String()})
	}
	return result, nil
}

func newCurrentOperation(random io.Reader) (agency.OperationKey, error) {
	if random == nil {
		return agency.OperationKey{}, errors.New("R7 Current operation entropy is unavailable")
	}
	entropy := make([]byte, currentOperationEntropy)
	if _, err := io.ReadFull(random, entropy); err != nil {
		clear(entropy)
		return agency.OperationKey{}, fmt.Errorf("generate R7 Current operation: %w", err)
	}
	value := currentOperationPrefix + base64.RawURLEncoding.EncodeToString(entropy)
	clear(entropy)
	return agency.NewOperationKey(value)
}

func deriveAdmissionOperation(current agency.OperationKey, intent agency.AgentIntent,
	candidates []candidateBinding,
) (agency.OperationKey, error) {
	if current.IsZero() || len(intent.CanonicalJSON()) == 0 || len(candidates) > agency.MaxArtifactInputs {
		return agency.OperationKey{}, errors.New("derive R7 admission operation: inputs are invalid")
	}
	hash := sha256.New()
	writeOperationPart(hash, []byte(operationDerivationDomain))
	writeOperationPart(hash, []byte(current.String()))
	writeOperationPart(hash, intent.CanonicalJSON())
	for _, candidate := range candidates {
		handle, handleErr := agency.NewOpaqueHandle(candidate.Handle)
		digest, digestErr := agency.ParseDigest(candidate.Digest)
		if handleErr != nil || digestErr != nil {
			return agency.OperationKey{}, errors.New("derive R7 admission operation: candidate is invalid")
		}
		writeOperationPart(hash, []byte(handle.String()))
		writeOperationPart(hash, digest[:])
	}
	digest := hash.Sum(nil)
	value := admissionOperationPrefix + base64.RawURLEncoding.EncodeToString(digest)
	clear(digest)
	return agency.NewOperationKey(value)
}

func writeOperationPart(writer io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write(value)
}
