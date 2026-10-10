package world

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMonitorRejectsBeforeEffectAtGlobalProbeLimit(t *testing.T) {
	t.Parallel()
	var gatewayCalls, ledgerCalls atomic.Int64
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		_ *http.Request,
	) {
		gatewayCalls.Add(1)
		http.Error(writer, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(gateway.Close)
	ledger := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		_ *http.Request,
	) {
		ledgerCalls.Add(1)
		http.Error(writer, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(ledger.Close)
	monitor := NewMonitor(gateway.URL, ledger.URL)
	monitor.probes = MonitorProbeLimit
	request := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(`{}`))
	response := httptest.NewRecorder()

	monitor.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("probe status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	if monitor.probes != MonitorProbeLimit {
		t.Fatalf("probe count = %d, want unchanged hard limit", monitor.probes)
	}
	if gatewayCalls.Load() != 0 || ledgerCalls.Load() != 0 {
		t.Fatalf("exhausted probe reached effects: gateway=%d ledger=%d",
			gatewayCalls.Load(), ledgerCalls.Load())
	}
}
