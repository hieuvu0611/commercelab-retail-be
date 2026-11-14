package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/api-gateway/internal/proxy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestProxiedResponseContainsOnlyGatewayCorrelationID(t *testing.T) {
	authtest.Configure(t)
	for _, upstreamID := range []string{"req-test", "different-id", ""} {
		t.Run("upstream_"+upstreamID, func(t *testing.T) {
			rt := proxy.NewRouter(proxy.DefaultTargetsFromEnv())
			rt.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("X-Correlation-ID"); got != "req-test" {
					t.Errorf("forwarded correlation ID = %q", got)
				}
				w := httptest.NewRecorder()
				if upstreamID != "" {
					w.Header().Set("X-Correlation-ID", upstreamID)
				}
				w.WriteHeader(http.StatusOK)
				return w.Result(), nil
			})
			r := httptest.NewRequest(http.MethodGet, "/api/v1/products", nil)
			r.Header.Set("X-Correlation-ID", "req-test")
			w := httptest.NewRecorder()
			NewServer(NewCartStorage(nil), rt, false).ServeHTTP(w, r)
			values := w.Header().Values("X-Correlation-ID")
			if len(values) != 1 || values[0] != "req-test" {
				t.Fatalf("unexpected correlation IDs: %q", values)
			}
		})
	}
}

func TestGatewayCartAndAuthenticatedReverseProxy(t *testing.T) {
	authtest.Configure(t)
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

func TestCartStoreOutageAnswers503(t *testing.T) {
	authtest.Configure(t)
	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Second, MaxRetries: -1})
	t.Cleanup(func() { _ = redisClient.Close() })
	srv := NewServer(NewCartStorage(redisClient), proxy.NewRouter(proxy.ServiceTargets{}), true)

	tests := []struct {
		name   string
		method string
		body   string
	}{
		{name: "read cart", method: http.MethodGet},
		{name: "write cart", method: http.MethodPut, body: `{"items":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1/carts/user-lan", strings.NewReader(tc.body))
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

func TestLoadConfigFailsWithoutJWTSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_ACCESS_SECRET", "")
	t.Setenv("JWT_REFRESH_SECRET", "short")
	auth.Reset()
	t.Cleanup(auth.Reset)

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected a configuration error")
	}
	for _, want := range []string{"JWT_ACCESS_SECRET is required", "JWT_REFRESH_SECRET must be at least 32 bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if _, signErr := auth.SignAccessToken("u", "CUSTOMER", "NONE"); signErr == nil {
		t.Fatal("tokens must not work when the configuration is invalid")
	}
}

func TestLoadConfigAcceptsStrongSecrets(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_ACCESS_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("JWT_REFRESH_SECRET", "fedcba9876543210fedcba9876543210")
	auth.Reset()
	t.Cleanup(auth.Reset)

	if _, err := loadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := auth.SignAccessToken("u", "CUSTOMER", "NONE"); err != nil {
		t.Fatalf("tokens must work after a valid configuration: %v", err)
	}
}
