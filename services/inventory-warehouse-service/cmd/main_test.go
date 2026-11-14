package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInventoryReserveCommitHandoverAndWarehouseScope_SC02_SC12(t *testing.T) {
	repo := repository.NewInventoryRepository(nil)
	srv := NewServer(repo, false)

	// 1. Giữ tồn 2 Balo + 1 Bình nước cho đơn O-001 (SC02)
	resBody := []byte(`{
		"orderId": "O-001",
		"warehouseId": "WH-HCM-01",
		"lines": [
			{"skuId": "SKU-BALO-BLACK-20L", "quantity": 2},
			{"skuId": "SKU-BOTTLE-BLUE-500ML", "quantity": 1}
		]
	}`)
	resReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reservations", bytes.NewReader(resBody))
	resRec := httptest.NewRecorder()
	srv.ServeHTTP(resRec, resReq)
	if resRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on reserve, got %d: %s", resRec.Code, resRec.Body.String())
	}

	// 2. Commit giữ tồn khi khách thanh toán xong -> Available vẫn là 8
	comReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reservations/O-001/commit", nil)
	comRec := httptest.NewRecorder()
	srv.ServeHTTP(comRec, comReq)
	if comRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on commit, got %d: %s", comRec.Code, comRec.Body.String())
	}

	// 3. Nhân viên Kho HN (X-Warehouse-Scope: WH-HN-01) cố tình bấm bàn giao đơn của Kho HCM -> bị chặn 403 (SC12, I08)
	badHandoverReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/shipments/handover", bytes.NewReader([]byte(`{"orderId":"O-001"}`)))
	badHandoverReq.Header.Set("X-Warehouse-Scope", "WH-HN-01")
	badHandoverRec := httptest.NewRecorder()
	srv.ServeHTTP(badHandoverRec, badHandoverReq)
	if badHandoverRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for wrong warehouse scope (SC12), got %d", badHandoverRec.Code)
	}

	// 4. Nhân viên Kho HCM (X-Warehouse-Scope: WH-HCM-01) bàn giao hợp lệ -> OnHand giảm từ 10 xuống 8, Available vẫn là 8
	okHandoverReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/shipments/handover", bytes.NewReader([]byte(`{"orderId":"O-001"}`)))
	okHandoverReq.Header.Set("X-Warehouse-Scope", "WH-HCM-01")
	okHandoverRec := httptest.NewRecorder()
	srv.ServeHTTP(okHandoverRec, okHandoverReq)
	if okHandoverRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on handover, got %d: %s", okHandoverRec.Code, okHandoverRec.Body.String())
	}

	// 5. Kiểm tra tồn kho Balo sau bàn giao: OnHand = 8, Committed = 0, Available = 8
	stReq := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/SKU-BALO-BLACK-20L", nil)
	stRec := httptest.NewRecorder()
	srv.ServeHTTP(stRec, stReq)
	var stResp struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(stRec.Body.Bytes(), &stResp)
	if int64(stResp.Data["onHand"].(float64)) != 8 || int64(stResp.Data["available"].(float64)) != 8 {
		t.Fatalf("expected onHand=8 and available=8 after handover, got %+v", stResp.Data)
	}
}

func TestStoreOutageAnswers503InsteadOfSeededStock(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://inventory_svc_user:x@127.0.0.1:1/commerce_lab?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	srv := NewServer(repository.NewInventoryRepository(pool), true)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "stock lookup", method: http.MethodGet, path: "/api/v1/inventory/SKU-BALO-BLACK-20L"},
		{name: "reserve", method: http.MethodPost, path: "/api/v1/inventory/reservations",
			body: `{"orderId":"O-1","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`},
		{name: "commit", method: http.MethodPost, path: "/api/v1/inventory/reservations/O-1/commit"},
		{name: "release", method: http.MethodPost, path: "/api/v1/inventory/reservations/O-1/release"},
		{name: "handover", method: http.MethodPost, path: "/api/v1/inventory/shipments/handover", body: `{"orderId":"O-1"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Warehouse-Scope", "WH-HCM-01")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "DEPENDENCY_UNAVAILABLE") {
				t.Fatalf("missing DEPENDENCY_UNAVAILABLE code: %s", rec.Body.String())
			}
		})
	}
}
