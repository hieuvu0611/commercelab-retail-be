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

	t.Run("SC09 receiving with short and damaged quantities", func(t *testing.T) {
		// 1. Create PO for 100 backpacks
		createPOBody := `{
			"supplierId": "SUP-EVEREST-01",
			"warehouseId": "WH-HCM-01",
			"notes": "SC09 test batch",
			"lines": [{"skuId": "SKU-BALO-BLACK-20L", "orderedQty": 100, "unitCostVnd": 150000}]
		}`
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/procurement/orders", createPOBody, &procMgr)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create PO failed: %s", rec.Body.String())
		}
		var poResp struct {
			Data struct {
				POID string `json:"poId"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &poResp)
		poID := poResp.Data.POID

		// 2. Approve PO
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/procurement/orders/%s/approve", poID), "", &opsDirector)
		if rec.Code != http.StatusOK {
			t.Fatalf("approve PO failed: %s", rec.Body.String())
		}

		// Initial stock check
		initialStock, ok, _ := repo.GetStock(nil, "WH-HCM-01", "SKU-BALO-BLACK-20L")
		if !ok {
			t.Fatalf("stock not found")
		}
		initialOnHand := initialStock.OnHand
		initialUnsellable := initialStock.Unsellable
		initialAvailable := initialStock.Available()

		// 3. Receive goods with SC09 numbers:
		// Order 100, deliver 90 (short 10), 85 passed, 5 damaged
		inspBody := fmt.Sprintf(`{
			"poId": "%s",
			"notes": "SC09 inbound inspection",
			"lines": [
				{
					"skuId": "SKU-BALO-BLACK-20L",
					"deliveredQty": 90,
					"passedQty": 85,
					"damagedQty": 5
				}
			]
		}`, poID)
		rec = call(t, srv, http.MethodPost, "/api/v1/admin/inventory/receiving/inspections", inspBody, &whStaff)
		if rec.Code != http.StatusCreated {
			t.Fatalf("receiving inspection failed: %s", rec.Body.String())
		}

		var inspResp struct {
			Data struct {
				InspectionID string `json:"inspectionId"`
				Lines        []struct {
					SkuID        string `json:"skuId"`
					DeliveredQty int64  `json:"deliveredQty"`
					PassedQty    int64  `json:"passedQty"`
					DamagedQty   int64  `json:"damagedQty"`
					ShortQty     int64  `json:"shortQty"`
				} `json:"lines"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &inspResp); err != nil {
			t.Fatalf("unmarshal inspection response: %v", err)
		}

		if len(inspResp.Data.Lines) == 0 {
			t.Fatalf("expected inspection lines")
		}
		line := inspResp.Data.Lines[0]
		if line.ShortQty != 10 {
			t.Errorf("expected shortQty 10, got %d", line.ShortQty)
		}
		if line.PassedQty != 85 {
			t.Errorf("expected passedQty 85, got %d", line.PassedQty)
		}
		if line.DamagedQty != 5 {
			t.Errorf("expected damagedQty 5, got %d", line.DamagedQty)
		}

		// 4. Verify post-inspection stock:
		// OnHand must increase by +90
		// Unsellable must increase by +5
		// Available must increase by +85
		postStock, _, _ := repo.GetStock(nil, "WH-HCM-01", "SKU-BALO-BLACK-20L")
		if postStock.OnHand != initialOnHand+90 {
			t.Errorf("OnHand: got %d, want %d (+90)", postStock.OnHand, initialOnHand+90)
		}
		if postStock.Unsellable != initialUnsellable+5 {
			t.Errorf("Unsellable: got %d, want %d (+5)", postStock.Unsellable, initialUnsellable+5)
		}
		if postStock.Available() != initialAvailable+85 {
			t.Errorf("Available: got %d, want %d (+85)", postStock.Available(), initialAvailable+85)
		}

		// Invariant I01 check
		if postStock.Available() != (postStock.OnHand - postStock.Unsellable - postStock.Reserved - postStock.Committed) {
			t.Errorf("I01 violated: available (%d) != onHand(%d) - unsellable(%d) - reserved(%d) - committed(%d)",
				postStock.Available(), postStock.OnHand, postStock.Unsellable, postStock.Reserved, postStock.Committed)
		}
	})

	t.Run("invalid inspection line math rejected", func(t *testing.T) {
		// Create a PO for testing invalid line math
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/procurement/orders", `{
			"supplierId": "SUP-EVEREST-01",
			"warehouseId": "WH-HCM-01",
			"lines": [{"skuId": "SKU-BALO-BLACK-20L", "orderedQty": 100, "unitCostVnd": 100000}]
		}`, &procMgr)
		var poResp struct {
			Data struct {
				POID string `json:"poId"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &poResp)
		_ = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/procurement/orders/%s/approve", poResp.Data.POID), "", &opsDirector)

		// delivered 90 != passed 80 + damaged 5 (=85)
		invalidBody := fmt.Sprintf(`{
			"poId": "%s",
			"lines": [{"skuId": "SKU-BALO-BLACK-20L", "deliveredQty": 90, "passedQty": 80, "damagedQty": 5}]
		}`, poResp.Data.POID)
		rec = call(t, srv, http.MethodPost, "/api/v1/admin/inventory/receiving/inspections", invalidBody, &whStaff)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for math mismatch, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("supplier return of unsellable goods leaves available unchanged", func(t *testing.T) {
		// Currently SKU-BALO-BLACK-20L has Unsellable = 5, OnHand = 100, Available = 95
		stockBefore, _, _ := repo.GetStock(nil, "WH-HCM-01", "SKU-BALO-BLACK-20L")
		if stockBefore.Unsellable < 5 {
			t.Fatalf("expected unsellable >= 5, got %d", stockBefore.Unsellable)
		}
		availBefore := stockBefore.Available()
		onHandBefore := stockBefore.OnHand

		// 1. Create draft supplier return for 5 units
		rtvBody := `{
			"supplierId": "SUP-EVEREST-01",
			"warehouseId": "WH-HCM-01",
			"notes": "Return damaged units from batch",
			"lines": [
				{"skuId": "SKU-BALO-BLACK-20L", "quantity": 5, "reason": "Torn fabric detected during inbound QC"}
			]
		}`
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/inventory/supplier-returns", rtvBody, &whStaff)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create supplier return failed: %s", rec.Body.String())
		}
		var rtvResp struct {
			Data struct {
				ReturnID string `json:"returnId"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &rtvResp)
		rtvID := rtvResp.Data.ReturnID

		// 2. Dispatch supplier return
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/supplier-returns/%s/dispatch", rtvID), `{"trackingNumber":"VNPOST-RTV-9988"}`, &whStaff)
		if rec.Code != http.StatusOK {
			t.Fatalf("dispatch supplier return failed: %s", rec.Body.String())
		}

		// 3. Verify stock changes:
		// OnHand decreases by 5
		// Unsellable decreases by 5
		// Available remains unchanged!
		stockAfter, _, _ := repo.GetStock(nil, "WH-HCM-01", "SKU-BALO-BLACK-20L")
		if stockAfter.OnHand != onHandBefore-5 {
			t.Errorf("OnHand: got %d, want %d (-5)", stockAfter.OnHand, onHandBefore-5)
		}
		if stockAfter.Unsellable != 0 {
			t.Errorf("Unsellable: got %d, want 0 (-5)", stockAfter.Unsellable)
		}
		if stockAfter.Available() != availBefore {
			t.Errorf("Available changed! got %d, want %d", stockAfter.Available(), availBefore)
		}

		// 4. Returning more than available unsellable stock is rejected
		rec = call(t, srv, http.MethodPost, "/api/v1/admin/inventory/supplier-returns", rtvBody, &whStaff)
		_ = json.Unmarshal(rec.Body.Bytes(), &rtvResp)
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/supplier-returns/%s/dispatch", rtvResp.Data.ReturnID), "", &whStaff)
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409 Conflict when unsellable stock is insufficient, got %d", rec.Code)
		}
	})

	t.Run("stocktake discrepancy blocks sales immediately and requires maker != checker approval (I01, P10)", func(t *testing.T) {
		// Initial stock for SKU-BOTTLE-BLUE-500ML: OnHand 20, Available 20, Unsellable 0
		stBefore, _, _ := repo.GetStock(nil, "WH-HCM-01", "SKU-BOTTLE-BLUE-500ML")
		if stBefore.OnHand != 20 || stBefore.Available() != 20 {
			t.Fatalf("expected 20 on hand & available, got %d / %d", stBefore.OnHand, stBefore.Available())
		}

		// 1. Start stocktake session by whStaff
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/inventory/stocktakes", `{"warehouseId":"WH-HCM-01","notes":"Weekly audit"}`, &whStaff)
		if rec.Code != http.StatusCreated {
			t.Fatalf("start stocktake failed: %s", rec.Body.String())
		}
		var stkResp struct {
			Data struct {
				StocktakeID string `json:"stocktakeId"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &stkResp)
		stkID := stkResp.Data.StocktakeID

		// 2. Submit physical count: shelf count is 17 (3 units missing, diff = -3, unitCost = 100,000)
		countBody := fmt.Sprintf(`{
			"lines": [
				{"skuId": "SKU-BOTTLE-BLUE-500ML", "countedOnHand": 17, "unitCostVnd": 100000}
			]
		}`)
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/stocktakes/%s/count", stkID), countBody, &whStaff)
		if rec.Code != http.StatusOK {
			t.Fatalf("submit count failed: %s", rec.Body.String())
		}

		// Verify sales blocked immediately (I01):
		// Available must drop from 20 to 17
		// Unsellable must increase from 0 to 3
		stDuring, _, _ := repo.GetStock(nil, "WH-HCM-01", "SKU-BOTTLE-BLUE-500ML")
		if stDuring.Available() != 17 {
			t.Errorf("Sales not blocked! Available: got %d, want 17", stDuring.Available())
		}
		if stDuring.Unsellable != 3 {
			t.Errorf("Unsellable: got %d, want 3", stDuring.Unsellable)
		}

		// Get adjustment ID
		var submitResp struct {
			Data struct {
				Adjustments []struct {
					AdjustmentID string `json:"adjustmentId"`
				} `json:"adjustments"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &submitResp)
		if len(submitResp.Data.Adjustments) == 0 {
			t.Fatalf("expected adjustment created")
		}
		adjID := submitResp.Data.Adjustments[0].AdjustmentID

		// 3. Maker (whStaff) attempts to self-approve -> 403 Forbidden (P10)
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/adjustments/%s/decide", adjID), `{"action":"APPROVE"}`, &whStaff)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for self-approval, got %d", rec.Code)
		}

		// 4. Warehouse Manager (hcmManager) approves small discrepancy (3 units <= 5 units, 300k <= 2M)
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/adjustments/%s/decide", adjID), `{"action":"APPROVE","note":"Verified shelf count loss"}`, &hcmManager)
		if rec.Code != http.StatusOK {
			t.Fatalf("manager approval failed: %s", rec.Body.String())
		}

		// Verify final stock:
		// OnHand is now 17
		// Unsellable is 0
		// Available is 17
		stFinal, _, _ := repo.GetStock(nil, "WH-HCM-01", "SKU-BOTTLE-BLUE-500ML")
		if stFinal.OnHand != 17 {
			t.Errorf("final OnHand: got %d, want 17", stFinal.OnHand)
		}
		if stFinal.Unsellable != 0 {
			t.Errorf("final Unsellable: got %d, want 0", stFinal.Unsellable)
		}
		if stFinal.Available() != 17 {
			t.Errorf("final Available: got %d, want 17", stFinal.Available())
		}
	})

	t.Run("large stock adjustment threshold requires supply chain or admin", func(t *testing.T) {
		// Large count discrepancy: diff = -10 (> 5 units)
		rec := call(t, srv, http.MethodPost, "/api/v1/admin/inventory/stocktakes", `{"warehouseId":"WH-HCM-01"}`, &whStaff)
		var stkResp struct {
			Data struct {
				StocktakeID string `json:"stocktakeId"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &stkResp)

		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/stocktakes/%s/count", stkResp.Data.StocktakeID), `{
			"lines": [{"skuId": "SKU-BALO-BLACK-20L", "countedOnHand": 85, "unitCostVnd": 200000}]
		}`, &whStaff)
		var submitResp struct {
			Data struct {
				Adjustments []struct {
					AdjustmentID string `json:"adjustmentId"`
				} `json:"adjustments"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &submitResp)
		largeAdjID := submitResp.Data.Adjustments[0].AdjustmentID

		// Warehouse manager attempts to approve >5 units -> rejected 403
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/adjustments/%s/decide", largeAdjID), `{"action":"APPROVE"}`, &hcmManager)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for warehouse manager on large discrepancy, got %d", rec.Code)
		}

		// Supply chain manager approves -> 200 OK
		scMgr := auth.Identity{UserID: "sc-mgr-01", Roles: []string{"SUPPLY_CHAIN_MGR"}, WarehouseScopes: []string{"ALL"}}
		rec = call(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/admin/inventory/adjustments/%s/decide", largeAdjID), `{"action":"APPROVE","note":"Approved by supply chain lead"}`, &scMgr)
		if rec.Code != http.StatusOK {
			t.Fatalf("supply chain approval failed: %s", rec.Body.String())
		}
	})
}
