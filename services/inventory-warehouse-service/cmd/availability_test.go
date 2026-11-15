package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

func TestAvailabilityStateBoundaries(t *testing.T) {
	for available, want := range map[int64]string{-1: "OUT", 0: "OUT", 1: "LOW", 3: "LOW", 4: "IN_STOCK", 100: "IN_STOCK"} {
		if got := domain.AvailabilityState(available, 3); got != want {
			t.Errorf("available %d = %s, want %s", available, got, want)
		}
	}
	if got := domain.AvailabilityState(5, 5); got != "LOW" {
		t.Errorf("threshold 5 with 5 available = %s, want LOW", got)
	}
}

func TestAvailabilityIsPublicStateOnlyAndNeverLeaksCounts(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(repository.NewInventoryRepository(nil), false)
	// Seeded memory stock: backpack 10, bottle 20 in HCM; make the bottle LOW and the backpack OUT via reservations.
	call(t, srv, http.MethodPost, "/api/v1/inventory/reservations",
		`{"orderId":"O-AV","warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":10},{"skuId":"SKU-BOTTLE-BLUE-500ML","quantity":18}]}`, nil)

	rec := call(t, srv, http.MethodGet, "/api/v1/inventory/availability?skuIds=SKU-BALO-BLACK-20L,SKU-BOTTLE-BLUE-500ML,SKU-UNKNOWN,SKU-BALO-BLACK-20L", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			WarehouseID string `json:"warehouseId"`
			Items       []map[string]any
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	got := map[string]string{}
	for _, it := range resp.Data.Items {
		if len(it) != 2 {
			t.Fatalf("item %v must carry only skuId and state", it)
		}
		got[it["skuId"].(string)] = it["state"].(string)
	}
	want := map[string]string{"SKU-BALO-BLACK-20L": "OUT", "SKU-BOTTLE-BLUE-500ML": "LOW", "SKU-UNKNOWN": "OUT"}
	if len(resp.Data.Items) != 3 || resp.Data.WarehouseID != "WH-HCM-01" {
		t.Fatalf("response = %s; want 3 deduplicated items for the default warehouse", rec.Body.String())
	}
	for sku, state := range want {
		if got[sku] != state {
			t.Errorf("%s = %s, want %s", sku, got[sku], state)
		}
	}
	for _, leak := range []string{"onHand", "available", "reserved", "quantity", "\"18\"", ":2,"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("body leaks %q: %s", leak, rec.Body.String())
		}
	}
}

func TestAvailabilityHonoursTheWarehouseAndTheConfiguredThreshold(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(repository.NewInventoryRepository(nil), false, WithLowStockThreshold(5))
	// WH-HN-01 holds 5 backpacks in the memory seed: with threshold 5 that is LOW; HCM holds 10: IN_STOCK.
	state := func(wh string) string {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/availability?skuIds=SKU-BALO-BLACK-20L&warehouse="+wh, nil))
		var r struct {
			Data struct{ Items []struct{ State string } }
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return r.Data.Items[0].State
	}
	if got := state("WH-HN-01"); got != "LOW" {
		t.Errorf("HN = %s, want LOW", got)
	}
	if got := state("WH-HCM-01"); got != "IN_STOCK" {
		t.Errorf("HCM = %s, want IN_STOCK", got)
	}
}

func TestAvailabilityRejectsBadInput(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(repository.NewInventoryRepository(nil), false)
	many := make([]string, 101)
	for i := range many {
		many[i] = "S" + string(rune('A'+i%26)) + strings.Repeat("x", i/26)
	}
	for name, q := range map[string]string{
		"missing": "", "blank": "skuIds=,,", "too many": "skuIds=" + strings.Join(many, ","),
		"too long": "skuIds=" + strings.Repeat("a", 65),
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/availability?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
}

func TestExactStockCountsRequireWarehouseStaff(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(repository.NewInventoryRepository(nil), false)
	const path = "/api/v1/inventory/SKU-BALO-BLACK-20L"
	for name, tc := range map[string]struct {
		id   int
		want int
	}{"anonymous": {0, 401}, "customer": {1, 403}, "other warehouse staff": {2, 403}, "own staff": {3, 200}} {
		var rec *httptest.ResponseRecorder
		switch tc.id {
		case 0:
			rec = call(t, srv, http.MethodGet, path, "", nil)
		case 1:
			rec = call(t, srv, http.MethodGet, path, "", &customer)
		case 2:
			rec = call(t, srv, http.MethodGet, path, "", &hnStaff)
		default:
			rec = call(t, srv, http.MethodGet, path, "", &hcmStaff)
		}
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", name, rec.Code, tc.want)
		}
	}
}

func TestLowStockThresholdComesFromTheEnvironment(t *testing.T) {
	t.Setenv("JWT_ACCESS_SECRET", strings.Repeat("a", 32))
	t.Setenv("JWT_REFRESH_SECRET", strings.Repeat("b", 32))
	t.Cleanup(auth.Reset)
	cfg, err := loadConfig()
	if err != nil || cfg.LowStockThreshold != 3 {
		t.Fatalf("default threshold = %d, err %v; want 3", cfg.LowStockThreshold, err)
	}
	t.Setenv("INVENTORY_LOW_STOCK_THRESHOLD", "7")
	if cfg, err = loadConfig(); err != nil || cfg.LowStockThreshold != 7 {
		t.Fatalf("threshold = %d, err %v; want 7", cfg.LowStockThreshold, err)
	}
	t.Setenv("INVENTORY_LOW_STOCK_THRESHOLD", "many")
	if _, err = loadConfig(); err == nil {
		t.Fatal("a non-numeric threshold must be rejected")
	}
}
