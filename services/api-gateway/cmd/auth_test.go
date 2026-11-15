package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/api-gateway/internal/proxy"
)

// upstreamRecorder is a gateway whose upstream answers 200 and records what it received.
type upstreamRecorder struct {
	handler http.Handler
	calls   int
	last    *http.Request
}

func newGateway(t *testing.T) *upstreamRecorder {
	t.Helper()
	authtest.Configure(t)
	u := &upstreamRecorder{}
	rt := proxy.NewRouter(proxy.DefaultTargetsFromEnv())
	rt.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		u.calls++
		u.last = req
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.WriteString(`{"message":"upstream ok"}`)
		return rec.Result(), nil
	})
	u.handler = NewServer(rt, false)
	return u
}

func (u *upstreamRecorder) do(method, path, bearer string, headers map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	u.handler.ServeHTTP(rec, req)
	return rec
}

func tokenFor(t *testing.T, id auth.Identity) string {
	t.Helper()
	tok, err := auth.SignAccessToken(id)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

func TestAnonymousCallsToProtectedEndpointsAreRejectedBeforeReachingAnyService(t *testing.T) {
	gw := newGateway(t)
	protected := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/orders"},
		{http.MethodGet, "/api/v1/orders/O-1"},
		{http.MethodGet, "/api/v1/inventory/SKU-BALO-BLACK-20L"},
		{http.MethodPost, "/api/v1/inventory/reservations"},
		{http.MethodPost, "/api/v1/quotes"},
		{http.MethodPost, "/api/v1/payments/O-1/refund-reservations"},
		{http.MethodPost, "/api/v1/shipments/S-1/handover"},
		{http.MethodPost, "/api/v1/reconciliations/evaluate"},
		{http.MethodGet, "/api/v1/backoffice/exception-cases"},
		{http.MethodGet, "/api/v1/notifications/user-1"},
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/users/me/addresses"},
		{http.MethodGet, "/api/v1/vouchers/WELCOME10/eligibility"},
		{http.MethodGet, "/api/v1/admin/vouchers"},
		{http.MethodGet, "/api/v1/admin/orders"},
		{http.MethodGet, "/api/v1/admin/orders/O-1"},
		{http.MethodGet, "/api/v1/orders"},
		{http.MethodGet, "/api/v1/carts/me"},
		{http.MethodPut, "/api/v1/carts/me/items/SKU-1"},
		{http.MethodPost, "/api/v1/carts/me/merge"},
		{http.MethodGet, "/api/v1/something-unknown"},
		{http.MethodGet, "/api/v1/admin/inventory/stock"},
		{http.MethodGet, "/api/v1/inventory/availabilityx"},     // exact path only
		{http.MethodGet, "/api/v1/inventory/availability/x"},    // exact path only
		{http.MethodPost, "/api/v1/inventory/availability"},     // read-only endpoint
		{http.MethodPost, "/api/v1/products"},                   // writing the catalog is not public
		{http.MethodGet, "/api/v1/productsx"},                   // prefix boundary: not the products collection
		{http.MethodGet, "/api/v1/payments/webhooks/simulator"}, // only the POST webhook is public
		{http.MethodPost, "/api/v1/payments/attempts"},          // internal-only, the gateway needs a token first
		{http.MethodGet, "/api/v1/auth/login"},                  // only POST login is public
		{http.MethodGet, "/api/v1/auth/refresh"},                // only POST refresh is public
	}
	for _, tc := range protected {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := gw.do(tc.method, tc.path, "", nil, "{}")
			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
				t.Fatalf("status = %d, body = %s; want 401", rec.Code, rec.Body.String())
			}
		})
	}
	if gw.calls != 0 {
		t.Fatalf("%d anonymous request(s) reached a downstream service", gw.calls)
	}

	for name, bearer := range map[string]string{"garbage": "not-a-jwt", "refresh token": func() string {
		tok, _ := auth.SignRefreshToken("u", "CUSTOMER", "NONE", "rt-1")
		return tok
	}()} {
		if rec := gw.do(http.MethodGet, "/api/v1/orders/O-1", bearer, nil, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s token: status = %d; want 401", name, rec.Code)
		}
	}
	if gw.calls != 0 {
		t.Fatal("an invalid token must not reach a downstream service")
	}
}

func TestPublicAllowlistReachesTheServicesWithoutAToken(t *testing.T) {
	gw := newGateway(t)
	public := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/products"},
		{http.MethodGet, "/api/v1/products/PROD-BALO-CITY"},
		{http.MethodGet, "/api/v1/categories"},
		{http.MethodGet, "/api/v1/categories/backpacks"},
		{http.MethodGet, "/api/v1/inventory/availability"},
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodPost, "/api/v1/auth/register"},
		{http.MethodPost, "/api/v1/auth/refresh"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/auth/refresh"},
		{http.MethodPost, "/api/v1/payments/webhooks/simulator"},
	}
	for _, tc := range public {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			before := gw.calls
			rec := gw.do(tc.method, tc.path, "", nil, "{}")
			if rec.Code != http.StatusOK || gw.calls != before+1 {
				t.Fatalf("status = %d, body = %s, upstream calls +%d; want 200 and one upstream call", rec.Code, rec.Body.String(), gw.calls-before)
			}
		})
	}
	health := gw.do(http.MethodGet, "/health", "", nil, "")
	if health.Code != http.StatusOK {
		t.Fatalf("/health: %d", health.Code)
	}
}

func TestValidTokenIsForwardedAndForgedIdentityHeadersAreRemoved(t *testing.T) {
	gw := newGateway(t)
	token := tokenFor(t, auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}})

	rec := gw.do(http.MethodGet, "/api/v1/orders/O-1", token, map[string]string{
		"X-User-ID": "admin", "X-User-Role": "ACCESS_ADMIN", "X-Warehouse-Scope": "ALL",
	}, "")
	if rec.Code != http.StatusOK || gw.calls != 1 {
		t.Fatalf("status = %d, calls = %d; want a proxied 200", rec.Code, gw.calls)
	}
	if got := gw.last.Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("the token must reach the service so it can verify it itself, got %q", got)
	}
	for _, h := range []string{"X-User-ID", "X-User-Role", "X-Warehouse-Scope"} {
		if v := gw.last.Header.Get(h); v != "" {
			t.Fatalf("identity header %s = %q must not be forwarded (services read the token)", h, v)
		}
	}
}

func TestClientSentInternalTokenIsNeverForwarded(t *testing.T) {
	gw := newGateway(t)
	token := tokenFor(t, auth.Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}})

	rec := gw.do(http.MethodPost, "/api/v1/inventory/reservations", token, map[string]string{"X-Internal-Token": "guess"}, `{}`)
	if rec.Code != http.StatusOK || gw.calls != 1 {
		t.Fatalf("status = %d, calls = %d; want a proxied 200", rec.Code, gw.calls)
	}
	if v := gw.last.Header.Get("X-Internal-Token"); v != "" {
		t.Fatalf("X-Internal-Token = %q must not reach a service from the outside", v)
	}
}

func TestClientForwardedForIsReplacedByTheRealPeerAddress(t *testing.T) {
	gw := newGateway(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:4444"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	rec := httptest.NewRecorder()
	gw.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || gw.last == nil {
		t.Fatalf("status = %d; the login call must be proxied", rec.Code)
	}
	if got := gw.last.Header.Get("X-Forwarded-For"); got != "203.0.113.7" {
		t.Fatalf("upstream X-Forwarded-For = %q; want only the real peer 203.0.113.7", got)
	}
}
