package world

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type CallbackStatus struct {
	LatencyMillis int64 `json:"latency_ms"`
	Requests      int64 `json:"requests"`
	Delayed       int64 `json:"delayed"`
	Delivered     int64 `json:"delivered"`
}

type Callback struct {
	mu        sync.Mutex
	latency   time.Duration
	ledgerURL string
	client    *http.Client
	seen      map[string]bool
	requests  int64
	delayed   int64
	delivered int64
}

func NewCallback(latency time.Duration, ledgerURL string) *Callback {
	return &Callback{latency: latency, ledgerURL: ledgerURL, seen: make(map[string]bool),
		client: DefaultClient(3 * time.Second)}
}

func (callback *Callback) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		WriteJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /callback", callback.deliver)
	mux.HandleFunc("GET /status", callback.status)
	mux.HandleFunc("POST /admin/latency", callback.configureLatency)
	return mux
}

func (callback *Callback) deliver(writer http.ResponseWriter, request *http.Request) {
	var input ChargeRequest
	if err := DecodeJSON(request, &input); err != nil || !ValidToken(input.BusinessID) ||
		!ValidToken(input.AttemptKey) {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid callback"})
		return
	}
	callback.mu.Lock()
	callback.requests++
	latency := time.Duration(0)
	if !callback.seen[input.BusinessID] {
		callback.seen[input.BusinessID] = true
		callback.delayed++
		latency = callback.latency
	}
	callback.mu.Unlock()

	// The provider side effect intentionally survives caller timeout. This is
	// the realistic boundary that makes unstable retry keys dangerous.
	time.Sleep(latency)
	var response ChargeResponse
	err := PostJSON(context.Background(), callback.client, callback.ledgerURL+"/charge", input, &response)
	if err != nil {
		WriteJSON(writer, http.StatusBadGateway, map[string]string{"error": "ledger unavailable"})
		return
	}
	callback.mu.Lock()
	callback.delivered++
	callback.mu.Unlock()
	WriteJSON(writer, http.StatusOK, response)
}

func (callback *Callback) status(writer http.ResponseWriter, _ *http.Request) {
	callback.mu.Lock()
	status := CallbackStatus{LatencyMillis: callback.latency.Milliseconds(),
		Requests: callback.requests, Delayed: callback.delayed, Delivered: callback.delivered}
	callback.mu.Unlock()
	WriteJSON(writer, http.StatusOK, status)
}

func (callback *Callback) configureLatency(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		LatencyMillis int64 `json:"latency_ms"`
	}
	if err := DecodeJSON(request, &input); err != nil || input.LatencyMillis < 0 ||
		input.LatencyMillis > 5000 {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid latency"})
		return
	}
	callback.mu.Lock()
	callback.latency = time.Duration(input.LatencyMillis) * time.Millisecond
	status := CallbackStatus{LatencyMillis: input.LatencyMillis,
		Requests: callback.requests, Delayed: callback.delayed, Delivered: callback.delivered}
	callback.mu.Unlock()
	WriteJSON(writer, http.StatusOK, status)
}
