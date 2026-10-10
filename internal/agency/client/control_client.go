package agencyclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

const (
	routeAttachments   = "/v1/agency/attachments"
	routeAttachmentEnd = "/v1/agency/attachments/end"
	routeCurrent       = "/v1/agency/current"
	routeSubmit        = "/v1/agency/submit"
	routeArtifacts     = "/v1/agency/artifacts"
	routeArtifactRead  = "/v1/agency/artifacts/read"

	headerAttachment       = "Memory-Agency-Attachment"
	headerCredential       = "Memory-Agency-Credential"
	headerCurrentOperation = "Memory-Agency-Current-Operation"
	headerOperation        = "Memory-Agency-Operation"
	headerArtifactDigest   = "Memory-Artifact-Digest"

	attachmentSchema    = "memory.agency.attachment"
	attachmentEndSchema = "memory.agency.attachment-end"
	artifactSchema      = "memory.agency.artifact"
	controlVersion      = 1
	timeWireLayout      = "2006-01-02T15:04:05.000000000Z"
	ownerSocketMode     = os.FileMode(0o600)
)

var errUnsafeClientState = errors.New("R7 Agency client state is unsafe")

type agencyClient interface {
	Attach(context.Context, agency.Digest) (attachment, *controlError)
	End(context.Context, attachment) *controlError
	Current(context.Context, attachment, string) ([]byte, *controlError)
	Submit(context.Context, attachment, string, string,
		[]byte, []candidateBinding) ([]byte, *controlError)
	Capture(context.Context, []byte) (artifactCapture, *controlError)
	ReadArtifact(context.Context, attachment, string, string) ([]byte, *controlError)
}

type controlClient struct {
	socket   string
	ownerUID uint32
	http     *http.Client
}

func newControlClient(nodeState string) (*controlClient, error) {
	if nodeState == "" || !filepath.IsAbs(nodeState) || filepath.Clean(nodeState) != nodeState {
		return nil, fmt.Errorf("%w: Node state path is not absolute and canonical", errUnsafeClientState)
	}
	info, err := os.Lstat(nodeState)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect Node state directory", errUnsafeClientState)
	}
	ownerUID, err := validateOwnerInfo(info, ownerDirectoryMode, true)
	if err != nil {
		return nil, fmt.Errorf("%w: Node state directory is not owner-only", errUnsafeClientState)
	}
	client := &controlClient{socket: filepath.Join(nodeState, "control.sock"), ownerUID: ownerUID}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		DialContext: client.dialContext}
	client.http = &http.Client{Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return client, nil
}

func (client *controlClient) Attach(ctx context.Context,
	boundary agency.Digest,
) (attachment, *controlError) {
	if boundary.IsZero() {
		return attachment{}, newControlError(codeInvalidArgument,
			"Host boundary operation is invalid")
	}
	var response attachmentWire
	request := attachmentBeginWire{BoundaryDigest: boundary.String()}
	if apiErr := client.post(ctx, routeAttachments, request, nil, &response,
		maxPrivateResponse); apiErr != nil {
		return attachment{}, apiErr
	}
	if response.Schema != attachmentSchema || response.Version != controlVersion {
		return attachment{}, invalidControlResponse("Agency attachment response schema is unsupported")
	}
	credential, err := decodeSecret(response.Credential)
	if err != nil {
		clear(credential)
		return attachment{}, invalidControlResponse("Agency attachment response is invalid")
	}
	expiresAt, err := time.Parse(timeWireLayout, response.ExpiresAt)
	if err != nil || expiresAt.IsZero() || response.ExpiresAt != expiresAt.UTC().Format(timeWireLayout) {
		clear(credential)
		return attachment{}, invalidControlResponse("Agency attachment expiry is invalid")
	}
	result := attachment{ID: response.Attachment, Credential: credential, ExpiresAt: expiresAt}
	if validateAttachment(result) != nil {
		clear(credential)
		return attachment{}, invalidControlResponse("Agency attachment response is invalid")
	}
	return result, nil
}

func (client *controlClient) End(ctx context.Context, value attachment) *controlError {
	headers, apiErr := attachmentHeaders(value)
	if apiErr != nil {
		return apiErr
	}
	var response attachmentEndWire
	if apiErr := client.post(ctx, routeAttachmentEnd, struct{}{}, headers, &response,
		maxPrivateResponse); apiErr != nil {
		return apiErr
	}
	if response.Schema != attachmentEndSchema || response.Version != controlVersion ||
		response.Status != "ended" {
		return invalidControlResponse("Agency attachment end response is invalid")
	}
	return nil
}

func (client *controlClient) Current(ctx context.Context, authority attachment,
	operation string,
) ([]byte, *controlError) {
	headers, apiErr := authorityHeaders(authority, operation, "")
	if apiErr != nil {
		return nil, apiErr
	}
	response, apiErr := client.postProjection(ctx, routeCurrent, struct{}{}, headers,
		agency.MaxAgentViewCanonicalBytes)
	if apiErr != nil {
		return nil, apiErr
	}
	if err := agency.ValidateAgentViewProjectionCanonicalJSON(response); err != nil {
		return nil, invalidControlResponse("Agency View projection is invalid")
	}
	return append([]byte(nil), response...), nil
}

func (client *controlClient) Submit(ctx context.Context, authority attachment,
	currentOperation, operation string, intent []byte, candidates []candidateBinding,
) ([]byte, *controlError) {
	if _, err := agency.NewOperationKey(operation); err != nil {
		return nil, newControlError(codeInvalidArgument, "Intent or candidate binding is invalid")
	}
	if _, err := agency.ParseAgentIntentJSON(intent); err != nil || len(candidates) > agency.MaxArtifactInputs {
		return nil, newControlError(codeInvalidArgument, "Intent or candidate binding is invalid")
	}
	headers, apiErr := authorityHeaders(authority, currentOperation, operation)
	if apiErr != nil {
		return nil, apiErr
	}
	wires := make([]controlCandidateWire, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for index, candidate := range candidates {
		handle, handleErr := agency.NewOpaqueHandle(candidate.Handle)
		digest, digestErr := agency.ParseDigest(candidate.Digest)
		if handleErr != nil || digestErr != nil || handle.IsZero() || digest.IsZero() {
			return nil, newControlError(codeArtifactInvalid, "candidate binding is invalid")
		}
		if _, duplicate := seen[candidate.Handle]; duplicate {
			return nil, newControlError(codeInvalidArgument, "Intent or candidate binding is invalid")
		}
		seen[candidate.Handle] = struct{}{}
		wires[index] = controlCandidateWire{Digest: candidate.Digest, Handle: candidate.Handle}
	}
	request := submitWire{Candidates: wires, Intent: json.RawMessage(append([]byte(nil), intent...))}
	response, apiErr := client.postProjection(ctx, routeSubmit, request, headers,
		agency.MaxAgentReceiptCanonicalBytes)
	if apiErr != nil {
		return nil, apiErr
	}
	if _, err := agency.ParseAgentReceiptProjectionCanonicalJSON(response); err != nil {
		return nil, invalidControlResponse("Agency Receipt projection is invalid")
	}
	return append([]byte(nil), response...), nil
}

func (client *controlClient) Capture(ctx context.Context, content []byte) (
	artifactCapture, *controlError,
) {
	if len(content) > maxArtifactInputBytes {
		return artifactCapture{}, newControlError(codeArtifactTooLarge,
			"Artifact exceeds the closed byte bound")
	}
	request := artifactRequestWire{Content: base64.RawStdEncoding.EncodeToString(content)}
	var response artifactResponseWire
	if apiErr := client.post(ctx, routeArtifacts, request, nil, &response,
		maxPrivateResponse); apiErr != nil {
		return artifactCapture{}, apiErr
	}
	if response.Schema != artifactSchema || response.Version != controlVersion ||
		response.ByteSize != int64(len(content)) {
		return artifactCapture{}, invalidControlResponse("Agency Artifact response is invalid")
	}
	capture := artifactCapture{Handle: response.Handle, Digest: response.Digest,
		ByteSize: response.ByteSize}
	if validateCapture(capture) != nil || response.Digest != agency.Sum(content).String() {
		return artifactCapture{}, invalidControlResponse("Agency Artifact response differs from content")
	}
	return capture, nil
}

func (client *controlClient) ReadArtifact(ctx context.Context, authority attachment,
	current, handleValue string,
) ([]byte, *controlError) {
	handle, err := agency.NewOpaqueHandle(handleValue)
	if err != nil || handle.IsZero() {
		return nil, newControlError(codeInvalidArgument, "Artifact handle is invalid")
	}
	headers, apiErr := authorityHeaders(authority, current, "")
	if apiErr != nil {
		return nil, apiErr
	}
	request := artifactReadRequestWire{Handle: handle.String()}
	return client.postArtifact(ctx, routeArtifactRead, request, headers)
}

func authorityHeaders(value attachment, current, operation string) (http.Header, *controlError) {
	headers, apiErr := attachmentHeaders(value)
	if apiErr != nil {
		return nil, apiErr
	}
	if _, err := agency.NewOperationKey(current); err != nil {
		return nil, newControlError(codeInvalidArgument, "Agency current operation is invalid")
	}
	if operation != "" {
		if _, err := agency.NewOperationKey(operation); err != nil {
			return nil, newControlError(codeInvalidArgument, "Agency operation is invalid")
		}
	}
	headers.Set(headerCurrentOperation, current)
	if operation != "" {
		headers.Set(headerOperation, operation)
	}
	return headers, nil
}

func attachmentHeaders(value attachment) (http.Header, *controlError) {
	if validateAttachment(value) != nil {
		return nil, newControlError(codeAuthenticationFailed,
			"Agency attachment authority is unavailable")
	}
	headers := make(http.Header)
	headers.Set(headerAttachment, value.ID)
	headers.Set(headerCredential, base64.RawURLEncoding.EncodeToString(value.Credential))
	return headers, nil
}

func decodeSecret(value string) ([]byte, error) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "= \t\r\n") {
		return nil, errors.New("secret is not unpadded base64url")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != attachmentSecretBytes {
		clear(decoded)
		return nil, errors.New("secret must encode 32 bytes")
	}
	return decoded, nil
}

// Wire fields are declared in canonical (lexicographic) order.
type attachmentWire struct {
	Attachment string `json:"attachment"`
	Credential string `json:"credential"`
	ExpiresAt  string `json:"expires_at"`
	Schema     string `json:"schema"`
	Version    int    `json:"version"`
}

type attachmentBeginWire struct {
	BoundaryDigest string `json:"boundary_digest"`
}

type attachmentEndWire struct {
	ReleasedClaim bool   `json:"released_claim"`
	Replayed      bool   `json:"replayed"`
	Schema        string `json:"schema"`
	Status        string `json:"status"`
	Version       int    `json:"version"`
}

type controlCandidateWire struct {
	Digest string `json:"digest"`
	Handle string `json:"handle"`
}

type submitWire struct {
	Candidates []controlCandidateWire `json:"candidates,omitempty"`
	Intent     json.RawMessage        `json:"intent"`
}

type artifactRequestWire struct {
	Content string `json:"content_base64"`
}

type artifactReadRequestWire struct {
	Handle string `json:"handle"`
}

type artifactResponseWire struct {
	ByteSize int64  `json:"byte_size"`
	Digest   string `json:"digest"`
	Handle   string `json:"handle"`
	Schema   string `json:"schema"`
	Version  int    `json:"version"`
}
