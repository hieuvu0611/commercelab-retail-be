package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

func TestSettlementImportAPI(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewLedgerRepository(nil)
	settleRepo := repository.NewSettlementRepository(nil)
	srv := NewServer(repo, settleRepo, nil, false)

	financeStaff := auth.Identity{UserID: "fin-01", Roles: []string{"FINANCE"}, WarehouseScopes: []string{"ALL"}}

	csvPayload := `transaction_ref,order_id,type,gross_amount_vnd,fee_amount_vnd,net_amount_vnd,transacted_at
TX-VNPAY-001,O-660K,COLLECTION,660000,6600,650000,2026-11-16T15:30:00+07:00
TX-VNPAY-002,O-100K,COLLECTION,100000,1000,99000,2026-11-16T16:00:00+07:00`

	var firstBatchID string

	t.Run("first import succeeds with 201 Created and alreadyImported=false", func(t *testing.T) {
		body := map[string]string{
			"filename": "settlement-20261116.csv",
			"s3Key":    "s3://bucket/settlements/20261116.csv",
			"content":  csvPayload,
		}
		bytes, _ := json.Marshal(body)

		rec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/settlements/import", string(bytes), &financeStaff)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d (%s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			Data struct {
				AlreadyImported bool `json:"alreadyImported"`
				LinesCount      int  `json:"linesCount"`
				Batch           struct {
					BatchID       string `json:"batchId"`
					TotalGrossVND int64  `json:"totalGrossVnd"`
					TotalNetVND   int64  `json:"totalNetVnd"`
				} `json:"batch"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}

		if resp.Data.AlreadyImported {
			t.Fatal("expected alreadyImported to be false on first import")
		}
		if resp.Data.LinesCount != 2 {
			t.Fatalf("expected 2 lines, got %d", resp.Data.LinesCount)
		}
		if resp.Data.Batch.TotalGrossVND != 760_000 {
			t.Fatalf("expected 760,000 gross, got %d", resp.Data.Batch.TotalGrossVND)
		}
		firstBatchID = resp.Data.Batch.BatchID
	})

	t.Run("re-importing same CSV is strictly idempotent (200 OK and alreadyImported=true)", func(t *testing.T) {
		body := map[string]string{
			"filename": "settlement-20261116.csv",
			"s3Key":    "s3://bucket/settlements/20261116.csv",
			"content":  csvPayload,
		}
		bytes, _ := json.Marshal(body)

		rec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/settlements/import", string(bytes), &financeStaff)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for idempotent duplicate, got %d (%s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			Data struct {
				AlreadyImported bool `json:"alreadyImported"`
				Batch           struct {
					BatchID string `json:"batchId"`
				} `json:"batch"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}

		if !resp.Data.AlreadyImported {
			t.Fatal("expected alreadyImported to be true on second import")
		}
		if resp.Data.Batch.BatchID != firstBatchID {
			t.Fatalf("expected same batch ID %s, got %s", firstBatchID, resp.Data.Batch.BatchID)
		}
	})

	t.Run("list batches returns imported batch", func(t *testing.T) {
		rec := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/settlements/batches", "", &financeStaff)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp struct {
			Data struct {
				Total   int `json:"total"`
				Batches []struct {
					BatchID string `json:"batchId"`
				} `json:"batches"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if resp.Data.Total != 1 {
			t.Fatalf("expected 1 batch, got %d", resp.Data.Total)
		}
	})

	t.Run("list batch lines returns parsed rows", func(t *testing.T) {
		rec := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/settlements/batches/"+firstBatchID+"/lines", "", &financeStaff)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp struct {
			Data []struct {
				TransactionRef string `json:"transactionRef"`
				OrderID        string `json:"orderId"`
				NetAmountVND   int64  `json:"netAmountVnd"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(resp.Data) != 2 {
			t.Fatalf("expected 2 lines, got %d", len(resp.Data))
		}
		if resp.Data[0].OrderID != "O-660K" || resp.Data[0].NetAmountVND != 650_000 {
			t.Fatalf("unexpected line: %+v", resp.Data[0])
		}
	})
}
