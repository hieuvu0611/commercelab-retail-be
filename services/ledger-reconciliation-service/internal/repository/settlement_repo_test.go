package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
)

func TestMatchBatch_PropagatesErrorsAndPersistsMatchedAndDiscrepancy(t *testing.T) {
	ctx := context.Background()

	csvPayload := `transaction_ref,order_id,type,gross_amount_vnd,fee_amount_vnd,net_amount_vnd,transacted_at
TX-OK-001,ORD-100K,COLLECTION,100000,1000,99000,2026-11-16T15:00:00+07:00
TX-SHORT-002,ORD-660K,COLLECTION,660000,6600,650000,2026-11-16T15:30:00+07:00`

	t.Run("propagates error when ledger journal posting fails instead of ignoring it", func(t *testing.T) {
		ledgerRepo := NewLedgerRepository(nil)
		caseRepo := NewCaseRepository(nil)
		settleRepo := NewSettlementRepository(nil)

		// Remove required fee expense account 6410 so RecordJournalEntry returns ErrAccountNotFound.
		delete(ledgerRepo.accounts, "6410")

		batch, lines, err := domain.ParseSettlementCSV(strings.NewReader(csvPayload), "err.csv", "s3://err.csv", "finance-mgr")
		if err != nil {
			t.Fatalf("parse csv: %v", err)
		}
		if _, _, err := settleRepo.ImportSettlementBatch(ctx, batch, lines); err != nil {
			t.Fatalf("import batch: %v", err)
		}

		_, err = settleRepo.MatchBatch(ctx, batch.BatchID, ledgerRepo, caseRepo, "finance-mgr")
		if !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("expected ErrAccountNotFound to propagate from MatchBatch, got %v", err)
		}

		storedBatch, err := settleRepo.GetSettlementBatch(ctx, batch.BatchID)
		if err != nil {
			t.Fatalf("get batch: %v", err)
		}
		if storedBatch.Status == "RECONCILED" || storedBatch.Status == "PARTIALLY_RECONCILED" {
			t.Fatalf("batch status must not advance on MatchBatch error, got %q", storedBatch.Status)
		}
	})

	t.Run("records cases, journal entries, and updates batch status on success", func(t *testing.T) {
		ledgerRepo := NewLedgerRepository(nil)
		caseRepo := NewCaseRepository(nil)
		settleRepo := NewSettlementRepository(nil)

		batch, lines, err := domain.ParseSettlementCSV(strings.NewReader(csvPayload), "ok.csv", "s3://ok.csv", "finance-mgr")
		if err != nil {
			t.Fatalf("parse csv: %v", err)
		}
		if _, _, err := settleRepo.ImportSettlementBatch(ctx, batch, lines); err != nil {
			t.Fatalf("import batch: %v", err)
		}

		summary, err := settleRepo.MatchBatch(ctx, batch.BatchID, ledgerRepo, caseRepo, "finance-mgr")
		if err != nil {
			t.Fatalf("MatchBatch failed: %v", err)
		}
		if summary.MatchedLines != 1 || summary.DiscrepancyLines != 1 || summary.JournalEntriesCreated != 2 {
			t.Fatalf("unexpected match summary: %+v", summary)
		}
		if summary.BatchStatus != "PARTIALLY_RECONCILED" {
			t.Fatalf("expected PARTIALLY_RECONCILED, got %q", summary.BatchStatus)
		}

		cases, err := caseRepo.ListCases(ctx, CaseFilter{BatchID: batch.BatchID})
		if err != nil {
			t.Fatalf("list cases: %v", err)
		}
		if len(cases) != 1 || cases[0].OrderID != "ORD-660K" || cases[0].DiscrepancyVND != 3400 {
			t.Fatalf("expected 1 open 3400 VND discrepancy case for ORD-660K, got %+v", cases)
		}

		acc1388, err := ledgerRepo.GetAccount(ctx, "1388")
		if err != nil || acc1388.BalanceVND != 3400 {
			t.Fatalf("expected account 1388 balance 3400, got %+v (err=%v)", acc1388, err)
		}
	})
}
