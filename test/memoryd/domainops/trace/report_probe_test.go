package main

import "testing"

func TestValidateReportAcceptsAuditedSyntheticProbe(t *testing.T) {
	report := validReport()
	audit := syntheticProbeAudit{Observed: 2, Succeeded: 1, Failed: 1,
		Ledger: ledgerStatus{Charges: 2, ActiveCharges: 1, VoidedCharges: 1,
			UniqueBusinesses: 2}}
	report.World.Episodes[0].SyntheticProbes = audit
	report.World.Episodes[1].SyntheticProbes = audit
	if err := validateReport(report); err != nil {
		t.Fatalf("validateReport() rejected audited probes: %v", err)
	}
}

func TestValidateReportRejectsSyntheticProbeIntegrityDrift(t *testing.T) {
	report := validReport()
	report.World.Episodes[0].SyntheticProbes = syntheticProbeAudit{
		Observed: 1, Succeeded: 1,
		Ledger: ledgerStatus{Charges: 2, ActiveCharges: 2,
			UniqueBusinesses: 1, DuplicateBusinesses: 1},
	}
	report.World.Episodes[1].SyntheticProbes = report.World.Episodes[0].SyntheticProbes
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted duplicate active synthetic captures")
	}
}

func TestValidateReportRejectsSyntheticProbeHistoryRegression(t *testing.T) {
	report := validReport()
	report.World.Episodes[0].SyntheticProbes = syntheticProbeAudit{Observed: 1,
		Succeeded: 1, Ledger: ledgerStatus{Charges: 1, ActiveCharges: 1,
			UniqueBusinesses: 1}}
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted a regressed cumulative probe audit")
	}
}

func TestValidateReportRejectsSyntheticProbeCountAboveGlobalBound(t *testing.T) {
	report := validReport()
	report.World.Episodes[0].SyntheticProbes = syntheticProbeAudit{
		Observed: maxSyntheticProbes + 1,
		Failed:   maxSyntheticProbes + 1,
		Ledger:   ledgerStatus{UniqueBusinesses: maxSyntheticProbes + 1},
	}
	report.World.Episodes[1].SyntheticProbes = report.World.Episodes[0].SyntheticProbes
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted a synthetic-probe count above the global bound")
	}
}
