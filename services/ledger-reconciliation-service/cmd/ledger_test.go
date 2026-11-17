package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/ledger-reconciliation-service/internal/repository"
)

func bearerFor(t *testing.T, id auth.Identity) string {
	t.Helper()
	token, err := auth.SignAccessToken(id)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return "Bearer " + token
}

func call(t *testing.T, srv http.Handler, method, path, body string, id *auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if id != nil {
		req.Header.Set("Authorization", bearerFor(t, *id))
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestLedgerAPI(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewLedgerRepository(nil)
	settleRepo := repository.NewSettlementRepository(nil)
	srv := NewServer(repo, settleRepo, nil, nil, false)

	financeStaff := auth.Identity{UserID: "fin-01", Roles: []string{"FINANCE"}, WarehouseScopes: []string{"ALL"}}
	customer := auth.Identity{UserID: "cust-01", Roles: []string{"CUSTOMER"}}

	t.Run("unauthenticated request returns 401", func(t *testing.T) {
		rec := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/accounts", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("customer role returns 403", func(t *testing.T) {
		rec := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/accounts", "", &customer)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", rec.Code)
		}
	})

	t.Run("list accounts returns standard chart of accounts", func(t *testing.T) {
		rec := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/accounts", "", &financeStaff)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data []struct {
				AccountCode string `json:"accountCode"`
				AccountName string `json:"accountName"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(resp.Data) < 7 {
			t.Fatalf("expected at least 7 accounts, got %d", len(resp.Data))
		}
	})

	t.Run("record balanced journal entry succeeds", func(t *testing.T) {
		body := `{
			"entryId": "JE-API-001",
			"eventType": "PAYMENT_CAPTURED",
			"referenceId": "ORD-API-660K",
			"description": "Order 660k payment",
			"postings": [
				{"accountCode": "1120", "direction": "DEBIT", "amountVnd": 660000},
				{"accountCode": "5110", "direction": "CREDIT", "amountVnd": 660000}
			]
		}`
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/entries", body, &financeStaff)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
		}

		// Verify entry can be retrieved by ID
		recGet := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/entries/JE-API-001", "", &financeStaff)
		if recGet.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", recGet.Code, recGet.Body.String())
		}
	})

	t.Run("record unbalanced journal entry is rejected with 400", func(t *testing.T) {
		body := `{
			"entryId": "JE-UNBALANCED",
			"eventType": "MANUAL",
			"referenceId": "REF-FAIL",
			"postings": [
				{"accountCode": "1120", "direction": "DEBIT", "amountVnd": 660000},
				{"accountCode": "5110", "direction": "CREDIT", "amountVnd": 650000}
			]
		}`
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/ledger/entries", body, &financeStaff)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("list journal entries with reference filter", func(t *testing.T) {
		rec := call(t, srv, http.MethodGet, "/api/v1/admin/ledger/entries?referenceId=ORD-API-660K", "", &financeStaff)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data struct {
				Total   int `json:"total"`
				Entries []struct {
					EntryID string `json:"entryId"`
				} `json:"entries"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if resp.Data.Total != 1 {
			t.Fatalf("expected 1 entry, got %d", resp.Data.Total)
		}
	})
}
