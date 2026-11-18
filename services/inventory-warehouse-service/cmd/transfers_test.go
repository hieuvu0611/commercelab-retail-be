package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

func TestMultiWarehouseAvailabilityAndAtomicSplitReservation(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewInventoryRepository(nil)
	srv := newTestServer(repo, false)

	// Memory seed has 10 backpacks in WH-HCM-01 and 5 in WH-HN-01 (total 15).
	rec := call(t, srv, http.MethodGet, "/api/v1/inventory/availability?warehouse=ALL&skuIds=SKU-BALO-BLACK-20L", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("availability ALL status = %d: %s", rec.Code, rec.Body.String())
	}

	// Internal breakdown returns per-warehouse counts.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/availability/breakdown?skuIds=SKU-BALO-BLACK-20L", nil)
	req.Header.Set(internalauth.Header, internalauth.Token())
	bdRec := httptest.NewRecorder()
	srv.ServeHTTP(bdRec, req)
	if bdRec.Code != http.StatusOK {
		t.Fatalf("breakdown status = %d: %s", bdRec.Code, bdRec.Body.String())
	}

	// Atomic split failure: ask 10 from WH-HCM-01 + 6 from WH-HN-01 (only 5 in HN) -> must fail and leave HCM untouched.
	failBody := `{"orderId":"O-SPLIT-FAIL","allocations":[{"warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":10}]},{"warehouseId":"WH-HN-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":6}]}]}`
	failRes := call(t, srv, http.MethodPost, "/api/v1/inventory/reservations", failBody, nil)
	if failRes.Code != http.StatusConflict {
		t.Fatalf("expected 409 for failed split reserve, got %d: %s", failRes.Code, failRes.Body.String())
	}
	stHCM, _, _ := repo.GetStock(t.Context(), "WH-HCM-01", "SKU-BALO-BLACK-20L")
	if stHCM.Reserved != 0 || stHCM.Available() != 10 {
		t.Fatalf("expected atomic rollback on WH-HCM-01, got reserved=%d avail=%d", stHCM.Reserved, stHCM.Available())
	}

	// Valid split across both warehouses: 10 in HCM + 3 in HN = 13.
	okBody := `{"orderId":"O-SPLIT-OK","allocations":[{"warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":10}]},{"warehouseId":"WH-HN-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":3}]}]}`
	okRes := call(t, srv, http.MethodPost, "/api/v1/inventory/reservations", okBody, nil)
	if okRes.Code != http.StatusCreated {
		t.Fatalf("expected 201 for split reserve, got %d: %s", okRes.Code, okRes.Body.String())
	}
	comRes := call(t, srv, http.MethodPost, "/api/v1/inventory/reservations/O-SPLIT-OK/commit", "", nil)
	if comRes.Code != http.StatusOK {
		t.Fatalf("commit split failed: %d %s", comRes.Code, comRes.Body.String())
	}

	// Warehouse-scoped handover: WH-HCM-01 staff hands over only WH-HCM-01 parcel.
	hoReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/shipments/handover",
		strings.NewReader(`{"orderId":"O-SPLIT-OK","warehouseId":"WH-HCM-01"}`))
	hoReq.Header.Set("Authorization", bearerFor(t, hcmStaff))
	hoRec := httptest.NewRecorder()
	srv.ServeHTTP(hoRec, hoReq)
	if hoRec.Code != http.StatusOK {
		t.Fatalf("HCM handover failed: %d %s", hoRec.Code, hoRec.Body.String())
	}
	stHCM, _, _ = repo.GetStock(t.Context(), "WH-HCM-01", "SKU-BALO-BLACK-20L")
	stHN, _, _ := repo.GetStock(t.Context(), "WH-HN-01", "SKU-BALO-BLACK-20L")
	if stHCM.OnHand != 0 || stHCM.Committed != 0 {
		t.Fatalf("expected HCM handed over (onHand=0, committed=0), got %+v", stHCM)
	}
	if stHN.OnHand != 5 || stHN.Committed != 3 {
		t.Fatalf("expected HN still committed (onHand=5, committed=3), got %+v", stHN)
	}
}

func TestInterWarehouseTransferLifecycleAndSC09Discrepancy(t *testing.T) {
	authtest.Configure(t)
	var openedCase bool
	boSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/backoffice/exception-cases" && r.Method == http.MethodPost {
			openedCase = true
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"caseId": "CASE-TRF-001"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer boSrv.Close()
	t.Setenv("BACKOFFICE_SERVICE_URL", boSrv.URL)

	repo := repository.NewInventoryRepository(nil)
	srv := newTestServer(repo, false)
	hcmMgr := auth.Identity{UserID: "staff-whm-hcm", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"WH-HCM-01"}}

	// 1. Create transfer of 4 backpacks from WH-HCM-01 -> WH-HN-01
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/inventory/transfers",
		strings.NewReader(`{"fromWarehouseId":"WH-HCM-01","toWarehouseId":"WH-HN-01","notes":"Rebalance HN","items":[{"skuId":"SKU-BALO-BLACK-20L","quantity":4}]}`))
	createReq.Header.Set("Authorization", bearerFor(t, hcmMgr))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create transfer failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var createResp struct {
		Data domain.WarehouseTransfer `json:"data"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
	trfID := createResp.Data.TransferID

	// 2. Dispatch from WH-HCM-01 -> HCM available drops 10->6, HN in_transit becomes 4 (HN available stays 5)
	dispReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/inventory/transfers/"+trfID+"/dispatch", nil)
	dispReq.Header.Set("Authorization", bearerFor(t, hcmMgr))
	dispRec := httptest.NewRecorder()
	srv.ServeHTTP(dispRec, dispReq)
	if dispRec.Code != http.StatusOK {
		t.Fatalf("dispatch failed: %d %s", dispRec.Code, dispRec.Body.String())
	}
	stHCM, _, _ := repo.GetStock(t.Context(), "WH-HCM-01", "SKU-BALO-BLACK-20L")
	stHN, _, _ := repo.GetStock(t.Context(), "WH-HN-01", "SKU-BALO-BLACK-20L")
	if stHCM.Available() != 6 || stHN.InTransit != 4 || stHN.Available() != 5 {
		t.Fatalf("SC09 in-transit invariant violated: HCM=%+v HN=%+v", stHCM, stHN)
	}

	// 3. Receive at WH-HN-01 with SC09 shortage: 3 good, 0 damaged, 1 missing
	recReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/inventory/transfers/"+trfID+"/receive",
		strings.NewReader(`{"lines":[{"skuId":"SKU-BALO-BLACK-20L","receivedGoodQty":3,"damagedQty":0,"missingQty":1}]}`))
	recReq.Header.Set("Authorization", bearerFor(t, hnStaff))
	recReq.Header.Set("Content-Type", "application/json")
	recRec := httptest.NewRecorder()
	srv.ServeHTTP(recRec, recReq)
	if recRec.Code != http.StatusOK {
		t.Fatalf("receive failed: %d %s", recRec.Code, recRec.Body.String())
	}
	var recResp struct {
		Data domain.WarehouseTransfer `json:"data"`
	}
	_ = json.Unmarshal(recRec.Body.Bytes(), &recResp)
	if recResp.Data.Status != domain.TransferStatusReceivedWithDiscrepancy || recResp.Data.ExceptionCaseID != "CASE-TRF-001" || !openedCase {
		t.Fatalf("expected SC09 discrepancy case, got %+v (opened=%v)", recResp.Data, openedCase)
	}
	stHN, _, _ = repo.GetStock(t.Context(), "WH-HN-01", "SKU-BALO-BLACK-20L")
	if stHN.InTransit != 0 || stHN.Available() != 8 || stHN.OnHand != 8 {
		t.Fatalf("expected HN onHand=8, inTransit=0, got %+v", stHN)
	}
}

func TestTransferInvalidStateReturnsConflict(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewInventoryRepository(nil)
	srv := newTestServer(repo, false)
	hcmMgr := auth.Identity{UserID: "staff-whm-hcm", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"WH-HCM-01"}}

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/inventory/transfers",
		strings.NewReader(`{"fromWarehouseId":"WH-HCM-01","toWarehouseId":"WH-HN-01","notes":"Un-dispatched","items":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`))
	createReq.Header.Set("Authorization", bearerFor(t, hcmMgr))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create transfer failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var createResp struct {
		Data domain.WarehouseTransfer `json:"data"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)

	// Attempting to receive before dispatch must return 409 TRANSFER_INVALID_STATE.
	recReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/inventory/transfers/"+createResp.Data.TransferID+"/receive",
		strings.NewReader(`{"lines":[{"skuId":"SKU-BALO-BLACK-20L","receivedGoodQty":1,"damagedQty":0,"missingQty":0}]}`))
	recReq.Header.Set("Authorization", bearerFor(t, hnStaff))
	recReq.Header.Set("Content-Type", "application/json")
	recRec := httptest.NewRecorder()
	srv.ServeHTTP(recRec, recReq)
	if recRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for receiving non-in-transit transfer, got %d: %s", recRec.Code, recRec.Body.String())
	}
}
