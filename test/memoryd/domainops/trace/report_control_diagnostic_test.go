package main

import "testing"

func TestValidateReportAcceptsContainedPostAcceptDenials(t *testing.T) {
	report := validReport()
	report.Turns[0].BashCalls = 14
	report.Turns[0].SubmitAttempts = 14
	report.Turns[0].IntentSubmits = 1
	acceptTestTurn(&report.Turns[0], "report-post-accept")
	report.Turns[0].SubmitDenials = 13
	report.Turns[0].SubmitControlDenials = []controlDenial{{Code: "context_required", Count: 13}}
	report.Turns[0].PostAcceptDenials = 13
	if err := validateReport(report); err != nil {
		t.Fatalf("validateReport() rejected contained post-accept denials: %v", err)
	}
	report.Turns[0].BashCalls = 257
	report.Turns[0].SubmitAttempts = 257
	report.Turns[0].SubmitDenials = 256
	report.Turns[0].SubmitControlDenials[0].Count = 256
	report.Turns[0].PostAcceptDenials = 256
	if err := validateReport(report); err == nil {
		t.Fatal("validateReport() accepted counters above the generic turn bound")
	}
}

func TestValidateFailureReportAccountsClosedSubmitDenials(t *testing.T) {
	report := validFailureReport()
	report.Turns[0].BashCalls = 1
	report.Turns[0].SubmitAttempts = 3
	report.Turns[0].IntentSubmits = 1
	report.Turns[0].RejectedReceipts = 1
	report.Turns[0].SubmitDenials = 2
	report.Turns[0].SubmitControlDenials = []controlDenial{{Code: "context_required", Count: 2}}
	if err := validateFailureReport(report); err != nil {
		t.Fatalf("validateFailureReport() rejected accounted attempts: %v", err)
	}
	report.Turns[0].SubmitDenials = 0
	report.Turns[0].SubmitControlDenials = nil
	if err := validateFailureReport(report); err == nil {
		t.Fatal("validateFailureReport() accepted an unaccounted submit attempt")
	}
}

func TestValidateFailureReportAcceptsContainedPostAcceptDenials(t *testing.T) {
	report := validFailureReport()
	report.Turns[0].BashCalls = 14
	report.Turns[0].SubmitAttempts = 14
	report.Turns[0].IntentSubmits = 1
	acceptTestTurn(&report.Turns[0], "failure-post-accept")
	report.Turns[0].SubmitDenials = 13
	report.Turns[0].SubmitControlDenials = []controlDenial{{Code: "context_required", Count: 13}}
	report.Turns[0].PostAcceptDenials = 13
	if err := validateFailureReport(report); err != nil {
		t.Fatalf("validateFailureReport() rejected contained post-accept denials: %v", err)
	}
}
