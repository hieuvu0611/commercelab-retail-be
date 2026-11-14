package main

import (
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

func TestGatewayRoutesAnAuthenticatedRequestToTheInventoryService(t *testing.T) {
	gw := newGateway(t)
	token := tokenFor(t, auth.Identity{UserID: "staff-wh-hcm", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}})

	rec := gw.do(http.MethodGet, "/api/v1/inventory/SKU-BALO-BLACK-20L", token, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from proxied inventory service, got %d: %s", rec.Code, rec.Body.String())
	}
	if gw.last.URL.Host != "localhost:8082" {
		t.Fatalf("expected routing to localhost:8082, got %s", gw.last.URL.Host)
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
			req := httptest.NewRequest(tc.method, "/api/v1/carts/me", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tokenFor(t, auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}}))
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
	if _, signErr := auth.SignAccessToken(auth.Identity{UserID: "u", Role: "CUSTOMER", WarehouseScope: "NONE"}); signErr == nil {
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
	if _, err := auth.SignAccessToken(auth.Identity{UserID: "u", Role: "CUSTOMER", WarehouseScope: "NONE"}); err != nil {
		t.Fatalf("tokens must work after a valid configuration: %v", err)
	}
}

func TestProxiedResponseHasExactlyOneSetOfCORSHeaders(t *testing.T) {
	authtest.Configure(t)
	rt := proxy.NewRouter(proxy.DefaultTargetsFromEnv())
	rt.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// The downstream service adds its own CORS headers, as every Go service does through middleware.Chain.
		w := httptest.NewRecorder()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return w.Result(), nil
	})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/products", nil)
	w := httptest.NewRecorder()
	NewServer(NewCartStorage(nil), rt, false).ServeHTTP(w, r)

	for _, header := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
		if values := w.Header().Values(header); len(values) != 1 {
			t.Fatalf("%s has %d values %q; a browser rejects a response with more than one", header, len(values), values)
		}
	}
}
