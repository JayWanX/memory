package world

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type PaymentConfig struct {
	TimeoutMillis int64 `json:"timeout_ms"`
	StableKeys    bool  `json:"stable_keys"`
	Retries       int   `json:"retries"`
}

type PaymentStatus struct {
	Config    PaymentConfig `json:"config"`
	Requests  int64         `json:"requests"`
	Succeeded int64         `json:"succeeded"`
	Failed    int64         `json:"failed"`
	Attempts  int64         `json:"attempts"`
}

type Payment struct {
	mu          sync.Mutex
	config      PaymentConfig
	callbackURL string
	requests    int64
	succeeded   int64
	failed      int64
	attempts    int64
}

func NewPayment(config PaymentConfig, callbackURL string) *Payment {
	return &Payment{config: config, callbackURL: callbackURL}
}

func (payment *Payment) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		WriteJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /pay", payment.pay)
	mux.HandleFunc("GET /status", payment.status)
	mux.HandleFunc("POST /admin/config", payment.configure)
	return mux
}

func (payment *Payment) pay(writer http.ResponseWriter, request *http.Request) {
	var input PayRequest
	if err := DecodeJSON(request, &input); err != nil || !ValidToken(input.BusinessID) {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid payment"})
		return
	}
	payment.mu.Lock()
	payment.requests++
	config := payment.config
	payment.mu.Unlock()

	for attempt := 1; attempt <= config.Retries; attempt++ {
		attemptKey := input.BusinessID
		if !config.StableKeys {
			attemptKey = fmt.Sprintf("%s-attempt-%d", input.BusinessID, attempt)
		}
		ctx, cancel := context.WithTimeout(request.Context(),
			time.Duration(config.TimeoutMillis)*time.Millisecond)
		var result ChargeResponse
		err := PostJSON(ctx, DefaultClient(0), payment.callbackURL+"/callback",
			ChargeRequest{BusinessID: input.BusinessID, AttemptKey: attemptKey}, &result)
		cancel()
		payment.mu.Lock()
		payment.attempts++
		payment.mu.Unlock()
		if err == nil {
			payment.mu.Lock()
			payment.succeeded++
			payment.mu.Unlock()
			WriteJSON(writer, http.StatusOK, PayResponse{Paid: true, Attempts: attempt,
				CaptureID: result.Sequence})
			return
		}
	}
	payment.mu.Lock()
	payment.failed++
	payment.mu.Unlock()
	WriteJSON(writer, http.StatusBadGateway, map[string]bool{"paid": false})
}

func (payment *Payment) status(writer http.ResponseWriter, _ *http.Request) {
	payment.mu.Lock()
	status := payment.statusLocked()
	payment.mu.Unlock()
	WriteJSON(writer, http.StatusOK, status)
}

func (payment *Payment) configure(writer http.ResponseWriter, request *http.Request) {
	var config PaymentConfig
	if err := DecodeJSON(request, &config); err != nil || !validPaymentConfig(config) {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid payment config"})
		return
	}
	payment.mu.Lock()
	payment.config = config
	status := payment.statusLocked()
	payment.mu.Unlock()
	WriteJSON(writer, http.StatusOK, status)
}

func (payment *Payment) statusLocked() PaymentStatus {
	return PaymentStatus{Config: payment.config, Requests: payment.requests,
		Succeeded: payment.succeeded, Failed: payment.failed, Attempts: payment.attempts}
}

func validPaymentConfig(config PaymentConfig) bool {
	return config.TimeoutMillis >= 50 && config.TimeoutMillis <= 5000 &&
		config.Retries >= 1 && config.Retries <= 4
}
