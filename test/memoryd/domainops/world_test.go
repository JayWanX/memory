package domainops_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	world "github.com/JayWanX/memory/testdata/memoryd/domainops/world"
)

const (
	requestTimeout = 2 * time.Second
	pollTimeout    = 3 * time.Second
)

type serviceWorld struct {
	gatewayURL      string
	monitorURL      string
	ledgerURL       string
	eastCallbackURL string
	westCallbackURL string
	eastPaymentURL  string
	westPaymentURL  string
}

func TestDefaultEastFaultReturnsSuccessAndDuplicatesCharge(t *testing.T) {
	services := newServiceWorld(t)
	prefix := "default-fault"

	result, err := checkout(services.gatewayURL, prefix+"-order-1")
	if err != nil {
		t.Fatalf("default East checkout failed: %v", err)
	}
	if result.CaptureID <= 0 {
		t.Fatalf("capture ID = %d, want a durable customer receipt", result.CaptureID)
	}

	status := waitForMonitor(t, services.monitorURL, prefix, func(status world.MonitorStatus) bool {
		return status.Gateway.Succeeded == 1 && status.Ledger.DuplicateBusinesses == 1
	})
	if status.Gateway.Route != "east" {
		t.Fatalf("gateway route = %q, want east", status.Gateway.Route)
	}
	if status.Gateway.Failed != 0 {
		t.Fatalf("gateway failures = %d, want 0", status.Gateway.Failed)
	}
	if status.Ledger.ActiveCharges != 2 || status.Ledger.UniqueBusinesses != 1 {
		t.Fatalf("ledger status = %+v, want two charges for one business", status.Ledger)
	}
	history := getGatewayHistory(t, services.gatewayURL, prefix)
	if len(history.Entries) != 1 || history.Entries[0].BusinessID != prefix+"-order-1" ||
		history.Entries[0].CaptureID != result.CaptureID ||
		history.Entries[0].Route != "east" ||
		history.Entries[0].Status != world.GatewayReceiptSucceeded {
		t.Fatalf("gateway history = %+v, want exact returned receipt", history)
	}
}

func TestGatewayHistoryIsReadOnlyAndBounded(t *testing.T) {
	var nextCapture atomic.Int64
	payment := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input world.PayRequest
		if err := world.DecodeJSON(request, &input); err != nil {
			world.WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid payment"})
			return
		}
		if input.BusinessID == "history-failed" {
			world.WriteJSON(writer, http.StatusBadGateway, map[string]bool{"paid": false})
			return
		}
		captureID := nextCapture.Add(1)
		world.WriteJSON(writer, http.StatusOK, world.PayResponse{Paid: true, Attempts: 1,
			CaptureID: captureID})
	}))
	t.Cleanup(payment.Close)

	gateway := httptest.NewServer(world.NewGateway("east", payment.URL, payment.URL).Handler())
	t.Cleanup(gateway.Close)

	succeeded, err := checkout(gateway.URL, "history-succeeded")
	if err != nil {
		t.Fatalf("successful checkout: %v", err)
	}
	if _, err := checkout(gateway.URL, "history-failed"); err == nil {
		t.Fatal("failed downstream checkout unexpectedly succeeded")
	}
	history := getGatewayHistory(t, gateway.URL, "history-")
	if history.Limit != world.GatewayHistoryLimit || len(history.Entries) != 2 {
		t.Fatalf("gateway history = %+v", history)
	}
	if history.Entries[0].Status != world.GatewayReceiptSucceeded ||
		history.Entries[0].CaptureID != succeeded.CaptureID ||
		history.Entries[0].Route != "east" {
		t.Fatalf("successful receipt = %+v", history.Entries[0])
	}
	if history.Entries[1].Status != world.GatewayReceiptFailed ||
		history.Entries[1].CaptureID != 0 || history.Entries[1].Route != "east" {
		t.Fatalf("failed receipt = %+v", history.Entries[1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := world.PostJSON(ctx, world.DefaultClient(requestTimeout), gateway.URL+"/history",
		map[string]string{}, nil); err == nil {
		t.Fatal("gateway history accepted a mutation")
	}

	for index := 1; index <= world.GatewayHistoryLimit+3; index++ {
		businessID := maxWidthBusinessID(index)
		if _, err := checkout(gateway.URL, businessID); err != nil {
			t.Fatalf("bounded checkout %q: %v", businessID, err)
		}
	}
	history = getGatewayHistory(t, gateway.URL, "")
	if len(history.Entries) != world.GatewayHistoryLimit {
		t.Fatalf("history entries = %d, want hard limit %d", len(history.Entries),
			world.GatewayHistoryLimit)
	}
	lastBoundedBusiness := maxWidthBusinessID(world.GatewayHistoryLimit + 3)
	if history.Entries[0].BusinessID != maxWidthBusinessID(4) ||
		history.Entries[len(history.Entries)-1].BusinessID != lastBoundedBusiness {
		t.Fatalf("bounded history retained wrong window: first=%q last=%q",
			history.Entries[0].BusinessID,
			history.Entries[len(history.Entries)-1].BusinessID)
	}
}

func maxWidthBusinessID(index int) string {
	suffix := fmt.Sprintf("-%03d", index)
	return strings.Repeat("b", 96-len(suffix)) + suffix
}

func TestDistinctRemediationsSatisfySameOutcomeOracle(t *testing.T) {
	remediations := []struct {
		name  string
		apply func(*testing.T, serviceWorld)
	}{
		{
			name: "route_new_traffic_to_healthy_region",
			apply: func(t *testing.T, services serviceWorld) {
				postAccepted(t, services.gatewayURL+"/admin/route", map[string]string{
					"route": "west",
				})
			},
		},
		{
			name: "stabilize_payment_retry_configuration",
			apply: func(t *testing.T, services serviceWorld) {
				postAccepted(t, services.eastPaymentURL+"/admin/config", world.PaymentConfig{
					TimeoutMillis: 500,
					StableKeys:    true,
					Retries:       2,
				})
			},
		},
	}

	for _, remediation := range remediations {
		remediation := remediation
		t.Run(remediation.name, func(t *testing.T) {
			services := newServiceWorld(t)
			prefix := "incident-" + remediation.name

			customerCapture := induceIncident(t, services, prefix)
			remediation.apply(t, services)
			voidDuplicateCharges(t, services.ledgerURL, prefix, customerCapture)

			assertRecoveredOutcome(t, services, prefix, 3)
		})
	}
}

func TestSecondRegionalVariantSatisfiesTheSameOutcomeOracle(t *testing.T) {
	remediations := []struct {
		name  string
		apply func(*testing.T, serviceWorld)
	}{
		{
			name: "route_new_traffic_to_recovered_region",
			apply: func(t *testing.T, services serviceWorld) {
				postAccepted(t, services.gatewayURL+"/admin/route", map[string]string{
					"route": "east",
				})
			},
		},
		{
			name: "stabilize_second_region_retry_configuration",
			apply: func(t *testing.T, services serviceWorld) {
				postAccepted(t, services.westPaymentURL+"/admin/config", world.PaymentConfig{
					TimeoutMillis: 500,
					StableKeys:    true,
					Retries:       2,
				})
			},
		},
	}

	for _, remediation := range remediations {
		remediation := remediation
		t.Run(remediation.name, func(t *testing.T) {
			services := newServiceWorld(t)
			// Establish a healthy alternative before injecting the second,
			// region-reversed incident. The later oracle is intentionally the
			// same one used by the first incident and does not inspect the repair.
			postAccepted(t, services.eastPaymentURL+"/admin/config", world.PaymentConfig{
				TimeoutMillis: 500,
				StableKeys:    true,
				Retries:       2,
			})
			postAccepted(t, services.westCallbackURL+"/admin/latency", map[string]int64{
				"latency_ms": 150,
			})
			postAccepted(t, services.westPaymentURL+"/admin/config", world.PaymentConfig{
				TimeoutMillis: 50,
				StableKeys:    false,
				Retries:       2,
			})
			postAccepted(t, services.gatewayURL+"/admin/route", map[string]string{
				"route": "west",
			})

			prefix := "second-variant-" + remediation.name
			customerCapture := induceIncident(t, services, prefix)
			remediation.apply(t, services)
			voidDuplicateCharges(t, services.ledgerURL, prefix, customerCapture)

			assertRecoveredOutcome(t, services, prefix, 3)
		})
	}
}

func newServiceWorld(t *testing.T) serviceWorld {
	t.Helper()

	ledger := httptest.NewServer(world.NewLedger().Handler())
	t.Cleanup(ledger.Close)

	eastCallback := httptest.NewServer(world.NewCallback(150*time.Millisecond, ledger.URL).Handler())
	t.Cleanup(eastCallback.Close)
	westCallback := httptest.NewServer(world.NewCallback(5*time.Millisecond, ledger.URL).Handler())
	t.Cleanup(westCallback.Close)

	eastPayment := httptest.NewServer(world.NewPayment(world.PaymentConfig{
		TimeoutMillis: 50,
		StableKeys:    false,
		Retries:       2,
	}, eastCallback.URL).Handler())
	t.Cleanup(eastPayment.Close)
	westPayment := httptest.NewServer(world.NewPayment(world.PaymentConfig{
		TimeoutMillis: 500,
		StableKeys:    true,
		Retries:       2,
	}, westCallback.URL).Handler())
	t.Cleanup(westPayment.Close)

	gateway := httptest.NewServer(world.NewGateway("east", eastPayment.URL, westPayment.URL).Handler())
	t.Cleanup(gateway.Close)
	monitor := httptest.NewServer(world.NewMonitor(gateway.URL, ledger.URL).Handler())
	t.Cleanup(monitor.Close)

	return serviceWorld{
		gatewayURL:      gateway.URL,
		monitorURL:      monitor.URL,
		ledgerURL:       ledger.URL,
		eastCallbackURL: eastCallback.URL,
		westCallbackURL: westCallback.URL,
		eastPaymentURL:  eastPayment.URL,
		westPaymentURL:  westPayment.URL,
	}
}

func induceIncident(t *testing.T, services serviceWorld, prefix string) int64 {
	t.Helper()
	businessID := prefix + "-original"
	result, err := checkout(services.gatewayURL, businessID)
	if err != nil {
		t.Fatalf("incident checkout failed before remediation: %v", err)
	}
	waitForMonitor(t, services.monitorURL, prefix, func(status world.MonitorStatus) bool {
		return status.Gateway.Succeeded == 1 && status.Ledger.DuplicateBusinesses == 1
	})
	history := getGatewayHistory(t, services.gatewayURL, businessID)
	if len(history.Entries) != 1 || history.Entries[0].BusinessID != businessID ||
		history.Entries[0].CaptureID != result.CaptureID ||
		history.Entries[0].Status != world.GatewayReceiptSucceeded {
		t.Fatalf("gateway did not retain the returned customer receipt: %+v", history)
	}
	return history.Entries[0].CaptureID
}

func voidDuplicateCharges(t *testing.T, ledgerURL, prefix string, preserve int64) {
	t.Helper()
	charges := getCharges(t, ledgerURL, prefix)
	voided := 0
	for _, charge := range charges {
		if charge.State == world.ChargeActive && charge.Sequence != preserve {
			postAccepted(t, ledgerURL+"/admin/void", world.VoidRequest{
				Sequence: charge.Sequence,
				Reason:   "duplicate capture confirmed by incident reconciliation",
			})
			voided++
		}
	}
	if voided != 1 {
		t.Fatalf("voided charges = %d, want one explicit duplicate", voided)
	}
	status := getLedgerStatus(t, ledgerURL, prefix)
	if status.DuplicateBusinesses != 0 || status.VoidedCharges != 1 {
		t.Fatalf("ledger status after reconciliation = %+v, want one retained void", status)
	}
}

// assertRecoveredOutcome is intentionally shared by every remediation. It
// observes user traffic and durable ledger state, not which component changed.
func assertRecoveredOutcome(t *testing.T, services serviceWorld, prefix string, evaluations int) {
	t.Helper()
	for index := 1; index <= evaluations; index++ {
		businessID := fmt.Sprintf("%s-evaluation-%d", prefix, index)
		if _, err := checkout(services.gatewayURL, businessID); err != nil {
			t.Fatalf("evaluation checkout %q failed: %v", businessID, err)
		}
	}

	status := waitForMonitor(t, services.monitorURL, prefix, func(status world.MonitorStatus) bool {
		return status.Gateway.Succeeded == int64(evaluations+1) &&
			status.Ledger.DuplicateBusinesses == 0 &&
			status.Ledger.UniqueBusinesses == evaluations+1 &&
			status.Ledger.ActiveCharges == evaluations+1 &&
			status.Ledger.VoidedCharges == 1
	})
	if status.Gateway.Failed != 0 {
		t.Fatalf("gateway failures = %d, want all customer requests to return", status.Gateway.Failed)
	}
	if status.Ledger.Charges != evaluations+2 {
		t.Fatalf("ledger charges = %d, want audit history retained", status.Ledger.Charges)
	}
	assertGatewayReceiptsMatchActiveCharges(t, services, prefix, evaluations+1)
}

func checkout(gatewayURL, businessID string) (world.CheckoutResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var result world.CheckoutResponse
	err := world.PostJSON(ctx, world.DefaultClient(requestTimeout), gatewayURL+"/checkout",
		world.PayRequest{BusinessID: businessID}, &result)
	return result, err
}

func postAccepted(t *testing.T, target string, input any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var result map[string]any
	if err := world.PostJSON(ctx, world.DefaultClient(requestTimeout), target, input, &result); err != nil {
		t.Fatalf("POST %s failed: %v", target, err)
	}
}

func getLedgerStatus(t *testing.T, ledgerURL, prefix string) world.LedgerStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var status world.LedgerStatus
	target := ledgerURL + "/status?prefix=" + url.QueryEscape(prefix)
	if err := world.GetJSON(ctx, world.DefaultClient(requestTimeout), target, &status); err != nil {
		t.Fatalf("GET ledger status failed: %v", err)
	}
	return status
}

func getCharges(t *testing.T, ledgerURL, prefix string) []world.Charge {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var charges []world.Charge
	target := ledgerURL + "/charges?prefix=" + url.QueryEscape(prefix)
	if err := world.GetJSON(ctx, world.DefaultClient(requestTimeout), target, &charges); err != nil {
		t.Fatalf("GET ledger charges failed: %v", err)
	}
	return charges
}

func getGatewayHistory(t *testing.T, gatewayURL, prefix string) world.GatewayHistory {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var history world.GatewayHistory
	target := gatewayURL + "/history?prefix=" + url.QueryEscape(prefix)
	if err := world.GetJSON(ctx, world.DefaultClient(requestTimeout), target, &history); err != nil {
		t.Fatalf("GET gateway history failed: %v", err)
	}
	return history
}

func assertGatewayReceiptsMatchActiveCharges(
	t *testing.T,
	services serviceWorld,
	prefix string,
	want int,
) {
	t.Helper()
	history := getGatewayHistory(t, services.gatewayURL, prefix)
	if len(history.Entries) != want {
		t.Fatalf("gateway receipts = %d, want %d", len(history.Entries), want)
	}
	active := make(map[string]int64)
	for _, charge := range getCharges(t, services.ledgerURL, prefix) {
		if charge.State == world.ChargeActive {
			active[charge.BusinessID] = charge.Sequence
		}
	}
	for _, receipt := range history.Entries {
		if receipt.Status != world.GatewayReceiptSucceeded || receipt.CaptureID <= 0 ||
			active[receipt.BusinessID] != receipt.CaptureID {
			t.Fatalf("gateway receipt %+v does not identify the retained active capture", receipt)
		}
	}
}

func waitForMonitor(
	t *testing.T,
	monitorURL string,
	prefix string,
	ready func(world.MonitorStatus) bool,
) world.MonitorStatus {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	var last world.MonitorStatus
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		target := monitorURL + "/status?prefix=" + url.QueryEscape(prefix)
		lastErr = world.GetJSON(ctx, world.DefaultClient(250*time.Millisecond), target, &last)
		cancel()
		if lastErr == nil && ready(last) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("monitor condition was not met: last=%+v error=%v", last, lastErr)
	return world.MonitorStatus{}
}
