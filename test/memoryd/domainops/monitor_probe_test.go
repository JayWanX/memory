package domainops_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JayWanX/memory/testdata/memoryd/domainops/world"
)

func TestMonitorProbeRunsOneServerNamedCheckout(t *testing.T) {
	monitorURL, _ := newMonitorProbeFixture(t, 0, world.PaymentConfig{
		TimeoutMillis: 500,
		StableKeys:    true,
		Retries:       1,
	})

	for index := 1; index <= 2; index++ {
		var result world.MonitorProbeResult
		if err := world.PostJSON(context.Background(), world.DefaultClient(3*time.Second),
			monitorURL+"/probe", struct{}{}, &result); err != nil {
			t.Fatalf("probe %d: %v", index, err)
		}
		wantID := fmt.Sprintf("synthetic-%03d", index)
		if result.Receipt.BusinessID != wantID ||
			result.Receipt.Status != world.GatewayReceiptSucceeded ||
			result.Receipt.CaptureID <= 0 ||
			result.Observed != (world.LedgerStatus{Charges: 1, ActiveCharges: 1,
				UniqueBusinesses: 1}) ||
			result.Ledger != (world.LedgerStatus{Charges: 1, ActiveCharges: 1,
				UniqueBusinesses: 1}) {
			t.Fatalf("probe %d result = %+v", index, result)
		}
	}
}

func TestMonitorProbeReconcilesOnlyItsAcknowledgedCapture(t *testing.T) {
	monitorURL, ledgerURL := newMonitorProbeFixture(t, 300*time.Millisecond,
		world.PaymentConfig{
			TimeoutMillis: 100,
			StableKeys:    false,
			Retries:       2,
		})

	var result world.MonitorProbeResult
	if err := world.PostJSON(context.Background(), world.DefaultClient(3*time.Second),
		monitorURL+"/probe", struct{}{}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Status != world.GatewayReceiptSucceeded ||
		result.Receipt.CaptureID <= 0 ||
		result.Observed != (world.LedgerStatus{Charges: 2, ActiveCharges: 2,
			UniqueBusinesses: 1, DuplicateBusinesses: 1}) ||
		result.Ledger != (world.LedgerStatus{Charges: 2, ActiveCharges: 1,
			VoidedCharges: 1, UniqueBusinesses: 1}) {
		t.Fatalf("probe result = %+v", result)
	}

	var charges []world.Charge
	if err := world.GetJSON(context.Background(), world.DefaultClient(time.Second),
		ledgerURL+"/charges?prefix=synthetic-001", &charges); err != nil {
		t.Fatal(err)
	}
	if len(charges) != 2 {
		t.Fatalf("probe charges = %+v", charges)
	}
	for _, charge := range charges {
		if charge.Sequence == result.Receipt.CaptureID {
			if charge.State != world.ChargeActive {
				t.Fatalf("acknowledged capture = %+v", charge)
			}
			continue
		}
		if charge.State != world.ChargeVoided ||
			charge.VoidReason != "synthetic-probe-reconciliation" {
			t.Fatalf("extra capture = %+v", charge)
		}
	}
}

func TestMonitorProbeReconcilesFailedCheckoutEffects(t *testing.T) {
	monitorURL, _ := newMonitorProbeFixture(t, 300*time.Millisecond,
		world.PaymentConfig{
			TimeoutMillis: 100,
			StableKeys:    false,
			Retries:       1,
		})

	var result world.MonitorProbeResult
	if err := world.PostJSON(context.Background(), world.DefaultClient(3*time.Second),
		monitorURL+"/probe", struct{}{}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Status != world.GatewayReceiptFailed || result.Receipt.CaptureID != 0 ||
		result.Observed != (world.LedgerStatus{Charges: 1, ActiveCharges: 1,
			UniqueBusinesses: 1}) ||
		result.Ledger != (world.LedgerStatus{Charges: 1, VoidedCharges: 1}) {
		t.Fatalf("probe result = %+v", result)
	}
}

func newMonitorProbeFixture(t *testing.T, latency time.Duration,
	config world.PaymentConfig,
) (string, string) {
	t.Helper()
	ledger := httptest.NewServer(world.NewLedger().Handler())
	t.Cleanup(ledger.Close)
	callback := httptest.NewServer(world.NewCallback(latency, ledger.URL).Handler())
	t.Cleanup(callback.Close)
	payment := httptest.NewServer(world.NewPayment(config, callback.URL).Handler())
	t.Cleanup(payment.Close)
	gateway := httptest.NewServer(world.NewGateway("east", payment.URL, payment.URL).Handler())
	t.Cleanup(gateway.Close)
	monitor := httptest.NewServer(world.NewMonitor(gateway.URL, ledger.URL).Handler())
	t.Cleanup(monitor.Close)
	return monitor.URL, ledger.URL
}

func TestMonitorProbeRejectsCallerParameters(t *testing.T) {
	monitor := httptest.NewServer(world.NewMonitor(
		"http://gateway.invalid", "http://ledger.invalid").Handler())
	t.Cleanup(monitor.Close)

	response, err := http.Post(monitor.URL+"/probe", "application/json",
		bytes.NewBufferString(`{"count":2}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("parameterized probe status = %d", response.StatusCode)
	}
}
