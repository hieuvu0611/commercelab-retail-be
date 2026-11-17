package domain

import (
	"fmt"
	"time"
)

var (
	// CutoffLocation is Asia/Ho_Chi_Minh (UTC+7)
	CutoffLocation *time.Location
)

func init() {
	loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		loc = time.FixedZone("Asia/Ho_Chi_Minh", 7*3600)
	}
	CutoffLocation = loc
}

// CutoffForDate returns the start of day (00:00:00) and end of day (23:59:59.999999999) in Asia/Ho_Chi_Minh
func CutoffForDate(t time.Time) (time.Time, time.Time) {
	inLoc := t.In(CutoffLocation)
	start := time.Date(inLoc.Year(), inLoc.Month(), inLoc.Day(), 0, 0, 0, 0, CutoffLocation)
	end := time.Date(inLoc.Year(), inLoc.Month(), inLoc.Day(), 23, 59, 59, 999999999, CutoffLocation)
	return start, end
}

// BelongsToCutoffPeriod checks if a transaction time falls within the period cutoff for the target date
func BelongsToCutoffPeriod(txTime time.Time, periodDate time.Time) bool {
	start, end := CutoffForDate(periodDate)
	return (txTime.Equal(start) || txTime.After(start)) && (txTime.Equal(end) || txTime.Before(end))
}

// MatchResult represents the evaluation result for a single settlement line
type MatchResult struct {
	LineNumber        int    `json:"lineNumber"`
	TransactionRef    string `json:"transactionRef"`
	OrderID           string `json:"orderId"`
	GrossAmountVND    int64  `json:"grossAmountVnd"`
	CalculatedFeeVND  int64  `json:"calculatedFeeVnd"`
	ExpectedPayoutVND int64  `json:"expectedPayoutVnd"`
	ActualPayoutVND   int64  `json:"actualPayoutVnd"`
	DiscrepancyVND    int64  `json:"discrepancyVnd"`
	Status            string `json:"status"` // "MATCHED" | "DISCREPANCY_OPEN"
	Message           string `json:"message"`
}

// BatchMatchSummary summarizes the reconciliation outcome of a settlement batch
type BatchMatchSummary struct {
	BatchID                string        `json:"batchId"`
	TotalLines             int           `json:"totalLines"`
	MatchedLines           int           `json:"matchedLines"`
	DiscrepancyLines       int           `json:"discrepancyLines"`
	TotalGrossVND          int64         `json:"totalGrossVnd"`
	TotalFeeVND            int64         `json:"totalFeeVnd"`
	TotalNetVND            int64         `json:"totalNetVnd"`
	TotalDiscrepancyVND    int64         `json:"totalDiscrepancyVnd"`
	JournalEntriesCreated  int           `json:"journalEntriesCreated"`
	BatchStatus            string        `json:"batchStatus"`
	Results                []MatchResult `json:"results"`
}

// ReconciliationOverview provides aggregate metrics for backoffice finance
type ReconciliationOverview struct {
	TotalBatches           int   `json:"totalBatches"`
	ReconciledBatches      int   `json:"reconciledBatches"`
	PendingBatches         int   `json:"pendingBatches"`
	TotalLinesProcessed    int   `json:"totalLinesProcessed"`
	MatchedLines           int   `json:"matchedLines"`
	OpenDiscrepancies      int   `json:"openDiscrepancies"`
	TotalDiscrepancyVND    int64 `json:"totalDiscrepancyVnd"`
}

// EvaluateSettlementLine matches a settlement line against expected payout with standard 1% gateway fee
// SC11 Scenario:
// Gross: 660,000 VND
// Fee 1%: 6,600 VND
// Expected Net: 653,400 VND
// Actual Net: 650,000 VND -> Discrepancy: 3,400 VND shortfall
func EvaluateSettlementLine(line SettlementLine, feePct int64) MatchResult {
	if feePct <= 0 {
		feePct = 1 // default 1% simulated partner fee
	}

	fee := (line.GrossAmountVND * feePct) / 100
	expected := line.GrossAmountVND - fee
	diff := expected - line.NetAmountVND

	status := "MATCHED"
	msg := fmt.Sprintf("Line #%d matched perfectly with 1%% fee (%d VND)", line.LineNumber, fee)
	if diff != 0 {
		status = "DISCREPANCY_OPEN"
		msg = fmt.Sprintf("Line #%d discrepancy detected: expected payout %d VND, partner paid %d VND (shortfall %d VND)",
			line.LineNumber, expected, line.NetAmountVND, diff)
	}

	return MatchResult{
		LineNumber:        line.LineNumber,
		TransactionRef:    line.TransactionRef,
		OrderID:           line.OrderID,
		GrossAmountVND:    line.GrossAmountVND,
		CalculatedFeeVND:  fee,
		ExpectedPayoutVND: expected,
		ActualPayoutVND:   line.NetAmountVND,
		DiscrepancyVND:    diff,
		Status:            status,
		Message:           msg,
	}
}

// ReconciliationResult is kept for backward compatibility with previous helper tests
type ReconciliationResult struct {
	OrderID             string `json:"orderId"`
	VerifiedCapturedVND int64  `json:"verifiedCapturedVnd"`
	PartnerFeeVND       int64  `json:"partnerFeeVnd"`
	ExpectedPayoutVND   int64  `json:"expectedPayoutVnd"`
	ActualPayoutVND     int64  `json:"actualPayoutVnd"`
	DiscrepancyVND      int64  `json:"discrepancyVnd"`
	Status              string `json:"status"`
	S3StatementKey      string `json:"s3StatementKey"`
}

func EvaluateSettlement(orderID string, verifiedCapturedVND int64, feePct int64, actualPayoutVND int64, s3Key string) ReconciliationResult {
	if feePct <= 0 {
		feePct = 1
	}
	fee := (verifiedCapturedVND * feePct) / 100
	expected := verifiedCapturedVND - fee
	diff := expected - actualPayoutVND
	status := "MATCHED"
	if diff != 0 {
		status = "DISCREPANCY_OPEN"
	}
	return ReconciliationResult{
		OrderID:             orderID,
		VerifiedCapturedVND: verifiedCapturedVND,
		PartnerFeeVND:       fee,
		ExpectedPayoutVND:   expected,
		ActualPayoutVND:     actualPayoutVND,
		DiscrepancyVND:      diff,
		Status:              status,
		S3StatementKey:      s3Key,
	}
}
