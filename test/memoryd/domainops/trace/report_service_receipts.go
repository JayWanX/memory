package main

import (
	"errors"
	"strings"
)

// validateServiceReceiptEvidence binds every customer-visible capture ID to
// the exact retained ledger record. Aggregate counts cannot prove this link.
func validateServiceReceiptEvidence(summary loadSummary, charges domainChargeResult,
	copiesPerBusiness, voidedPerBusiness int,
) error {
	if charges.Role != "data" || copiesPerBusiness < 1 || voidedPerBusiness < 0 ||
		voidedPerBusiness >= copiesPerBusiness ||
		len(charges.Result) != len(summary.Receipts)*copiesPerBusiness {
		return errors.New("sanitized live report has incomplete exact service-receipt evidence")
	}
	receipts, err := indexServiceReceipts(summary.Prefix, summary.Receipts)
	if err != nil {
		return err
	}
	counts, err := countChargeEvidence(receipts, charges.Result)
	if err != nil {
		return err
	}
	for businessID := range receipts {
		if counts.copies[businessID] != copiesPerBusiness ||
			counts.voided[businessID] != voidedPerBusiness ||
			counts.activeReceipt[businessID] != 1 {
			return errors.New("sanitized live report does not bind each receipt to one retained capture")
		}
	}
	return nil
}

func indexServiceReceipts(prefix string, values []serviceReceipt) (map[string]int64, error) {
	if prefix == "" {
		return nil, errors.New("sanitized live report has an empty load identity")
	}
	receipts := make(map[string]int64, len(values))
	for _, receipt := range values {
		if !strings.HasPrefix(receipt.BusinessID, prefix+"-") || receipt.CaptureID <= 0 {
			return nil, errors.New("sanitized live report has an invalid customer receipt")
		}
		if _, duplicate := receipts[receipt.BusinessID]; duplicate {
			return nil, errors.New("sanitized live report repeats a customer receipt")
		}
		receipts[receipt.BusinessID] = receipt.CaptureID
	}
	return receipts, nil
}

type chargeEvidenceCounts struct {
	copies        map[string]int
	voided        map[string]int
	activeReceipt map[string]int
}

func countChargeEvidence(receipts map[string]int64, charges []chargeRecord) (
	chargeEvidenceCounts, error,
) {
	seenSequences := make(map[int64]struct{}, len(charges))
	seenAttempts := make(map[string]struct{}, len(charges))
	counts := chargeEvidenceCounts{copies: make(map[string]int, len(receipts)),
		voided:        make(map[string]int, len(receipts)),
		activeReceipt: make(map[string]int, len(receipts))}
	for _, charge := range charges {
		captureID, expected := receipts[charge.BusinessID]
		if !expected || charge.Sequence <= 0 || charge.AttemptKey == "" ||
			(charge.State != "active" && charge.State != "voided") {
			return chargeEvidenceCounts{}, errors.New("sanitized live report has an invalid exact charge record")
		}
		if _, duplicate := seenSequences[charge.Sequence]; duplicate {
			return chargeEvidenceCounts{}, errors.New("sanitized live report repeats a charge sequence")
		}
		if _, duplicate := seenAttempts[charge.AttemptKey]; duplicate {
			return chargeEvidenceCounts{}, errors.New("sanitized live report repeats an attempt identity")
		}
		seenSequences[charge.Sequence] = struct{}{}
		seenAttempts[charge.AttemptKey] = struct{}{}
		counts.copies[charge.BusinessID]++
		if charge.State == "voided" {
			if charge.VoidReason == "" {
				return chargeEvidenceCounts{}, errors.New("sanitized live report omits a void audit reason")
			}
			counts.voided[charge.BusinessID]++
		} else if charge.Sequence == captureID {
			counts.activeReceipt[charge.BusinessID]++
		}
	}
	return counts, nil
}
