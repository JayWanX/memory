package world

import (
	"net/http"
	"sort"
	"strings"
	"sync"
)

const (
	ChargeActive = "active"
	ChargeVoided = "voided"
)

type Charge struct {
	Sequence   int64  `json:"sequence"`
	BusinessID string `json:"business_id"`
	AttemptKey string `json:"attempt_key"`
	State      string `json:"state"`
	VoidReason string `json:"void_reason,omitempty"`
}

type LedgerStatus struct {
	Charges             int `json:"charges"`
	ActiveCharges       int `json:"active_charges"`
	VoidedCharges       int `json:"voided_charges"`
	UniqueBusinesses    int `json:"unique_businesses"`
	DuplicateBusinesses int `json:"duplicate_businesses"`
}

type Ledger struct {
	mu        sync.Mutex
	byAttempt map[string]Charge
	next      int64
}

func NewLedger() *Ledger { return &Ledger{byAttempt: make(map[string]Charge)} }

func (ledger *Ledger) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		WriteJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /charge", ledger.charge)
	mux.HandleFunc("GET /status", ledger.status)
	mux.HandleFunc("GET /charges", ledger.charges)
	mux.HandleFunc("POST /admin/void", ledger.void)
	return mux
}

func (ledger *Ledger) charge(writer http.ResponseWriter, request *http.Request) {
	var input ChargeRequest
	if err := DecodeJSON(request, &input); err != nil || !ValidToken(input.BusinessID) ||
		!ValidToken(input.AttemptKey) {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid charge"})
		return
	}
	ledger.mu.Lock()
	charge, exists := ledger.byAttempt[input.AttemptKey]
	if !exists {
		ledger.next++
		charge = Charge{Sequence: ledger.next, BusinessID: input.BusinessID,
			AttemptKey: input.AttemptKey, State: ChargeActive}
		ledger.byAttempt[input.AttemptKey] = charge
	}
	ledger.mu.Unlock()
	WriteJSON(writer, http.StatusOK, ChargeResponse{Accepted: true, Replayed: exists,
		Sequence: charge.Sequence})
}

func (ledger *Ledger) status(writer http.ResponseWriter, request *http.Request) {
	prefix := request.URL.Query().Get("prefix")
	ledger.mu.Lock()
	status := ledger.statusLocked(prefix)
	ledger.mu.Unlock()
	WriteJSON(writer, http.StatusOK, status)
}

func (ledger *Ledger) charges(writer http.ResponseWriter, request *http.Request) {
	prefix := request.URL.Query().Get("prefix")
	ledger.mu.Lock()
	charges := ledger.chargesLocked(prefix)
	ledger.mu.Unlock()
	WriteJSON(writer, http.StatusOK, charges)
}

func (ledger *Ledger) void(writer http.ResponseWriter, request *http.Request) {
	var input VoidRequest
	if err := DecodeJSON(request, &input); err != nil || input.Sequence <= 0 ||
		len(input.Reason) < 3 || len(input.Reason) > 160 {
		WriteJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid void request"})
		return
	}
	ledger.mu.Lock()
	charge, found := ledger.voidLocked(input)
	ledger.mu.Unlock()
	if !found {
		WriteJSON(writer, http.StatusNotFound, map[string]string{"error": "active charge not found"})
		return
	}
	WriteJSON(writer, http.StatusOK, charge)
}

func (ledger *Ledger) statusLocked(prefix string) LedgerStatus {
	counts := make(map[string]int)
	status := LedgerStatus{}
	for _, charge := range ledger.byAttempt {
		if !strings.HasPrefix(charge.BusinessID, prefix) {
			continue
		}
		status.Charges++
		if charge.State == ChargeActive {
			counts[charge.BusinessID]++
			status.ActiveCharges++
		} else {
			status.VoidedCharges++
		}
	}
	status.UniqueBusinesses = len(counts)
	for _, count := range counts {
		if count > 1 {
			status.DuplicateBusinesses++
		}
	}
	return status
}

func (ledger *Ledger) chargesLocked(prefix string) []Charge {
	charges := make([]Charge, 0, len(ledger.byAttempt))
	for _, charge := range ledger.byAttempt {
		if strings.HasPrefix(charge.BusinessID, prefix) {
			charges = append(charges, charge)
		}
	}
	sort.Slice(charges, func(i, j int) bool { return charges[i].Sequence < charges[j].Sequence })
	return charges
}

func (ledger *Ledger) voidLocked(input VoidRequest) (Charge, bool) {
	for attemptKey, charge := range ledger.byAttempt {
		if charge.Sequence != input.Sequence || charge.State != ChargeActive {
			continue
		}
		charge.State = ChargeVoided
		charge.VoidReason = input.Reason
		ledger.byAttempt[attemptKey] = charge
		return charge, true
	}
	return Charge{}, false
}
