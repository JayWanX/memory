package world

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	GatewayHistoryLimit     = 192
	GatewayReceiptSucceeded = "succeeded"
	GatewayReceiptFailed    = "failed"
)

type GatewayStatus struct {
	Route     string `json:"route"`
	Requests  int64  `json:"requests"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
}

// GatewayReceipt records the bounded observation made at the public gateway
// boundary. CaptureID is the exact value placed in a successful checkout
// response; failed requests retain zero rather than guessing downstream state.
type GatewayReceipt struct {
	RequestID  int64  `json:"request_id"`
	BusinessID string `json:"business_id"`
	CaptureID  int64  `json:"capture_id"`
	Route      string `json:"route"`
	Status     string `json:"status"`
}

type GatewayHistory struct {
	Limit   int              `json:"limit"`
	Entries []GatewayReceipt `json:"entries"`
}

type Gateway struct {
	mu        sync.Mutex
	route     string
	eastURL   string
	westURL   string
	client    *http.Client
	requests  int64
	succeeded int64
	failed    int64
	history   []GatewayReceipt
}

func NewGateway(route, eastURL, westURL string) *Gateway {
	return &Gateway{route: route, eastURL: eastURL, westURL: westURL,
		client: DefaultClient(4 * time.Second)}
}

func (gateway *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		WriteJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /checkout", gateway.checkout)
	mux.HandleFunc("GET /status", gateway.status)
	mux.HandleFunc("GET /history", gateway.readHistory)
	mux.HandleFunc("POST /admin/route", gateway.configureRoute)
	return mux
}

func (gateway *Gateway) checkout(writer http.ResponseWriter, request *http.Request) {
	var input PayRequest
	if err := DecodeJSON(request, &input); err != nil || !ValidToken(input.BusinessID) {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid checkout"})
		return
	}
	gateway.mu.Lock()
	gateway.requests++
	requestID := gateway.requests
	route := gateway.route
	target := gateway.eastURL
	if route == "west" {
		target = gateway.westURL
	}
	gateway.mu.Unlock()
	var result PayResponse
	err := PostJSON(context.Background(), gateway.client, target+"/pay", input, &result)
	gateway.mu.Lock()
	receipt := GatewayReceipt{RequestID: requestID, BusinessID: input.BusinessID,
		Route: route, Status: GatewayReceiptFailed}
	if err == nil {
		gateway.succeeded++
		receipt.Status = GatewayReceiptSucceeded
		receipt.CaptureID = result.CaptureID
	} else {
		gateway.failed++
	}
	gateway.appendReceiptLocked(receipt)
	gateway.mu.Unlock()
	if err != nil {
		WriteJSON(writer, http.StatusBadGateway, map[string]any{"accepted": false, "route": route})
		return
	}
	WriteJSON(writer, http.StatusOK, CheckoutResponse{Accepted: true, Route: route,
		CaptureID: result.CaptureID})
}

func (gateway *Gateway) readHistory(writer http.ResponseWriter, request *http.Request) {
	prefix := request.URL.Query().Get("prefix")
	if prefix != "" && !ValidToken(prefix) {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid history prefix"})
		return
	}
	gateway.mu.Lock()
	history := gateway.historyLocked(prefix)
	gateway.mu.Unlock()
	WriteJSON(writer, http.StatusOK, history)
}

func (gateway *Gateway) status(writer http.ResponseWriter, _ *http.Request) {
	gateway.mu.Lock()
	status := gateway.statusLocked()
	gateway.mu.Unlock()
	WriteJSON(writer, http.StatusOK, status)
}

func (gateway *Gateway) configureRoute(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Route string `json:"route"`
	}
	if err := DecodeJSON(request, &input); err != nil || (input.Route != "east" && input.Route != "west") {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid route"})
		return
	}
	gateway.mu.Lock()
	gateway.route = input.Route
	status := gateway.statusLocked()
	gateway.mu.Unlock()
	WriteJSON(writer, http.StatusOK, status)
}

func (gateway *Gateway) statusLocked() GatewayStatus {
	return GatewayStatus{Route: gateway.route, Requests: gateway.requests,
		Succeeded: gateway.succeeded, Failed: gateway.failed}
}

func (gateway *Gateway) appendReceiptLocked(receipt GatewayReceipt) {
	if len(gateway.history) < GatewayHistoryLimit {
		gateway.history = append(gateway.history, receipt)
		return
	}
	copy(gateway.history, gateway.history[1:])
	gateway.history[len(gateway.history)-1] = receipt
}

func (gateway *Gateway) historyLocked(prefix string) GatewayHistory {
	entries := make([]GatewayReceipt, 0, len(gateway.history))
	for _, receipt := range gateway.history {
		if strings.HasPrefix(receipt.BusinessID, prefix) {
			entries = append(entries, receipt)
		}
	}
	return GatewayHistory{Limit: GatewayHistoryLimit, Entries: entries}
}
