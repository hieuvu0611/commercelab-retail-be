package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
)

func b64url(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestNegative_JWTAlgorithmDowngradeAndTamperingRejected(t *testing.T) {
	gw := newGateway(t)

	genuine := tokenFor(t, auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}})
	parts := strings.Split(genuine, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT segments, got %d", len(parts))
	}

	algNone := b64url(map[string]string{"alg": "none", "typ": "JWT"}) + "." +
		b64url(map[string]any{"userId": "staff-admin", "roles": []string{"ACCESS_ADMIN"}, "tokenType": "ACCESS"}) + "."
	algRS256 := b64url(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + parts[1] + "." + parts[2]
	tamperedPayload := parts[0] + "." +
		b64url(map[string]any{"userId": "user-lan", "roles": []string{"ACCESS_ADMIN"}, "warehouseScopes": []string{"ALL"}, "tokenType": "ACCESS"}) +
		"." + parts[2]

	expiredTok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		UserID: "staff-admin", Roles: []string{"ACCESS_ADMIN"}, TokenType: "ACCESS",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))},
	}).SignedString([]byte("another-secret-never-valid-for-gateway"))
	if err != nil {
		t.Fatalf("sign expired wrong secret: %v", err)
	}
	wrongSecret, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		UserID: "staff-admin", Roles: []string{"ACCESS_ADMIN"}, TokenType: "ACCESS",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString([]byte("attacker-controlled-secret-key-material"))
	if err != nil {
		t.Fatalf("sign wrong secret: %v", err)
	}
	refreshTok, err := auth.SignRefreshToken("staff-admin", "ACCESS_ADMIN", "ALL", "rt-neg-1")
	if err != nil {
		t.Fatalf("sign refresh: %v", err)
	}

	cases := []struct {
		name   string
		header string
	}{
		{"alg none unsigned", "Bearer " + algNone},
		{"alg RS256 downgrade", "Bearer " + algRS256},
		{"tampered admin payload with genuine sig", "Bearer " + tamperedPayload},
		{"expired token", "Bearer " + expiredTok},
		{"wrong HMAC secret", "Bearer " + wrongSecret},
		{"refresh token presented as access", "Bearer " + refreshTok},
		{"basic auth scheme", "Basic YWRtaW46cGFzc3dvcmQ="},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/pos/sales", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", tc.header)
			rec := httptest.NewRecorder()
			gw.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: status = %d, body = %s; want 401", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
	if gw.calls != 0 {
		t.Fatalf("expected 0 upstream calls for invalid/forged tokens, got %d", gw.calls)
	}
}

func TestNegative_SensitiveAdminPOSAndInternalRoutesFailClosed(t *testing.T) {
	gw := newGateway(t)

	blocked := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/admin/pos/sales"},
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodGet, "/api/v1/backoffice/metrics?period=TODAY"},
		{http.MethodPost, "/api/v1/inventory/internal/shipments/handover"},
		{http.MethodPost, "/api/v1/shipments/internal/orders/O-1/stop"},
		{http.MethodPost, "/api/v1/admin/ledger/settlements/import"},
		{http.MethodPost, "/api/v1/backoffice/approvals/APP-1/decide"},
		{http.MethodPost, "/api/v1/products"},
		{http.MethodDelete, "/api/v1/categories/backpacks"},
		{http.MethodGet, "/api/v1/payments/webhooks/simulator"},
		{http.MethodGet, "/api/v1/inventory/availability/stock"},
	}

	for _, tc := range blocked {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := gw.do(tc.method, tc.path, "", map[string]string{
				"X-Internal-Token":  "forged-secret",
				"X-User-ID":         "staff-admin",
				"X-User-Role":       "ACCESS_ADMIN",
				"X-Warehouse-Scope": "ALL",
			}, `{}`)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s: status = %d, body = %s; want 401", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
	if gw.calls != 0 {
		t.Fatalf("expected 0 upstream calls for unauthenticated sensitive endpoints, got %d", gw.calls)
	}
}

func TestNegative_ForgedTrustHeadersStrippedOnPublicAndAuthenticatedPaths(t *testing.T) {
	t.Run("public auth login strips spoofed headers", func(t *testing.T) {
		gw := newGateway(t)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
		req.RemoteAddr = "198.51.100.42:51500"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", "spoofed-internal-secret")
		req.Header.Set("X-User-ID", "staff-ops")
		req.Header.Set("X-User-Role", "OPS_MGR")
		req.Header.Set("X-Warehouse-Scope", "ALL")
		req.Header.Set("X-Forwarded-For", "127.0.0.1, 10.0.0.9")

		rec := httptest.NewRecorder()
		gw.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || gw.last == nil {
			t.Fatalf("status = %d; expected proxied login call", rec.Code)
		}
		for _, hdr := range []string{"X-Internal-Token", "X-User-ID", "X-User-Role", "X-Warehouse-Scope"} {
			if got := gw.last.Header.Get(hdr); got != "" {
				t.Fatalf("expected %s to be stripped, got %q", hdr, got)
			}
		}
		if got := gw.last.Header.Get("X-Forwarded-For"); got != "198.51.100.42" {
			t.Fatalf("expected client X-Forwarded-For to be overwritten by 198.51.100.42, got %q", got)
		}
	})

	t.Run("authenticated POS route strips spoofed internal and privilege headers", func(t *testing.T) {
		gw := newGateway(t)
		custTok := tokenFor(t, auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}})
		rec := gw.do(http.MethodPost, "/api/v1/admin/pos/sales", custTok, map[string]string{
			"X-Internal-Token":  "spoofed-internal-secret",
			"X-User-ID":         "staff-admin",
			"X-User-Role":       "ACCESS_ADMIN",
			"X-Warehouse-Scope": "ALL",
		}, `{}`)
		if rec.Code != http.StatusOK || gw.last == nil {
			t.Fatalf("status = %d; expected gateway forward for upstream RBAC verification", rec.Code)
		}
		for _, hdr := range []string{"X-Internal-Token", "X-User-ID", "X-User-Role", "X-Warehouse-Scope"} {
			if got := gw.last.Header.Get(hdr); got != "" {
				t.Fatalf("expected %s to be stripped on authenticated path, got %q", hdr, got)
			}
		}
	})
}
