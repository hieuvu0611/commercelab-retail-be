package consumer

import (
	"context"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

func TestPOSHandler_CashSalePostsBalancedEntryTo1111And5110(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewLedgerRepository(nil)
	handler := &POSHandler{Repo: repo}

	ev := events.DomainEvent{
		EventID:       "evt-pos-cash-1",
		EventType:     events.EventPOSSaleCompleted,
		AggregateType: "Order",
		AggregateID:   "ord-pos-cash01",
		OccurredAt:    time.Now().UTC(),
		Payload: map[string]any{
			"orderId":       "ord-pos-cash01",
			"receiptNumber": "POS-HCM-CASH01",
			"warehouseId":   "WH-HCM-01",
			"cashierId":     "usr-staff-hcm",
			"paymentMethod": "POS_CASH",
			"totalVnd":      float64(600_000),
		},
	}

	if err := handler.Handle(ctx, ev); err != nil {
		t.Fatalf("Handle POS_CASH failed: %v", err)
	}

	entry, err := repo.GetJournalEntry(ctx, "JE-POS-ord-pos-cash01")
	if err != nil {
		t.Fatalf("GetJournalEntry failed: %v", err)
	}
	if err := domain.ValidateJournalEntry(entry); err != nil {
		t.Fatalf("entry must be balanced: %v", err)
	}
	var debitSum, creditSum int64
	for _, p := range entry.Postings {
		switch p.Direction {
		case domain.DirectionDebit:
			debitSum += p.AmountVND
			if p.AccountCode != "1111" {
				t.Fatalf("expected debit account 1111, got %s", p.AccountCode)
			}
		case domain.DirectionCredit:
			creditSum += p.AmountVND
			if p.AccountCode != "5110" {
				t.Fatalf("expected credit account 5110, got %s", p.AccountCode)
			}
		}
	}
	if debitSum != 600_000 || creditSum != 600_000 {
		t.Fatalf("expected balanced debit=600000 credit=600000, got debit=%d credit=%d", debitSum, creditSum)
	}

	acc1111, err := repo.GetAccount(ctx, "1111")
	if err != nil || acc1111.BalanceVND != 600_000 {
		t.Fatalf("expected account 1111 balance 600000, got %+v err=%v", acc1111, err)
	}
	acc5110, err := repo.GetAccount(ctx, "5110")
	if err != nil || acc5110.BalanceVND != 600_000 {
		t.Fatalf("expected account 5110 balance 600000, got %+v err=%v", acc5110, err)
	}
}

func TestPOSHandler_CardSalePostsBalancedEntryTo1120And5110(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewLedgerRepository(nil)
	handler := &POSHandler{Repo: repo}

	ev := events.DomainEvent{
		EventID:       "evt-pos-card-1",
		EventType:     events.EventPOSSaleCompleted,
		AggregateType: "Order",
		AggregateID:   "ord-pos-card01",
		OccurredAt:    time.Now().UTC(),
		Payload: map[string]any{
			"orderId":       "ord-pos-card01",
			"receiptNumber": "POS-HN-CARD01",
			"warehouseId":   "WH-HN-01",
			"cashierId":     "usr-staff-hn",
			"paymentMethod": "POS_CARD",
			"totalVnd":      int64(450_000),
		},
	}

	if err := handler.Handle(ctx, ev); err != nil {
		t.Fatalf("Handle POS_CARD failed: %v", err)
	}

	entry, err := repo.GetJournalEntry(ctx, "JE-POS-ord-pos-card01")
	if err != nil {
		t.Fatalf("GetJournalEntry failed: %v", err)
	}
	if err := domain.ValidateJournalEntry(entry); err != nil {
		t.Fatalf("entry must be balanced: %v", err)
	}
	if len(entry.Postings) != 2 || entry.Postings[0].AccountCode != "1120" || entry.Postings[0].Direction != domain.DirectionDebit ||
		entry.Postings[1].AccountCode != "5110" || entry.Postings[1].Direction != domain.DirectionCredit {
		t.Fatalf("unexpected postings: %+v", entry.Postings)
	}

	acc1120, err := repo.GetAccount(ctx, "1120")
	if err != nil || acc1120.BalanceVND != 450_000 {
		t.Fatalf("expected account 1120 balance 450000, got %+v err=%v", acc1120, err)
	}
	acc5110, err := repo.GetAccount(ctx, "5110")
	if err != nil || acc5110.BalanceVND != 450_000 {
		t.Fatalf("expected account 5110 balance 450000, got %+v err=%v", acc5110, err)
	}
}

func TestPOSHandler_IdempotentDuplicateDeliveryAndIgnoresOtherEvents(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewLedgerRepository(nil)
	handler := &POSHandler{Repo: repo}

	other := events.DomainEvent{
		EventID:   "evt-other-1",
		EventType: events.EventOrderCreated,
		Payload:   map[string]any{"orderId": "O-1"},
	}
	if err := handler.Handle(ctx, other); err != nil {
		t.Fatalf("Handle non-POS event failed: %v", err)
	}

	ev := events.DomainEvent{
		EventID:       "evt-pos-dup-1",
		EventType:     events.EventPOSSaleCompleted,
		AggregateType: "Order",
		AggregateID:   "ord-pos-dup01",
		OccurredAt:    time.Now().UTC(),
		Payload: map[string]any{
			"orderId":       "ord-pos-dup01",
			"warehouseId":   "WH-HCM-01",
			"cashierId":     "usr-staff-hcm",
			"paymentMethod": "POS_CASH",
			"totalVnd":      int64(300_000),
		},
	}

	for i := 0; i < 3; i++ {
		if err := handler.Handle(ctx, ev); err != nil {
			t.Fatalf("delivery %d failed: %v", i, err)
		}
	}

	entries, total, err := repo.ListJournalEntries(ctx, "ord-pos-dup01", 10, 0)
	if err != nil || total != 1 || len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry after duplicate delivery, got total=%d len=%d err=%v", total, len(entries), err)
	}
	acc1111, _ := repo.GetAccount(ctx, "1111")
	acc5110, _ := repo.GetAccount(ctx, "5110")
	if acc1111.BalanceVND != 300_000 || acc5110.BalanceVND != 300_000 {
		t.Fatalf("duplicate delivery double-posted balances: 1111=%d 5110=%d", acc1111.BalanceVND, acc5110.BalanceVND)
	}
}
