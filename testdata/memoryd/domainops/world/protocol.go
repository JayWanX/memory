// Package world implements the test-only service world for the R7 federated
// domain-operations case. It contains no Agent or memoryd policy.
package world

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

const (
	MaxRequestBodyBytes  = 32 << 10
	MaxResponseBodyBytes = 128 << 10
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)

type ChargeRequest struct {
	BusinessID string `json:"business_id"`
	AttemptKey string `json:"attempt_key"`
}

type ChargeResponse struct {
	Accepted bool  `json:"accepted"`
	Replayed bool  `json:"replayed"`
	Sequence int64 `json:"sequence"`
}

type PayRequest struct {
	BusinessID string `json:"business_id"`
}

type PayResponse struct {
	Paid      bool  `json:"paid"`
	Attempts  int   `json:"attempts"`
	CaptureID int64 `json:"capture_id"`
}

type CheckoutResponse struct {
	Accepted  bool   `json:"accepted"`
	Route     string `json:"route"`
	CaptureID int64  `json:"capture_id"`
}

type VoidRequest struct {
	Sequence int64  `json:"sequence"`
	Reason   string `json:"reason"`
}

func ValidToken(value string) bool { return tokenPattern.MatchString(value) }

func DecodeJSON(request *http.Request, destination any) error {
	if request == nil || request.Body == nil || destination == nil {
		return errors.New("request body is required")
	}
	return decodeBoundedJSON(request.Body, MaxRequestBodyBytes, destination, "request")
}

func decodeBoundedJSON(reader io.Reader, limit int64, destination any, subject string) error {
	payload, err := readBounded(reader, limit)
	if err != nil {
		return fmt.Errorf("read %s: %w", subject, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode %s: %w", subject, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%s contains a trailing JSON value", subject)
		}
		return fmt.Errorf("decode %s trailer: %w", subject, err)
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil || limit <= 0 {
		return nil, errors.New("bounded reader dependencies are required")
	}
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, fmt.Errorf("body exceeds %d-byte bound", limit)
	}
	return payload, nil
}

func WriteJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func PostJSON(ctx context.Context, client *http.Client, target string, input, output any) error {
	if ctx == nil || client == nil || target == "" {
		return errors.New("post dependencies are required")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if len(payload) > MaxRequestBodyBytes {
		return errors.New("request exceeds body bound")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("post request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, MaxResponseBodyBytes))
		return fmt.Errorf("post response status %d", response.StatusCode)
	}
	if output == nil {
		_, err := readBounded(response.Body, MaxResponseBodyBytes)
		return err
	}
	return decodeBoundedJSON(response.Body, MaxResponseBodyBytes, output, "response")
}

func GetJSON(ctx context.Context, client *http.Client, target string, output any) error {
	if ctx == nil || client == nil || target == "" || output == nil {
		return errors.New("get dependencies are required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("get request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, MaxResponseBodyBytes))
		return fmt.Errorf("get response status %d", response.StatusCode)
	}
	return decodeBoundedJSON(response.Body, MaxResponseBodyBytes, output, "response")
}

func DefaultClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}
