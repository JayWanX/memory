package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JayWanX/memory/testdata/memoryd/domainops/world"
)

func TestParseConfigurationAcceptsClosedOptionsBeforeOrAfterOperands(t *testing.T) {
	t.Parallel()

	getenv := func(name string) string {
		return map[string]string{"DOMAIN_ROLE": "payment", "DOMAIN_ENDPOINT": "http://east:8080"}[name]
	}
	payload := `{"timeout_ms":250,"stable_keys":true,"retries":0}`
	wantArgs := []string{"action", "/admin/config", payload}
	for _, arguments := range [][]string{
		{"--endpoint", "http://west:8080", "--timeout=9s", "action", "/admin/config", payload},
		{"action", "/admin/config", payload, "--endpoint=http://west:8080", "--timeout", "9s"},
	} {
		config, err := parseConfigurationArgs(arguments, getenv)
		if err != nil {
			t.Fatal(err)
		}
		if config.role != "payment" || config.endpoint != "http://west:8080" ||
			config.timeout != 9*time.Second || !reflect.DeepEqual(config.args, wantArgs) {
			t.Fatalf("config = %#v", config)
		}
	}
}

func TestParseConfigurationRejectsInvalidClosedOptions(t *testing.T) {
	t.Parallel()

	getenv := func(name string) string {
		return map[string]string{"DOMAIN_ROLE": "payment", "DOMAIN_ENDPOINT": "http://east:8080"}[name]
	}
	for name, arguments := range map[string][]string{
		"duplicate": {"status", "--endpoint", "http://west:8080", "--endpoint=http://east:8080"},
		"unknown":   {"status", "--region", "west"},
		"missing":   {"status", "--endpoint"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseConfigurationArgs(arguments, getenv); err == nil {
				t.Fatal("invalid options were accepted")
			}
		})
	}
}

func TestEndpointOverrideAfterCommandSelectsTheRequestedInstance(t *testing.T) {
	t.Parallel()

	defaultCalls, overrideCalls := 0, 0
	defaultServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		defaultCalls++
	}))
	t.Cleanup(defaultServer.Close)
	overrideServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		overrideCalls++
		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(overrideServer.Close)
	getenv := func(name string) string {
		return map[string]string{"DOMAIN_ROLE": "platform", "DOMAIN_ENDPOINT": defaultServer.URL}[name]
	}
	config, err := parseConfigurationArgs(
		[]string{"status", "--endpoint", overrideServer.URL}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if defaultCalls != 0 || overrideCalls != 1 {
		t.Fatalf("default calls = %d, override calls = %d", defaultCalls, overrideCalls)
	}
}

func TestResolveAllowsGatewayHistoryWithoutBroadeningControlSurface(t *testing.T) {
	t.Parallel()

	target, err := resolve("http://gateway:8080", "/history?prefix=incident-")
	if err != nil {
		t.Fatalf("resolve gateway history: %v", err)
	}
	if target != "http://gateway:8080/history?prefix=incident-" {
		t.Fatalf("history target = %q", target)
	}

	if _, err := resolve("http://gateway:8080", "/requests"); err == nil {
		t.Fatal("unreviewed read surface was accepted")
	}
}

func TestProbeHasNoCallerControlledShape(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost || request.URL.Path != "/probe" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != "{}" {
			t.Errorf("probe body = %q", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]bool{"observed": true})
	}))
	t.Cleanup(server.Close)

	result, err := execute(context.Background(), configuration{
		role: "lead", endpoint: server.URL, args: []string{"probe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "{\"observed\":true}\n" {
		t.Fatalf("probe result = %q", result)
	}
}

func TestRequestEnforcesClosedControlResponseBound(t *testing.T) {
	t.Parallel()

	requestPayload := func(payload string) error {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
			_ *http.Request,
		) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, payload)
		}))
		defer server.Close()
		_, err := request(context.Background(), http.MethodGet, server.URL, "/status", nil)
		return err
	}

	bounded := `"` + strings.Repeat("a", maxControlResponseBytes-2) + `"`
	if err := requestPayload(bounded); err != nil {
		t.Fatalf("bounded control response: %v", err)
	}
	overflow := `"` + strings.Repeat("a", maxControlResponseBytes-1) + `"`
	if err := requestPayload(overflow); err == nil {
		t.Fatal("oversized control response was accepted")
	}
}

func TestControlBoundCoversMaximumSyntheticChargeAudit(t *testing.T) {
	t.Parallel()
	charges := make([]world.Charge, 0,
		world.MonitorProbeLimit*world.MonitorProbeChargeLimit)
	for probe := 1; probe <= world.MonitorProbeLimit; probe++ {
		for attempt := 1; attempt <= world.MonitorProbeChargeLimit; attempt++ {
			charges = append(charges, world.Charge{Sequence: int64(len(charges) + 1),
				BusinessID: fmt.Sprintf("synthetic-%03d", probe),
				AttemptKey: fmt.Sprintf("synthetic-%03d-attempt-%d", probe, attempt),
				State:      world.ChargeVoided,
				VoidReason: "synthetic-probe-reconciliation"})
		}
	}
	payload, err := json.Marshal(charges)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= 64<<10 || len(payload) > maxControlResponseBytes {
		t.Fatalf("maximum synthetic audit bytes = %d, control bound = %d",
			len(payload), maxControlResponseBytes)
	}
}

func TestActionRequestUsesTheWorldRequestBound(t *testing.T) {
	t.Parallel()
	valid := `"` + strings.Repeat("a", maxActionRequestBytes-2) + `"`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != valid {
			t.Errorf("action body length = %d, want %d", len(body), len(valid))
		}
		_, _ = io.WriteString(writer, `{}`)
	}))
	t.Cleanup(server.Close)

	if _, err := execute(context.Background(), configuration{role: "payment",
		endpoint: server.URL, args: []string{"action", "/admin/config", valid}}); err != nil {
		t.Fatalf("bounded action request: %v", err)
	}
	overflow := `"` + strings.Repeat("a", maxActionRequestBytes-1) + `"`
	if _, err := execute(context.Background(), configuration{role: "payment",
		endpoint: server.URL, args: []string{"action", "/admin/config", overflow}}); err == nil {
		t.Fatal("oversized action request was accepted")
	}
}
