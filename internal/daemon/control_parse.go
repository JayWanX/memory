package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/JayWanX/memory/internal/agency"
	"github.com/JayWanX/memory/internal/agency/artifact"
	"github.com/JayWanX/memory/internal/agency/authority"
)

func prepareControlRequest(request *http.Request, readOnly, requireAttachment,
	requireCurrent, requireOperation bool,
) *controlError {
	if request == nil {
		return newControlError(codeInvalidArgument, "request is required")
	}
	wantMethod := http.MethodPost
	if readOnly {
		wantMethod = http.MethodGet
	}
	if request.Method != wantMethod {
		return newControlError(codeInvalidArgument, "method is not allowed")
	}
	if request.URL == nil || request.URL.RawQuery != "" {
		return newControlError(codeInvalidArgument, "local agency request must not contain a query")
	}
	for _, name := range legacyHeaders {
		if len(request.Header.Values(name)) != 0 {
			return newControlError(codeInvalidArgument, "R5 control metadata is not allowed")
		}
	}
	attachmentPresent := anyHeader(request.Header, headerAttachment, headerCredential)
	currentPresent := len(request.Header.Values(headerCurrentOperation)) != 0
	operationPresent := len(request.Header.Values(headerOperation)) != 0
	if attachmentPresent != requireAttachment || currentPresent != requireCurrent ||
		operationPresent != requireOperation {
		return newControlError(codeInvalidArgument, "Agency authority is not allowed on this route")
	}
	if readOnly {
		if request.ContentLength != 0 || len(request.TransferEncoding) != 0 ||
			len(request.Header.Values("Content-Type")) != 0 {
			return newControlError(codeInvalidArgument, "status request must not contain content")
		}
		return nil
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 || contentTypes[0] != "application/json" {
		return newControlError(codeInvalidArgument, "Content-Type must be application/json")
	}
	return nil
}

func anyHeader(header http.Header, names ...string) bool {
	for _, name := range names {
		if len(header.Values(name)) != 0 {
			return true
		}
	}
	return false
}

func parseControlAuthority(header http.Header) (authority.AttachmentProof,
	authority.CurrentOperation, *controlError,
) {
	proof, errValue := parseAttachmentProof(header)
	if errValue != nil {
		return authority.AttachmentProof{}, authority.CurrentOperation{}, errValue
	}
	currentValue, errValue := singleHeader(header, headerCurrentOperation,
		codeInvalidArgument, "Agency current operation is required exactly once")
	if errValue != nil {
		return authority.AttachmentProof{}, authority.CurrentOperation{}, errValue
	}
	key, err := agency.NewOperationKey(currentValue)
	if err != nil {
		return authority.AttachmentProof{}, authority.CurrentOperation{},
			newControlError(codeInvalidArgument, "Agency current operation is invalid")
	}
	current, err := authority.NewCurrentOperation(key)
	if err != nil {
		return authority.AttachmentProof{}, authority.CurrentOperation{},
			newControlError(codeInvalidArgument, "Agency current operation is invalid")
	}
	return proof, current, nil
}

func parseAttachmentProof(header http.Header) (authority.AttachmentProof, *controlError) {
	attachmentValue, errValue := singleHeader(header, headerAttachment,
		codeAuthenticationFailed, "Agency attachment proof is required")
	if errValue != nil {
		return authority.AttachmentProof{}, errValue
	}
	credentialValue, errValue := singleHeader(header, headerCredential,
		codeAuthenticationFailed, "Agency attachment proof is required")
	if errValue != nil {
		return authority.AttachmentProof{}, errValue
	}
	credential, err := decodeSecret(credentialValue)
	if err != nil {
		return authority.AttachmentProof{},
			newControlError(codeAuthenticationFailed, "Agency attachment proof is invalid")
	}
	defer clear(credential)
	id, err := agency.NewAttachmentID(attachmentValue)
	if err != nil {
		return authority.AttachmentProof{},
			newControlError(codeAuthenticationFailed, "Agency attachment proof is invalid")
	}
	proof, err := authority.NewAttachmentProof(id, credential)
	if err != nil {
		return authority.AttachmentProof{},
			newControlError(codeAuthenticationFailed, "Agency attachment proof is invalid")
	}
	return proof, nil
}

func singleHeader(header http.Header, name string, code controlErrorCode,
	message string,
) (string, *controlError) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", newControlError(code, message)
	}
	return values[0], nil
}

func decodeSecret(value string) ([]byte, error) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "= \t\r\n") {
		return nil, errors.New("secret is not unpadded base64url")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != attachmentCredentialBytes {
		clear(decoded)
		return nil, errors.New("secret must encode 32 bytes")
	}
	return decoded, nil
}

func parseCandidateBindings(values []candidateWire) ([]candidateBinding, error) {
	result := make([]candidateBinding, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		handle, handleErr := agency.NewOpaqueHandle(value.Handle)
		digest, digestErr := agency.ParseDigest(value.Digest)
		if handleErr != nil || digestErr != nil || handle.IsZero() || digest.IsZero() {
			return nil, errors.New("candidate binding is invalid")
		}
		if _, duplicate := seen[handle.String()]; duplicate {
			return nil, errors.New("candidate binding is duplicated")
		}
		seen[handle.String()] = struct{}{}
		result[index] = candidateBinding{handle: handle, digest: digest}
	}
	return result, nil
}

func decodeArtifactContent(value string) ([]byte, *controlError) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "=\t\r\n ") {
		return nil, newControlError(codeArtifactInvalid,
			"Artifact content must be canonical raw base64")
	}
	if len(value) > base64.RawStdEncoding.EncodedLen(artifact.MaxObjectBytes) {
		return nil, newControlError(codeArtifactTooLarge, "Artifact exceeds the closed byte bound")
	}
	content, err := base64.RawStdEncoding.Strict().DecodeString(value)
	if err != nil {
		clear(content)
		return nil, newControlError(codeArtifactInvalid, "Artifact content is invalid")
	}
	if len(content) > artifact.MaxObjectBytes {
		clear(content)
		return nil, newControlError(codeArtifactTooLarge, "Artifact exceeds the closed byte bound")
	}
	return content, nil
}

func decodeClosedRequest(request *http.Request, target any, maximum int64) *controlError {
	if request == nil || request.Body == nil || target == nil || maximum <= 0 {
		return newControlError(codeInvalidArgument, "JSON object body is required")
	}
	if request.ContentLength > maximum {
		return newControlError(codeInvalidArgument, "request body exceeds the local control limit")
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, maximum+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > maximum || raw[0] != '{' {
		return newControlError(codeInvalidArgument, "request body exceeds the local control limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return newControlError(codeInvalidArgument, "request body does not match the closed route schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return newControlError(codeInvalidArgument, "request body contains a trailing value")
	}
	rebuilt, err := json.Marshal(target)
	if err != nil || !bytes.Equal(rebuilt, raw) {
		return newControlError(codeInvalidArgument, "request body does not match the closed route schema")
	}
	return nil
}
