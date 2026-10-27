package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
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
