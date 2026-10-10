package main

type syntheticProbeAudit struct {
	Observed  int          `json:"observed"`
	Succeeded int          `json:"succeeded"`
	Failed    int          `json:"failed"`
	Ledger    ledgerStatus `json:"ledger"`
}
