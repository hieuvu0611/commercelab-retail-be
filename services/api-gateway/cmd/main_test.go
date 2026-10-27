package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/auth"
	"github.com/hieuvu0211/full-service-shopping/be/services/api-gateway/internal/proxy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGatewayCartAndAuthenticatedReverseProxy(t *testing.T) {
	var capturedUserID, capturedRole, capturedWarehouseScope, capturedHost string

	rt := proxy.NewRouter(proxy.DefaultTargetsFromEnv())
	rt.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedUserID = req.Header.Get("X-User-ID")
		capturedRole = req.Header.Get("X-User-Role")
		capturedWarehouseScope = req.Header.Get("X-Warehouse-Scope")
		capturedHost = req.URL.Host

		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.WriteString(`{"message":"downstream ok","data":{"skuId":"SKU-BALO-BLACK-20L"}}`)
		return rec.Result(), nil
	})

	gw := NewServer(NewCartStorage(nil), rt, false)

	// 1. Kiểm tra cập nhật và lấy giỏ hàng trên Gateway (P02)
	putCartBody := []byte(`{"items":[{"skuId":"SKU-BALO-BLACK-20L","quantity":3}]}`)
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/carts/user-lan", bytes.NewReader(putCartBody))
	putRec := httptest.NewRecorder()
	gw.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on PUT cart, got %d", putRec.Code)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/carts/user-lan", nil)
	getRec := httptest.NewRecorder()
	gw.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET cart, got %d", getRec.Code)
	}

	// 2. Kiểm tra Reverse Proxy kèm chống giả mạo header X-Warehouse-Scope và tiêm từ JWT thật (I08)
	token, err := auth.SignAccessToken("staff-wh-hcm", "WAREHOUSE_STAFF", "WH-HCM-01")
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	invReq := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/SKU-BALO-BLACK-20L", nil)
	invReq.Header.Set("Authorization", "Bearer "+token)
	invReq.Header.Set("X-Warehouse-Scope", "ALL") // Client cố tình giả mạo ALL
	invRec := httptest.NewRecorder()
	gw.ServeHTTP(invRec, invReq)

	if invRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from proxied inventory service, got %d: %s", invRec.Code, invRec.Body.String())
	}
	if capturedHost != "localhost:8082" {
		t.Fatalf("expected routing to localhost:8082, got %s", capturedHost)
	}
	if capturedUserID != "staff-wh-hcm" || capturedRole != "WAREHOUSE_STAFF" || capturedWarehouseScope != "WH-HCM-01" {
		t.Fatalf("expected gateway to overwrite headers from JWT, got user=%s role=%s scope=%s",
			capturedUserID, capturedRole, capturedWarehouseScope)
	}

	var resp map[string]any
	if err := json.Unmarshal(invRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal proxy response: %v", err)
	}
}
