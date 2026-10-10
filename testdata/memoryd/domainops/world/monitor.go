package world

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	MonitorProbeLimit       = 128
	MonitorProbeChargeLimit = 4
	monitorProbeSettle      = 500 * time.Millisecond
	monitorProbeVoidReason  = "synthetic-probe-reconciliation"
)

type MonitorStatus struct {
	Gateway GatewayStatus `json:"gateway"`
	Ledger  LedgerStatus  `json:"ledger"`
}

// MonitorProbeResult is one bounded customer-like observation. The monitor,
// not the caller, chooses the identity and cardinality. Receipt is copied from
// the public gateway boundary; Ledger is an aggregate observation for that
// exact synthetic identity. Observed is captured before the monitor reconciles
// effects owned by the probe; Ledger is the verified postcondition returned to
// the caller. Production identities are never eligible for this lifecycle.
type MonitorProbeResult struct {
	Receipt  GatewayReceipt `json:"receipt"`
	Observed LedgerStatus   `json:"observed"`
	Ledger   LedgerStatus   `json:"ledger"`
}

type Monitor struct {
	gatewayURL string
	ledgerURL  string
	client     *http.Client
	probeMu    sync.Mutex
	probes     int
}

func NewMonitor(gatewayURL, ledgerURL string) *Monitor {
	return &Monitor{gatewayURL: gatewayURL, ledgerURL: ledgerURL,
		client: DefaultClient(2 * time.Second)}
}

func (monitor *Monitor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		WriteJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /status", monitor.status)
	mux.HandleFunc("POST /probe", monitor.probe)
	return mux
}

func (monitor *Monitor) status(writer http.ResponseWriter, request *http.Request) {
	prefix := request.URL.Query().Get("prefix")
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	var gateway GatewayStatus
	if err := GetJSON(ctx, monitor.client, monitor.gatewayURL+"/status", &gateway); err != nil {
		WriteJSON(writer, http.StatusBadGateway, map[string]string{"error": "gateway unavailable"})
		return
	}
	var ledger LedgerStatus
	ledgerTarget := monitor.ledgerURL + "/status?prefix=" + url.QueryEscape(prefix)
	if err := GetJSON(ctx, monitor.client, ledgerTarget, &ledger); err != nil {
		WriteJSON(writer, http.StatusBadGateway, map[string]string{"error": "ledger unavailable"})
		return
	}
	WriteJSON(writer, http.StatusOK, MonitorStatus{Gateway: gateway, Ledger: ledger})
}

func (monitor *Monitor) probe(writer http.ResponseWriter, request *http.Request) {
	var input struct{}
	if err := DecodeJSON(request, &input); err != nil {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid probe"})
		return
	}

	// Keep the full real-world effect serialized. A queued caller cannot choose
	// an identity, fan out, or race the global bound.
	monitor.probeMu.Lock()
	defer monitor.probeMu.Unlock()
	if monitor.probes >= MonitorProbeLimit {
		WriteJSON(writer, http.StatusTooManyRequests,
			map[string]string{"error": "probe limit reached"})
		return
	}
	monitor.probes++
	businessID := fmt.Sprintf("synthetic-%03d", monitor.probes)

	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	var checkout CheckoutResponse
	// A failed public response remains useful evidence. The exact gateway
	// receipt below, rather than this transport result, records what happened at
	// the public boundary.
	_ = PostJSON(ctx, monitor.client, monitor.gatewayURL+"/checkout",
		PayRequest{BusinessID: businessID}, &checkout)
	timer := time.NewTimer(monitorProbeSettle)
	select {
	case <-ctx.Done():
		timer.Stop()
		WriteJSON(writer, http.StatusGatewayTimeout,
			map[string]string{"error": "probe observation timed out"})
		return
	case <-timer.C:
	}

	var history GatewayHistory
	historyTarget := monitor.gatewayURL + "/history?prefix=" + url.QueryEscape(businessID)
	if err := GetJSON(ctx, monitor.client, historyTarget, &history); err != nil ||
		len(history.Entries) != 1 || history.Entries[0].BusinessID != businessID {
		WriteJSON(writer, http.StatusBadGateway,
			map[string]string{"error": "probe receipt unavailable"})
		return
	}
	observed, ledger, err := monitor.reconcileProbe(ctx, businessID, history.Entries[0])
	if err != nil {
		WriteJSON(writer, http.StatusBadGateway,
			map[string]string{"error": "probe reconciliation incomplete"})
		return
	}
	WriteJSON(writer, http.StatusOK,
		MonitorProbeResult{Receipt: history.Entries[0], Observed: observed, Ledger: ledger})
}

func (monitor *Monitor) reconcileProbe(ctx context.Context, businessID string,
	receipt GatewayReceipt,
) (LedgerStatus, LedgerStatus, error) {
	charges, observed, err := monitor.readProbeLedger(ctx, businessID)
	if err != nil {
		return LedgerStatus{}, LedgerStatus{}, err
	}
	if err := validateProbeReceipt(receipt, businessID); err != nil {
		return LedgerStatus{}, LedgerStatus{}, err
	}
	for _, charge := range charges {
		if charge.State != ChargeActive ||
			(receipt.Status == GatewayReceiptSucceeded && charge.Sequence == receipt.CaptureID) {
			continue
		}
		var voided Charge
		if err := PostJSON(ctx, monitor.client, monitor.ledgerURL+"/admin/void",
			VoidRequest{Sequence: charge.Sequence, Reason: monitorProbeVoidReason}, &voided); err != nil {
			return LedgerStatus{}, LedgerStatus{}, fmt.Errorf("void probe charge: %w", err)
		}
		if voided.Sequence != charge.Sequence || voided.BusinessID != businessID ||
			voided.State != ChargeVoided || voided.VoidReason != monitorProbeVoidReason {
			return LedgerStatus{}, LedgerStatus{}, fmt.Errorf("invalid probe void receipt")
		}
	}

	settledCharges, settled, err := monitor.readProbeLedger(ctx, businessID)
	if err != nil {
		return LedgerStatus{}, LedgerStatus{}, err
	}
	if err := validateSettledProbe(receipt, settledCharges, settled); err != nil {
		return LedgerStatus{}, LedgerStatus{}, err
	}
	return observed, settled, nil
}

func (monitor *Monitor) readProbeLedger(ctx context.Context, businessID string) (
	[]Charge, LedgerStatus, error,
) {
	query := url.QueryEscape(businessID)
	var charges []Charge
	if err := GetJSON(ctx, monitor.client, monitor.ledgerURL+"/charges?prefix="+query,
		&charges); err != nil {
		return nil, LedgerStatus{}, fmt.Errorf("read probe charges: %w", err)
	}
	if len(charges) > MonitorProbeChargeLimit {
		return nil, LedgerStatus{}, fmt.Errorf("probe charge bound exceeded")
	}
	for _, charge := range charges {
		if charge.BusinessID != businessID || charge.Sequence <= 0 ||
			(charge.State != ChargeActive && charge.State != ChargeVoided) {
			return nil, LedgerStatus{}, fmt.Errorf("invalid probe charge")
		}
	}
	var status LedgerStatus
	if err := GetJSON(ctx, monitor.client, monitor.ledgerURL+"/status?prefix="+query,
		&status); err != nil {
		return nil, LedgerStatus{}, fmt.Errorf("read probe ledger status: %w", err)
	}
	if status.Charges != len(charges) {
		return nil, LedgerStatus{}, fmt.Errorf("probe ledger observation is inconsistent")
	}
	return charges, status, nil
}

func validateProbeReceipt(receipt GatewayReceipt, businessID string) error {
	if receipt.BusinessID != businessID || receipt.RequestID <= 0 ||
		(receipt.Route != "east" && receipt.Route != "west") {
		return fmt.Errorf("invalid probe receipt")
	}
	if receipt.Status == GatewayReceiptSucceeded && receipt.CaptureID > 0 {
		return nil
	}
	if receipt.Status == GatewayReceiptFailed && receipt.CaptureID == 0 {
		return nil
	}
	return fmt.Errorf("invalid probe receipt outcome")
}

func validateSettledProbe(receipt GatewayReceipt, charges []Charge, status LedgerStatus) error {
	active := 0
	captureActive := false
	for _, charge := range charges {
		if charge.State != ChargeActive {
			continue
		}
		active++
		captureActive = captureActive || charge.Sequence == receipt.CaptureID
	}
	if status.ActiveCharges != active || status.VoidedCharges != len(charges)-active ||
		status.DuplicateBusinesses != 0 {
		return fmt.Errorf("probe ledger postcondition is inconsistent")
	}
	if receipt.Status == GatewayReceiptSucceeded && active == 1 && captureActive &&
		status.UniqueBusinesses == 1 {
		return nil
	}
	if receipt.Status == GatewayReceiptFailed && active == 0 &&
		status.UniqueBusinesses == 0 {
		return nil
	}
	return fmt.Errorf("probe ledger postcondition is not reconciled")
}
