package agencyclient

import (
	"fmt"
	"strings"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	controlSchemaVersion   = 1
	maxControlDiagnostic   = 512
	maxArtifactInputBytes  = 4 << 20
	attachmentSecretBytes  = 32
	maxPrivateResponse     = 4 << 10
	maxArtifactRequestBody = ((maxArtifactInputBytes + 2) / 3 * 4) + 256
)

// These values are private transport material. They deliberately do not
// belong to the daemon domain surface: the Agent sees only View, Intent,
// Receipt, and candidate Handle.
type attachment struct {
	ID         string
	Credential []byte
	ExpiresAt  time.Time
}

type candidateBinding struct {
	Handle string
	Digest string
}

type artifactCapture struct {
	Handle   string
	Digest   string
	ByteSize int64
}

func validateAttachment(value attachment) error {
	if _, err := agency.NewAttachmentID(value.ID); err != nil ||
		len(value.Credential) != attachmentSecretBytes || value.ExpiresAt.IsZero() {
		return fmt.Errorf("Agency attachment is invalid")
	}
	return nil
}

func validateCapture(value artifactCapture) error {
	handle, handleErr := agency.NewOpaqueHandle(value.Handle)
	digest, digestErr := agency.ParseDigest(value.Digest)
	if handleErr != nil || digestErr != nil || handle.IsZero() || digest.IsZero() ||
		value.ByteSize < 0 || value.ByteSize > maxArtifactInputBytes {
		return fmt.Errorf("Agency Artifact capture is invalid")
	}
	return nil
}

type controlErrorCode string

const (
	codeInvalidArgument       controlErrorCode = "invalid_argument"
	codeContentRequired       controlErrorCode = "content_required"
	codeContentTooLarge       controlErrorCode = "content_too_large"
	codeArtifactInvalid       controlErrorCode = "artifact_invalid"
	codeArtifactTooLarge      controlErrorCode = "artifact_too_large"
	codeAuthenticationFailed  controlErrorCode = "authentication_failed"
	codeContextRequired       controlErrorCode = "context_required"
	codeContextStale          controlErrorCode = "context_stale"
	codeAssetRevisionMismatch controlErrorCode = "asset_revision_mismatch"
	codeActionNotAllowed      controlErrorCode = "action_not_allowed"
	codeOperationMismatch     controlErrorCode = "operation_mismatch"
	codeOperationPending      controlErrorCode = "operation_pending"
	codeMemorydUnavailable    controlErrorCode = "memoryd_unavailable"
	codeInternal              controlErrorCode = "internal"
)

func (code controlErrorCode) valid() bool {
	switch code {
	case codeInvalidArgument, codeContentRequired, codeContentTooLarge,
		codeArtifactInvalid, codeArtifactTooLarge, codeAuthenticationFailed,
		codeContextRequired, codeContextStale, codeAssetRevisionMismatch,
		codeActionNotAllowed, codeOperationMismatch, codeOperationPending,
		codeMemorydUnavailable, codeInternal:
		return true
	default:
		return false
	}
}

func (code controlErrorCode) retryable() bool {
	return code == codeOperationPending || code == codeMemorydUnavailable
}

func (code controlErrorCode) exitStatus() int {
	switch code {
	case codeInvalidArgument, codeContentRequired, codeContentTooLarge,
		codeArtifactInvalid, codeArtifactTooLarge:
		return 2
	case codeAuthenticationFailed, codeContextRequired, codeContextStale,
		codeAssetRevisionMismatch:
		return 3
	case codeActionNotAllowed, codeOperationMismatch:
		return 4
	case codeOperationPending, codeMemorydUnavailable:
		return 5
	default:
		return 1
	}
}

// Field order is the frozen canonical control-error order.
type controlError struct {
	Code          controlErrorCode `json:"code"`
	Message       string           `json:"message"`
	OperationID   *string          `json:"operation_id"`
	Replayed      bool             `json:"replayed"`
	Retryable     bool             `json:"retryable"`
	SchemaVersion int              `json:"schema_version"`
	Status        string           `json:"status"`
}

func newControlError(code controlErrorCode, message string) *controlError {
	message = strings.TrimSpace(message)
	if !code.valid() || message == "" || len([]byte(message)) > maxControlDiagnostic {
		code = codeInternal
		message = "internal control error"
	}
	return &controlError{Code: code, Message: message, Retryable: code.retryable(),
		SchemaVersion: controlSchemaVersion, Status: "error"}
}

func (value *controlError) Error() string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", value.Code, value.Message)
}

func (value *controlError) exitStatus() int {
	if value == nil {
		return 0
	}
	return value.Code.exitStatus()
}

func validateControlError(value *controlError) error {
	if value == nil || value.SchemaVersion != controlSchemaVersion || value.Status != "error" ||
		!value.Code.valid() || value.Retryable != value.Code.retryable() ||
		strings.TrimSpace(value.Message) != value.Message || value.Message == "" ||
		len([]byte(value.Message)) > maxControlDiagnostic || value.Replayed || value.OperationID != nil {
		return fmt.Errorf("invalid Agency control error")
	}
	return nil
}
