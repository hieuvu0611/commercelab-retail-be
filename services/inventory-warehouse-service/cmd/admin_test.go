package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

var (
	hcmManager = auth.Identity{UserID: "mgr-hcm", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"WH-HCM-01"}}
	customer   = auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}}
)

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

const openingBody = `{"warehouseId":"WH-HCM-01","reason":"initial count","lines":[{"skuId":"SKU-NEW-1","quantity":12},{"skuId":"SKU-NEW-2","quantity":3}]}`

func TestOpeningStockIsRestrictedToManagersInTheirWarehouse(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(repository.NewInventoryRepository(nil), false)
	const path = "/api/v1/admin/inventory/opening-stock"

	for name, tc := range map[string]struct {
		id   *auth.Identity
		body string
		want int
	}{
		"anonymous":            {nil, openingBody, http.StatusUnauthorized},
		"customer":             {&customer, openingBody, http.StatusForbidden},
		"staff is not manager": {&hcmStaff, openingBody, http.StatusForbidden},
		"manager of other warehouse": {&auth.Identity{UserID: "mgr-hn", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"WH-HN-01"}},
			openingBody, http.StatusForbidden},
		"no reason":    {&hcmManager, `{"warehouseId":"WH-HCM-01","reason":" ","lines":[{"skuId":"A","quantity":1}]}`, http.StatusBadRequest},
		"no lines":     {&hcmManager, `{"warehouseId":"WH-HCM-01","reason":"x","lines":[]}`, http.StatusBadRequest},
		"zero qty":     {&hcmManager, `{"warehouseId":"WH-HCM-01","reason":"x","lines":[{"skuId":"A","quantity":0}]}`, http.StatusBadRequest},
		"no warehouse": {&hcmManager, `{"reason":"x","lines":[{"skuId":"A","quantity":1}]}`, http.StatusBadRequest},
		"manager ok":   {&hcmManager, openingBody, http.StatusCreated},
	} {
		if rec := call(t, srv, http.MethodPost, path, tc.body, tc.id); rec.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestOpeningStockIsRecordedOnceAndAppearsInStockAndHistory(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(repository.NewInventoryRepository(nil), false)

	if rec := call(t, srv, http.MethodPost, "/api/v1/admin/inventory/opening-stock", openingBody, &hcmManager); rec.Code != http.StatusCreated {
		t.Fatalf("first call: %d %s", rec.Code, rec.Body.String())
	}
	// A second call (even with one new SKU) is rejected as a whole.
	again := `{"warehouseId":"WH-HCM-01","reason":"again","lines":[{"skuId":"SKU-NEW-3","quantity":1},{"skuId":"SKU-NEW-1","quantity":5}]}`
	rec := call(t, srv, http.MethodPost, "/api/v1/admin/inventory/opening-stock", again, &hcmManager)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "OPENING_STOCK_ALREADY_RECORDED") {
		t.Fatalf("second call: %d %s; want 409", rec.Code, rec.Body.String())
	}

	stock := call(t, srv, http.MethodGet, "/api/v1/admin/inventory/stock?warehouse=WH-HCM-01", "", &hcmStaff)
	var stockResp struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(stock.Body.Bytes(), &stockResp)
	found := map[string]float64{}
	for _, row := range stockResp.Data {
		found[row["skuId"].(string)] = row["available"].(float64)
	}
	if found["SKU-NEW-1"] != 12 || found["SKU-NEW-2"] != 3 {
		t.Fatalf("stock = %v, want SKU-NEW-1 12 and SKU-NEW-2 3", found)
	}
	if _, partial := found["SKU-NEW-3"]; partial {
		t.Fatal("a rejected opening call must apply nothing")
	}

	hist := call(t, srv, http.MethodGet, "/api/v1/admin/inventory/movements?warehouse=WH-HCM-01&skuId=SKU-NEW-1", "", &hcmStaff)
	var histResp struct {
		Data []struct {
			Type, ActorID, Reason string
			Qty                   int64
			At                    string
		} `json:"data"`
		Metadata struct{ Total int64 } `json:"metadata"`
	}
	_ = json.Unmarshal(hist.Body.Bytes(), &histResp)
	if len(histResp.Data) != 1 || histResp.Data[0].Type != "OPENING" || histResp.Data[0].Qty != 12 ||
		histResp.Data[0].ActorID != "mgr-hcm" || histResp.Data[0].Reason != "initial count" || histResp.Data[0].At == "" {
		t.Fatalf("history = %+v, want one OPENING by mgr-hcm with reason and time", histResp.Data)
	}
}

func TestReadEndpointsRequireWarehouseRoleAndScope(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(repository.NewInventoryRepository(nil), false)
	for _, path := range []string{
		"/api/v1/admin/inventory/stock?warehouse=WH-HCM-01",
		"/api/v1/admin/inventory/movements?warehouse=WH-HCM-01",
	} {
		for name, tc := range map[string]struct {
			id   *auth.Identity
			want int
		}{"anonymous": {nil, 401}, "customer": {&customer, 403}, "other warehouse": {&hnStaff, 403}, "own warehouse": {&hcmStaff, 200}} {
			if rec := call(t, srv, http.MethodGet, path, "", tc.id); rec.Code != tc.want {
				t.Errorf("%s %s: %d, want %d", path, name, rec.Code, tc.want)
			}
		}
	}
	if rec := call(t, srv, http.MethodGet, "/api/v1/admin/inventory/stock", "", &hcmStaff); rec.Code != http.StatusBadRequest {
		t.Errorf("missing warehouse: %d, want 400", rec.Code)
	}
	if rec := call(t, srv, http.MethodGet, "/api/v1/admin/inventory/movements?warehouse=WH-HCM-01&limit=1000", "", &hcmStaff); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized limit: %d, want 400", rec.Code)
	}
}

// SC02: every step of the order lifecycle leaves a movement (I06), newest first.
func TestOrderLifecycleWritesMovements_SC02(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(repository.NewInventoryRepository(nil), false)
	call(t, srv, http.MethodPost, "/api/v1/inventory/reservations",
		`{"orderId":"O-LEDGER","warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BOTTLE-BLUE-500ML","quantity":2}]}`, nil)
	call(t, srv, http.MethodPost, "/api/v1/inventory/reservations/O-LEDGER/commit", "", nil)
	call(t, srv, http.MethodPost, "/api/v1/inventory/shipments/handover", `{"orderId":"O-LEDGER"}`, &hcmStaff)

	rec := call(t, srv, http.MethodGet, "/api/v1/admin/inventory/movements?warehouse=WH-HCM-01&skuId=SKU-BOTTLE-BLUE-500ML", "", &hcmStaff)
	var resp struct {
		Data []struct{ Type, RefID, ActorID string } `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	var types []string
	for _, m := range resp.Data {
		types = append(types, m.Type)
	}
	if got := strings.Join(types, ","); got != "HANDOVER,COMMIT,RESERVE,OPENING" {
		t.Fatalf("movement types = %s, want HANDOVER,COMMIT,RESERVE,OPENING", got)
	}
	if resp.Data[0].RefID != "O-LEDGER" || resp.Data[0].ActorID != "system" {
		t.Fatalf("latest movement = %+v", resp.Data[0])
	}
}
