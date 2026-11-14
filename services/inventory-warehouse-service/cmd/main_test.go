package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bearerFor signs an access token for tests.
func bearerFor(t *testing.T, id auth.Identity) string {
	t.Helper()
	token, err := auth.SignAccessToken(id)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return "Bearer " + token
}

var (
	hcmStaff = auth.Identity{UserID: "staff-wh-hcm", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}}
	hnStaff  = auth.Identity{UserID: "staff-wh-hn", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HN-01"}}
)

func TestInventoryReserveCommitHandoverAndWarehouseScope_SC02_SC12(t *testing.T) {
	authtest.Configure(t)
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
	badHandoverReq.Header.Set("Authorization", bearerFor(t, hnStaff))
	badHandoverRec := httptest.NewRecorder()
	srv.ServeHTTP(badHandoverRec, badHandoverReq)
	if badHandoverRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for wrong warehouse scope (SC12), got %d", badHandoverRec.Code)
	}

	// 4. Nhân viên Kho HCM (X-Warehouse-Scope: WH-HCM-01) bàn giao hợp lệ -> OnHand giảm từ 10 xuống 8, Available vẫn là 8
	okHandoverReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/shipments/handover", bytes.NewReader([]byte(`{"orderId":"O-001"}`)))
	okHandoverReq.Header.Set("Authorization", bearerFor(t, hcmStaff))
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
	authtest.Configure(t)
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
			req.Header.Set("Authorization", bearerFor(t, hcmStaff))
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

func reserveOrderForHandover(t *testing.T, srv http.Handler) {
	t.Helper()
	body := []byte(`{"orderId":"O-AUTH","warehouseId":"WH-HCM-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`)
	for _, step := range []struct {
		method, path string
		body         []byte
	}{
		{http.MethodPost, "/api/v1/inventory/reservations", body},
		{http.MethodPost, "/api/v1/inventory/reservations/O-AUTH/commit", nil},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(step.method, step.path, bytes.NewReader(step.body)))
		if rec.Code >= 300 {
			t.Fatalf("setup %s: %d %s", step.path, rec.Code, rec.Body.String())
		}
	}
}

func handoverWith(srv http.Handler, authorization, extraHeader, extraValue string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/shipments/handover", bytes.NewReader([]byte(`{"orderId":"O-AUTH"}`)))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if extraHeader != "" {
		req.Header.Set(extraHeader, extraValue)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHandoverRequiresAValidTokenRoleAndTheReservationsWarehouse(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(repository.NewInventoryRepository(nil), false)
	reserveOrderForHandover(t, srv)

	customer := auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}}
	allMgr := auth.Identity{UserID: "mgr", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"ALL"}}

	tests := []struct {
		name          string
		authorization string
		header        [2]string
		want          int
	}{
		{"no token", "", [2]string{}, http.StatusUnauthorized},
		{"forged scope headers are ignored", "", [2]string{"X-Warehouse-Scope", "ALL"}, http.StatusUnauthorized},
		{"garbage token", "Bearer nope", [2]string{}, http.StatusUnauthorized},
		{"customer role", bearerFor(t, customer), [2]string{}, http.StatusForbidden},
		{"other warehouse's staff, even with a forged ALL header", bearerFor(t, hnStaff), [2]string{"X-Warehouse-Scope", "ALL"}, http.StatusForbidden},
		{"legacy scope claim alone", bearerFor(t, auth.Identity{UserID: "x", Role: "WAREHOUSE_STAFF", WarehouseScope: "ALL", Roles: []string{"WAREHOUSE_STAFF"}}), [2]string{}, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := handoverWith(srv, tc.authorization, tc.header[0], tc.header[1])
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s; want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}

	// A manager whose scope covers every warehouse may hand over.
	if rec := handoverWith(srv, bearerFor(t, allMgr), "", ""); rec.Code != http.StatusOK {
		t.Fatalf("manager with scope ALL: %d %s; want 200", rec.Code, rec.Body.String())
	}
}

func TestHandoverUsesTheReservationsWarehouseNotAHardCodedOne(t *testing.T) {
	authtest.Configure(t)
	srv := NewServer(repository.NewInventoryRepository(nil), false)
	body := []byte(`{"orderId":"O-HN","warehouseId":"WH-HN-01","lines":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`)
	for _, step := range []struct {
		path string
		body []byte
	}{
		{"/api/v1/inventory/reservations", body},
		{"/api/v1/inventory/reservations/O-HN/commit", nil},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, step.path, bytes.NewReader(step.body)))
		if rec.Code >= 300 {
			t.Fatalf("setup %s: %d %s", step.path, rec.Code, rec.Body.String())
		}
	}
	handover := func(id auth.Identity) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/shipments/handover", bytes.NewReader([]byte(`{"orderId":"O-HN"}`)))
		req.Header.Set("Authorization", bearerFor(t, id))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := handover(hcmStaff); got != http.StatusForbidden {
		t.Fatalf("HCM staff on an HN reservation: %d; want 403", got)
	}
	if got := handover(hnStaff); got != http.StatusOK {
		t.Fatalf("HN staff on an HN reservation: %d; want 200", got)
	}
}

func TestInventoryLoadConfigRequiresJWTSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_ACCESS_SECRET", "")
	t.Setenv("JWT_REFRESH_SECRET", "")
	auth.Reset()
	t.Cleanup(auth.Reset)
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "JWT_ACCESS_SECRET is required") {
		t.Fatalf("err = %v, want a missing JWT secret error", err)
	}
}
