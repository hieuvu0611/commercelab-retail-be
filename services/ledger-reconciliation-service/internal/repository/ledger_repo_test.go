package repository

import (
	"context"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
)

func TestLedgerRepo_LifecycleAndAccountBalances(t *testing.T) {
	ctx := context.Background()
	repo := NewLedgerRepository(nil)

	// Step 1: Record Order Payment Captured (660,000 VND)
	payEntry, err := domain.NewPaymentCapturedEntry("JE-PAY-001", "ORD-660K", 660_000, "staff-checkout")
	if err != nil {
		t.Fatalf("failed to create payment entry: %v", err)
	}

	recorded, err := repo.RecordJournalEntry(ctx, payEntry)
	if err != nil {
		t.Fatalf("failed to record payment journal entry: %v", err)
	}
	if recorded.EntryID != "JE-PAY-001" {
		t.Fatalf("expected JE-PAY-001, got %s", recorded.EntryID)
	}

	// Verify Account 1120 (Gateway Receivable) is +660,000 VND
	acc1120, err := repo.GetAccount(ctx, "1120")
	if err != nil {
		t.Fatalf("failed to get account 1120: %v", err)
	}
	if acc1120.BalanceVND != 660_000 {
		t.Fatalf("expected 1120 balance 660,000, got %d", acc1120.BalanceVND)
	}

	// Verify Account 5110 (Revenue) is +660,000 VND
	acc5110, err := repo.GetAccount(ctx, "5110")
	if err != nil {
		t.Fatalf("failed to get account 5110: %v", err)
	}
	if acc5110.BalanceVND != 660_000 {
		t.Fatalf("expected 5110 balance 660,000, got %d", acc5110.BalanceVND)
	}

	// Step 2: Record Settlement with SC11 Discrepancy (Gross 660k, Fee 6.6k, Bank 650k, Discrepancy 3.4k)
	settleEntry, err := domain.NewSettlementMatchedEntry(
		"JE-SETTLE-SC11", "ORD-660K", "BATCH-001",
		660_000, 6_600, 650_000, 3_400, "finance-mgr",
	)
	if err != nil {
		t.Fatalf("failed to create settlement entry: %v", err)
	}

	_, err = repo.RecordJournalEntry(ctx, settleEntry)
	if err != nil {
		t.Fatalf("failed to record settlement entry: %v", err)
	}

	// Verify Account 1120 (Gateway Receivable) cleared to 0 (660,000 - 660,000 = 0)
	acc1120After, _ := repo.GetAccount(ctx, "1120")
	if acc1120After.BalanceVND != 0 {
		t.Fatalf("expected 1120 balance cleared to 0, got %d", acc1120After.BalanceVND)
	}

	// Verify Bank 1110 received 650,000 VND
	acc1110, _ := repo.GetAccount(ctx, "1110")
	if acc1110.BalanceVND != 650_000 {
		t.Fatalf("expected 1110 bank balance 650,000, got %d", acc1110.BalanceVND)
	}

	// Verify Processing Fee 6410 is 6,600 VND
	acc6410, _ := repo.GetAccount(ctx, "6410")
	if acc6410.BalanceVND != 6_600 {
		t.Fatalf("expected 6410 fee balance 6,600, got %d", acc6410.BalanceVND)
	}

	// Verify Discrepancy Receivable 1388 is 3,400 VND
	acc1388, _ := repo.GetAccount(ctx, "1388")
	if acc1388.BalanceVND != 3_400 {
		t.Fatalf("expected 1388 discrepancy balance 3,400, got %d", acc1388.BalanceVND)
	}

	// Step 3: List entries and filter by reference
	list, total, err := repo.ListJournalEntries(ctx, "ORD-660K", 10, 0)
	if err != nil {
		t.Fatalf("failed to list entries: %v", err)
	}
	if total != 1 { // ReferenceID "ORD-660K" is only for JE-PAY-001
		t.Fatalf("expected 1 entry for reference ORD-660K, got %d", total)
	}
	if len(list) != 1 || list[0].EntryID != "JE-PAY-001" {
		t.Fatalf("unexpected entry: %+v", list)
	}
}
