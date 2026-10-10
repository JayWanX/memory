package main

import (
	"strings"
	"testing"
)

func TestValidateReportRejectsBrokenServiceReceiptCorrespondence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*liveReport)
	}{
		{"wrong retained capture", func(report *liveReport) {
			report.World.Episodes[0].Recovery.Receipts[0].CaptureID++
		}},
		{"hidden extra charge", func(report *liveReport) {
			report.World.Episodes[0].StabilityCharges.Result = append(
				report.World.Episodes[0].StabilityCharges.Result,
				report.World.Episodes[0].StabilityCharges.Result[0])
		}},
		{"duplicate sequence", func(report *liveReport) {
			report.World.Episodes[0].RecoveryCharges.Result[1].Sequence =
				report.World.Episodes[0].RecoveryCharges.Result[0].Sequence
		}},
		{"missing void reason", func(report *liveReport) {
			for index := range report.World.Episodes[0].IncidentCharges.Result {
				if report.World.Episodes[0].IncidentCharges.Result[index].State == "voided" {
					report.World.Episodes[0].IncidentCharges.Result[index].VoidReason = ""
					return
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := validReport()
			test.mutate(&report)
			if err := validateReport(report); err == nil {
				t.Fatal("validateReport() accepted broken service-receipt evidence")
			}
		})
	}
}

func TestValidateReportBindsTwoDistinctRegionalEpisodes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*liveReport)
	}{
		{"same incident route", func(report *liveReport) {
			report.World.Episodes[1].Baseline.Observed.Gateway.Route = "east"
		}},
		{"reused load identity", func(report *liveReport) {
			first := report.World.Episodes[0].Baseline.Prefix
			second := report.World.Episodes[1].Baseline.Prefix
			report.World.Episodes[1].Baseline.Prefix = first
			for index := range report.World.Episodes[1].Baseline.Receipts {
				report.World.Episodes[1].Baseline.Receipts[index].BusinessID = strings.Replace(
					report.World.Episodes[1].Baseline.Receipts[index].BusinessID, second, first, 1)
			}
			for index := range report.World.Episodes[1].IncidentCharges.Result {
				report.World.Episodes[1].IncidentCharges.Result[index].BusinessID = strings.Replace(
					report.World.Episodes[1].IncidentCharges.Result[index].BusinessID, second, first, 1)
			}
		}},
		{"receipt outside load identity", func(report *liveReport) {
			report.World.Episodes[1].Recovery.Receipts[0].BusinessID = "different-1"
		}},
		{"reused global ledger sequence", func(report *liveReport) {
			report.World.Episodes[1].IncidentCharges.Result[0].Sequence =
				report.World.Episodes[0].IncidentCharges.Result[0].Sequence
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := validReport()
			test.mutate(&report)
			if err := validateReport(report); err == nil {
				t.Fatal("validateReport() accepted a non-independent regional episode")
			}
		})
	}
}

func TestValidateReportRejectsNonQuiescentDeliveryBarrier(t *testing.T) {
	report := validReport()
	report.Protocol.DeliveryQuiescence[0].PendingDeliveryRecords = 1
	report.Protocol.DeliveryQuiescence[0].Nodes[0].PendingOutbox = 1
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted a non-quiescent delivery barrier")
	}
}

func TestValidateReportTreatsEvolutionAsAnOptionalExactObservation(t *testing.T) {
	report := validReport()
	clearEvolutionObservation(&report)
	if err := validateReport(report); err != nil {
		t.Fatalf("validateReport() rejected an episode with no evolution claim: %v", err)
	}
	report.Protocol.Evolution.Demonstrated = true
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted an unproved evolution claim")
	}

	report = validReport()
	report.Protocol.Evolution.Effects[2].Matches[0].ReferenceDigest =
		"sha256:" + string(make([]byte, 64))
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted an invalid Reference digest")
	}

	report = validReport()
	report.Protocol.Evolution.Boundary.Nodes[2].ConsolidationAfterSequence = 3
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted a regressed consolidation boundary")
	}
}

func clearEvolutionObservation(report *liveReport) {
	report.Protocol.Evolution.Boundary.ActiveHeadCount = 0
	for index := range report.Protocol.Evolution.Boundary.Nodes {
		report.Protocol.Evolution.Boundary.Nodes[index].ActiveHeads = nil
	}
	for index := range report.Protocol.Evolution.Effects {
		report.Protocol.Evolution.Effects[index].ActiveHeadCount = 0
		report.Protocol.Evolution.Effects[index].AcceptedReferenceUses = 0
		report.Protocol.Evolution.Effects[index].Matches = nil
	}
	report.Protocol.Evolution.AcceptedReferenceUses = 0
	report.Protocol.Evolution.Demonstrated = false
}
