package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

func TestReconciliationCaseResolution_SC11(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewLedgerRepository(nil)
	settleRepo := repository.NewSettlementRepository(nil)
	caseRepo := repository.NewCaseRepository(nil)
	srv := NewServer(repo, settleRepo, caseRepo, nil, false)

	financeStaff := auth.Identity{UserID: "fin-mgr-01", Roles: []string{"FINANCE_MGR"}, WarehouseScopes: []string{"ALL"}}

	// 1. Initial collection entry for order O-SC11 (660,000 VND)
	initialEntry, err := domain.NewPaymentCapturedEntry("je_init_sc11", "O-SC11", 660000, "PAYMENT_SERVICE")
	if err != nil {
		t.Fatalf("failed to create initial payment entry: %v", err)
	}
	if _, err := repo.RecordJournalEntry(t.Context(), initialEntry); err != nil {
		t.Fatalf("failed to record initial payment entry: %v", err)
	}

	// 2. Import partner settlement CSV containing SC11 transaction
	// Gross: 660,000, Fee in file: 6,600, Net payout: 650,000 -> 3,400 shortfall
	csvContent := `transaction_ref,order_id,type,gross_amount_vnd,fee_amount_vnd,net_amount_vnd,transacted_at
TX-SC11,O-SC11,COLLECTION,660000,6600,650000,2026-11-16T15:30:00+07:00`

	importPayload, _ := json.Marshal(map[string]any{
		"s3Key":   "s3://bucket/settlements/sc11.csv",
		"content": csvContent,
	})

	rec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/settlements/import", string(importPayload), &financeStaff)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import failed: %d (%s)", rec.Code, rec.Body.String())
	}

	var importResp struct {
		Data struct {
			Batch struct {
				BatchID string `json:"batchId"`
			} `json:"batch"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &importResp)
	batchID := importResp.Data.Batch.BatchID
	if batchID == "" {
		t.Fatalf("expected non-empty batch ID, got: %s", rec.Body.String())
	}

	// 3. Run Matching Engine on the imported batch
	matchRec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/settlements/batches/"+batchID+"/match", "", &financeStaff)
	if matchRec.Code != http.StatusOK {
		t.Fatalf("match failed: %d (%s)", matchRec.Code, matchRec.Body.String())
	}

	// 4. Query Discrepancy Cases
	casesRec := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/discrepancies?status=OPEN", "", &financeStaff)
	if casesRec.Code != http.StatusOK {
		t.Fatalf("list discrepancies failed: %d", casesRec.Code)
	}

	var casesResp struct {
		Data []domain.ReconciliationCase `json:"data"`
	}
	if err := json.Unmarshal(casesRec.Body.Bytes(), &casesResp); err != nil {
		t.Fatalf("unmarshal cases error: %v", err)
	}

	if len(casesResp.Data) != 1 {
		t.Fatalf("expected 1 open discrepancy case, got %d", len(casesResp.Data))
	}

	sc11Case := casesResp.Data[0]
	if sc11Case.OrderID != "O-SC11" || sc11Case.DiscrepancyVND != 3400 || sc11Case.Status != "OPEN" {
		t.Fatalf("unexpected case details: %+v", sc11Case)
	}

	// Check Account 1388 balance before resolution (should be Debit 3,400)
	accountsBefore, _ := repo.ListAccounts(t.Context())
	var acc1388Before int64
	for _, acc := range accountsBefore {
		if acc.AccountCode == "1388" {
			acc1388Before = acc.BalanceVND
		}
	}
	if acc1388Before != 3400 {
		t.Fatalf("expected account 1388 balance 3400 before resolution, got %d", acc1388Before)
	}

	// 5. Resolve Discrepancy Case with Category FEE_ADJUSTMENT and Notes
	resolvePayload := `{
		"causeCategory": "FEE_ADJUSTMENT",
		"resolutionNotes": "Partner applied extra international card fee surcharge (3,400 VND); verified per contract annex B",
		"compensatingAccountCode": "6410"
	}`
	resolveRec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/discrepancies/"+sc11Case.CaseID+"/resolve", resolvePayload, &financeStaff)
	if resolveRec.Code != http.StatusOK {
		t.Fatalf("resolve failed: %d (%s)", resolveRec.Code, resolveRec.Body.String())
	}

	var resolveResp struct {
		Data struct {
			Case         domain.ReconciliationCase `json:"case"`
			JournalEntry domain.JournalEntry       `json:"journalEntry"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resolveRec.Body.Bytes(), &resolveResp); err != nil {
		t.Fatalf("unmarshal resolve response error: %v", err)
	}

	if resolveResp.Data.Case.Status != "RESOLVED" {
		t.Fatalf("expected case status RESOLVED, got %s", resolveResp.Data.Case.Status)
	}
	if resolveResp.Data.Case.CauseCategory != "FEE_ADJUSTMENT" {
		t.Fatalf("expected FEE_ADJUSTMENT, got %s", resolveResp.Data.Case.CauseCategory)
	}

	// 6. Verify Compensating Journal Entry
	je := resolveResp.Data.JournalEntry
	if je.EventType != "DISCREPANCY_RESOLVED" {
		t.Fatalf("expected DISCREPANCY_RESOLVED event, got %s", je.EventType)
	}
	if len(je.Postings) != 2 {
		t.Fatalf("expected 2 postings, got %d", len(je.Postings))
	}
	var debitAcc, creditAcc string
	var debitAmt, creditAmt int64
	for _, p := range je.Postings {
		if p.Direction == domain.DirectionDebit {
			debitAcc = p.AccountCode
			debitAmt = p.AmountVND
		} else if p.Direction == domain.DirectionCredit {
			creditAcc = p.AccountCode
			creditAmt = p.AmountVND
		}
	}
	if debitAcc != "6410" || debitAmt != 3400 {
		t.Fatalf("expected Debit 6410 of 3400, got %s %d", debitAcc, debitAmt)
	}
	if creditAcc != "1388" || creditAmt != 3400 {
		t.Fatalf("expected Credit 1388 of 3400, got %s %d", creditAcc, creditAmt)
	}

	// 7. Verify Account 1388 balance after resolution is offset to 0
	accountsAfter, _ := repo.ListAccounts(t.Context())
	var acc1388After int64
	for _, acc := range accountsAfter {
		if acc.AccountCode == "1388" {
			acc1388After = acc.BalanceVND
		}
	}
	if acc1388After != 0 {
		t.Fatalf("expected account 1388 balance offset to 0, got %d", acc1388After)
	}

	// 8. Subsequent resolve attempt on same case is rejected with 409 Conflict
	dupRec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/discrepancies/"+sc11Case.CaseID+"/resolve", resolvePayload, &financeStaff)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on already resolved case, got %d", dupRec.Code)
	}
}
