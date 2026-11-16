package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

func TestProcurementAPI(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewInventoryRepository(nil)
	srv := newTestServer(repo, false)

	procMgr := auth.Identity{UserID: "proc-mgr-01", Roles: []string{"PROCUREMENT_MGR"}, WarehouseScopes: []string{"ALL"}}
	opsDirector := auth.Identity{UserID: "ops-dir-01", Roles: []string{"OPS_DIRECTOR"}, WarehouseScopes: []string{"ALL"}}
	whStaff := auth.Identity{UserID: "wh-staff-01", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}}

	t.Run("list and create suppliers", func(t *testing.T) {
		// List initial suppliers
		rec := call(t, srv, http.MethodGet, "/api/v1/admin/procurement/suppliers", "", &procMgr)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
		}

		// Create new supplier
		body := `{"name":"Gia Dinh Textiles","code":"GIADINH","contactEmail":"giadinh@textiles.vn","phone":"0987654321","address":"District 12, HCMC"}`
		rec = call(t, srv, http.MethodPost, "/api/v1/admin/procurement/suppliers", body, &procMgr)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("create PO and maker != checker approval flow", func(t *testing.T) {
		poBodyUnder20M := `{
			"supplierId": "SUP-EVEREST-01",
			"warehouseId": "WH-HCM-01",
			"notes": "Winter replenishment",
			"lines": [
				{"skuId": "SKU-BALO-BLACK-20L", "orderedQty": 50, "unitCostVnd": 200000}
			]
		}`
		// Create PO under 20M (50 * 200,000 = 10,000,000 VND) by procMgr
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/procurement/orders", poBodyUnder20M, &procMgr)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data struct {
				POID           string `json:"poId"`
				Status         string `json:"status"`
				TotalAmountVND int64  `json:"totalAmountVnd"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		poID := resp.Data.POID
		if resp.Data.TotalAmountVND != 10_000_000 {
			t.Errorf("got %d, want 10,000,000", resp.Data.TotalAmountVND)
		}

		// Maker cannot self-approve (P10)
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/procurement/orders/%s/approve", poID), "", &procMgr)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for self-approval, got %d (%s)", rec.Code, rec.Body.String())
		}

		// Other manager (opsDirector) approves
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/procurement/orders/%s/approve", poID), "", &opsDirector)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for approval, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("high value PO threshold check (>20M)", func(t *testing.T) {
		maker := auth.Identity{UserID: "proc-staff-01", Roles: []string{"PROCUREMENT_MGR"}, WarehouseScopes: []string{"ALL"}}
		poBodyOver20M := `{
			"supplierId": "SUP-EVEREST-01",
			"warehouseId": "WH-HCM-01",
			"notes": "Large replenishment",
			"lines": [
				{"skuId": "SKU-BALO-BLACK-20L", "orderedQty": 150, "unitCostVnd": 200000}
			]
		}`
		// 150 * 200,000 = 30,000,000 VND
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/procurement/orders", poBodyOver20M, &maker)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d (%s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Data struct {
				POID string `json:"poId"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		highPOID := resp.Data.POID

		// Regular procurement manager (different user) attempts to approve >20M -> rejected
		checkerProcMgr := auth.Identity{UserID: "proc-mgr-02", Roles: []string{"PROCUREMENT_MGR"}, WarehouseScopes: []string{"ALL"}}
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/procurement/orders/%s/approve", highPOID), "", &checkerProcMgr)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for exceeding threshold, got %d (%s)", rec.Code, rec.Body.String())
		}

		// Ops Director approves -> success
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/procurement/orders/%s/approve", highPOID), "", &opsDirector)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for ops director, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("rejection flow", func(t *testing.T) {
		maker := auth.Identity{UserID: "maker-03", Roles: []string{"PROCUREMENT_MGR"}, WarehouseScopes: []string{"ALL"}}
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/procurement/orders", `{
			"supplierId": "SUP-EVEREST-01",
			"warehouseId": "WH-HCM-01",
			"lines": [{"skuId": "SKU-BALO-BLACK-20L", "orderedQty": 10, "unitCostVnd": 200000}]
		}`, &maker)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create failed: %s", rec.Body.String())
		}
		var resp struct {
			Data struct {
				POID string `json:"poId"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)

		// Reject PO
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/procurement/orders/%s/reject", resp.Data.POID), `{"reason":"Budget frozen"}`, &opsDirector)
		if rec.Code != http.StatusOK {
			t.Fatalf("reject failed: %s", rec.Body.String())
		}
	})

	t.Run("warehouse staff cannot create PO", func(t *testing.T) {
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/procurement/orders", `{"supplierId":"SUP-1","lines":[{"skuId":"A","orderedQty":1,"unitCostVnd":10}]}`, &whStaff)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden for staff creating PO, got %d", rec.Code)
		}
	})
}
