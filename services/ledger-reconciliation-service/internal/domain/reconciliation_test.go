package domain

import (
	"testing"
	"time"
)

func TestEvaluateSettlementLine_SC11(t *testing.T) {
	// SC11 Scenario:
	// Order collection: 660,000 VND
	// Fee 1%: 6,600 VND
	// Expected payout: 653,400 VND
	// Actual settlement received: 650,000 VND
	// Shortfall discrepancy: 3,400 VND
	line := SettlementLine{
		LineNumber:     1,
		TransactionRef: "TX_SC11_001",
		OrderID:        "ORD_SC11_660K",
		GrossAmountVND: 660000,
		FeeAmountVND:   10000, // Partner deducted 10,000 fee instead of agreed 6,600
		NetAmountVND:   650000,
	}

	result := EvaluateSettlementLine(line, 1)

	if result.CalculatedFeeVND != 6600 {
		t.Fatalf("expected 1%% fee to be 6600, got %d", result.CalculatedFeeVND)
	}
	if result.ExpectedPayoutVND != 653400 {
		t.Fatalf("expected payout to be 653400, got %d", result.ExpectedPayoutVND)
	}
	if result.ActualPayoutVND != 650000 {
		t.Fatalf("expected actual payout to be 650000, got %d", result.ActualPayoutVND)
	}
	if result.DiscrepancyVND != 3400 {
		t.Fatalf("expected discrepancy to be 3400, got %d", result.DiscrepancyVND)
	}
	if result.Status != "DISCREPANCY_OPEN" {
		t.Fatalf("expected status to be DISCREPANCY_OPEN, got %s", result.Status)
	}
}

func TestEvaluateSettlementLine_Matched(t *testing.T) {
	// Normal matched transaction:
	// Gross: 1,000,000 VND
	// Fee 1%: 10,000 VND
	// Net: 990,000 VND
	line := SettlementLine{
		LineNumber:     2,
		TransactionRef: "TX_OK_002",
		OrderID:        "ORD_OK_1M",
		GrossAmountVND: 1000000,
		FeeAmountVND:   10000,
		NetAmountVND:   990000,
	}

	result := EvaluateSettlementLine(line, 1)

	if result.CalculatedFeeVND != 10000 {
		t.Fatalf("expected fee 10000, got %d", result.CalculatedFeeVND)
	}
	if result.DiscrepancyVND != 0 {
		t.Fatalf("expected discrepancy 0, got %d", result.DiscrepancyVND)
	}
	if result.Status != "MATCHED" {
		t.Fatalf("expected status MATCHED, got %s", result.Status)
	}
}

func TestBelongsToCutoffPeriod(t *testing.T) {
	// Cutoff is 23:59:59 Asia/Ho_Chi_Minh (UTC+7)
	loc := CutoffLocation

	// Target date: 2026-11-16
	targetDate := time.Date(2026, 11, 16, 10, 0, 0, 0, loc)

	// In same day 15:30:00 (Asia/Ho_Chi_Minh) -> within period
	tx1 := time.Date(2026, 11, 16, 15, 30, 0, 0, loc)
	if !BelongsToCutoffPeriod(tx1, targetDate) {
		t.Errorf("expected tx1 %v to belong to period", tx1)
	}

	// At 23:59:59 (Asia/Ho_Chi_Minh) -> within period
	tx2 := time.Date(2026, 11, 16, 23, 59, 59, 0, loc)
	if !BelongsToCutoffPeriod(tx2, targetDate) {
		t.Errorf("expected tx2 %v at cutoff to belong to period", tx2)
	}

	// At 00:00:01 next day (Asia/Ho_Chi_Minh) -> outside period
	tx3 := time.Date(2026, 11, 17, 0, 0, 1, 0, loc)
	if BelongsToCutoffPeriod(tx3, targetDate) {
		t.Errorf("expected tx3 %v next day to be outside period", tx3)
	}
}
