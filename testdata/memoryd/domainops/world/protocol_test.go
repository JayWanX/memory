package world

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeJSONEnforcesTheExactRequestByteBound(t *testing.T) {
	t.Parallel()
	decode := func(payload string) error {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
		var value string
		return DecodeJSON(request, &value)
	}

	bounded := `"` + strings.Repeat("a", MaxRequestBodyBytes-2) + `"`
	if err := decode(bounded); err != nil {
		t.Fatalf("bounded request: %v", err)
	}
	for _, payload := range []string{
		`"` + strings.Repeat("a", MaxRequestBodyBytes-1) + `"`,
		`"ok"` + strings.Repeat(" ", MaxRequestBodyBytes),
	} {
		if err := decode(payload); err == nil {
			t.Fatal("oversized request was accepted")
		}
	}
}

func TestGetJSONEnforcesBoundAndSingleValueResponse(t *testing.T) {
	t.Parallel()
	payload := `"` + strings.Repeat("a", MaxResponseBodyBytes-2) + `"`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = io.WriteString(writer, payload)
	}))
	t.Cleanup(server.Close)
	read := func() error {
		var value string
		return GetJSON(context.Background(), DefaultClient(time.Second), server.URL, &value)
	}
	if err := read(); err != nil {
		t.Fatalf("bounded response: %v", err)
	}
	payload = `"` + strings.Repeat("a", MaxResponseBodyBytes-1) + `"`
	if err := read(); err == nil {
		t.Fatal("oversized response was accepted")
	}
	payload = `{} {}`
	if err := read(); err == nil {
		t.Fatal("trailing response value was accepted")
	}
}

func TestPostJSONRejectsOversizedRequestBeforeTransport(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(server.Close)

	input := strings.Repeat("a", MaxRequestBodyBytes)
	err := PostJSON(context.Background(), DefaultClient(time.Second), server.URL, input, nil)
	if err == nil {
		t.Fatal("oversized request was accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("oversized request reached the transport")
	}
}

func TestReadBoundedRejectsBytesPastLimit(t *testing.T) {
	t.Parallel()
	if _, err := readBounded(bytes.NewReader([]byte("1234")), 3); err == nil {
		t.Fatal("reader accepted bytes past its exact limit")
	}
}
