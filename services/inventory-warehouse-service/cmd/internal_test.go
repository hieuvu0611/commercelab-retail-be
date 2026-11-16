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
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

const testInternalToken = "test-internal-token-0123456789abcdef"

// newTestServer builds the handler and makes every request carry the internal service token, as the order
// service's calls do. Tests of the guard itself call NewServer directly.
func newTestServer(repo *repository.InventoryRepository, pgConnected bool, opts ...ServerOption) http.Handler {
	_ = internalauth.Configure(testInternalToken)
	inner := NewServer(repo, pgConnected, opts...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(internalauth.Header, testInternalToken)
		inner.ServeHTTP(w, r)
	})
}

func TestReservationRoutesAreInternalOnly(t *testing.T) {
	authtest.Configure(t)
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure(testInternalToken)
	srv := NewServer(repository.NewInventoryRepository(nil), false)
	reserve := `{"orderId":"O-GUARD","warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`
	customer := bearerFor(t, auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}})

	routes := []struct{ path, body string }{
		{"/api/v1/inventory/reservations", reserve},
		{"/api/v1/inventory/reservations/O-GUARD/commit", ""},
		{"/api/v1/inventory/reservations/O-GUARD/release", ""},
		{"/api/v1/inventory/reservations/O-GUARD/exception-hold", ""},
		{"/api/v1/inventory/reservations/O-GUARD/release-committed", ""},
	}
	for _, rt := range routes {
		for name, headers := range map[string]map[string]string{
			"anonymous":              {},
			"customer bearer":        {"Authorization": customer},
			"customer, wrong secret": {"Authorization": customer, internalauth.Header: "guess"},
		} {
			req := httptest.NewRequest(http.MethodPost, rt.path, strings.NewReader(rt.body))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "INTERNAL_AUTH_REQUIRED") {
				t.Errorf("%s %s: %d %s; want 401 INTERNAL_AUTH_REQUIRED", name, rt.path, rec.Code, rec.Body.String())
			}
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/reservations", strings.NewReader(reserve))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalauth.Header, testInternalToken)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("internal caller: %d %s; want 201", rec.Code, rec.Body.String())
	}
}

func TestInventoryLoadConfigRequiresTheInternalToken(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_ACCESS_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("JWT_REFRESH_SECRET", "0123456789abcdef0123456789abcdee")
	t.Setenv(internalauth.EnvVar, "")
	auth.Reset()
	internalauth.Reset()
	t.Cleanup(auth.Reset)
	t.Cleanup(internalauth.Reset)
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "INTERNAL_SERVICE_TOKEN is required") {
		t.Fatalf("err = %v, want a missing internal token error", err)
	}
}

func TestReleaseCommittedGivesPaidStockBackOnceAndNeverAfterHandover_B05(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewInventoryRepository(nil)
	srv := newTestServer(repo, false)
	post := func(path, body string, id *auth.Identity) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if id != nil {
			req.Header.Set("Authorization", bearerFor(t, *id))
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	available := func() float64 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/SKU-BALO-BLACK-20L", nil)
		req.Header.Set("Authorization", bearerFor(t, hcmStaff))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var st struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		return st.Data["available"].(float64)
	}
	const release = "/api/v1/inventory/reservations/O-RC/release-committed"
	if rec := post(release, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no reservation = %d", rec.Code)
	}
	post("/api/v1/inventory/reservations", `{"orderId":"O-RC","warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":2}]}`, nil)
	if rec := post(release, "", nil); rec.Code != http.StatusConflict {
		t.Fatalf("a reservation that is not committed = %d, want 409", rec.Code)
	}
	post("/api/v1/inventory/reservations/O-RC/commit", "", nil)
	if available() != 8 {
		t.Fatalf("available after commit = %v, want 8", available())
	}
	for i := 0; i < 3; i++ {
		if rec := post(release, "", nil); rec.Code != http.StatusOK {
			t.Fatalf("release #%d = %d %s", i, rec.Code, rec.Body)
		}
	}
	if available() != 10 {
		t.Fatalf("available after release = %v, want 10 (released once)", available())
	}
	// The released reservation can never be handed over any more (SC06 branch A).
	if rec := post("/api/v1/inventory/shipments/handover", `{"orderId":"O-RC"}`, &hcmStaff); rec.Code != http.StatusConflict {
		t.Fatalf("handover after release = %d, want 409", rec.Code)
	}
	// And a handed over reservation can never be released (SC06 branch B).
	post("/api/v1/inventory/reservations", `{"orderId":"O-RH","warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`, nil)
	post("/api/v1/inventory/reservations/O-RH/commit", "", nil)
	post("/api/v1/inventory/shipments/handover", `{"orderId":"O-RH"}`, &hcmStaff)
	if rec := post("/api/v1/inventory/reservations/O-RH/release-committed", "", nil); rec.Code != http.StatusConflict {
		t.Fatalf("release after handover = %d, want 409", rec.Code)
	}
}

func TestStaffStopReleasesCommittedStockInTheirWarehouseOnly_SC06(t *testing.T) {
	authtest.Configure(t)
	srv := newTestServer(repository.NewInventoryRepository(nil), false)
	post := func(path, body string, id *auth.Identity) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if id != nil {
			req.Header.Set("Authorization", bearerFor(t, *id))
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	post("/api/v1/inventory/reservations", `{"orderId":"O-ST","warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":2}]}`, nil)
	post("/api/v1/inventory/reservations/O-ST/commit", "", nil)
	const stop = "/api/v1/inventory/shipments/stop"
	customer := auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}}
	if rec := post(stop, `{"orderId":"O-ST"}`, &customer); rec.Code != http.StatusForbidden {
		t.Fatalf("customer = %d", rec.Code)
	}
	if rec := post(stop, `{"orderId":"O-ST"}`, &hnStaff); rec.Code != http.StatusForbidden {
		t.Fatalf("other warehouse = %d", rec.Code)
	}
	if rec := post(stop, `{"orderId":"O-ST","warehouseId":"WH-HN-01"}`, &hcmStaff); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "WAREHOUSE_MISMATCH") {
		t.Fatalf("mismatch = %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 2; i++ {
		if rec := post(stop, `{"orderId":"O-ST","warehouseId":"WH-HCM-01"}`, &hcmStaff); rec.Code != http.StatusOK {
			t.Fatalf("stop #%d = %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := post("/api/v1/inventory/shipments/handover", `{"orderId":"O-ST"}`, &hcmStaff); rec.Code != http.StatusConflict {
		t.Fatalf("handover after stop = %d, want 409", rec.Code)
	}
	if rec := post(stop, `{"orderId":"O-NONE"}`, &hcmStaff); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown order = %d", rec.Code)
	}
}

func TestReturnReceiveAndQC_SC07(t *testing.T) {
	authtest.Configure(t)
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure(testInternalToken)
	repo := repository.NewInventoryRepository(nil)
	srv := newTestServer(repo, false)

	rec := httptest.NewRecorder()
	body := `{"warehouseId":"WH-HCM-01","skuId":"SKU-BALO-BLACK-20L","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/returns/receive", strings.NewReader(body))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("receive return = %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data struct {
			OnHand     int64 `json:"onHand"`
			Unsellable int64 `json:"unsellable"`
			Available  int64 `json:"available"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Data.OnHand != 11 || res.Data.Unsellable != 1 || res.Data.Available != 10 {
		t.Fatalf("unexpected stock after receive return: %+v, want onHand=11, unsellable=1, available=10", res.Data)
	}

	recQc := httptest.NewRecorder()
	reqQc := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/returns/qc-pass", strings.NewReader(body))
	srv.ServeHTTP(recQc, reqQc)
	if recQc.Code != http.StatusOK {
		t.Fatalf("qc pass = %d %s", recQc.Code, recQc.Body.String())
	}
	_ = json.Unmarshal(recQc.Body.Bytes(), &res)
	if res.Data.OnHand != 11 || res.Data.Unsellable != 0 || res.Data.Available != 11 {
		t.Fatalf("unexpected stock after qc pass: %+v, want onHand=11, unsellable=0, available=11", res.Data)
	}
}
