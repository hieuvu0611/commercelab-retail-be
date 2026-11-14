package main

import (
	"encoding/json"
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
	u.handler = NewServer(NewCartStorage(nil), rt, false)
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
		{http.MethodGet, "/api/v1/carts/me"},
		{http.MethodPut, "/api/v1/carts/me"},
		{http.MethodGet, "/api/v1/something-unknown"},
		{http.MethodPost, "/api/v1/products"},    // writing the catalog is not public
		{http.MethodGet, "/api/v1/productsx"},    // prefix boundary: not the products collection
		{http.MethodGet, "/api/v1/auth/login"},   // only POST login is public
		{http.MethodGet, "/api/v1/auth/refresh"}, // only POST refresh is public
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
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodPost, "/api/v1/auth/register"},
		{http.MethodPost, "/api/v1/auth/refresh"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/auth/refresh"},
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

func TestCartBelongsToTheTokenSubject(t *testing.T) {
	gw := newGateway(t)
	alice := tokenFor(t, auth.Identity{UserID: "user-alice", Roles: []string{"CUSTOMER"}})
	bob := tokenFor(t, auth.Identity{UserID: "user-bob", Roles: []string{"CUSTOMER"}})

	if rec := gw.do(http.MethodPut, "/api/v1/carts/me", alice, nil, `{"items":[{"skuId":"SKU-BALO-BLACK-20L","quantity":3}]}`); rec.Code != http.StatusOK {
		t.Fatalf("alice PUT: %d %s", rec.Code, rec.Body.String())
	}

	read := func(bearer string) (Cart, int) {
		rec := gw.do(http.MethodGet, "/api/v1/carts/me", bearer, nil, "")
		var resp struct {
			Data Cart `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp.Data, rec.Code
	}
	cart, code := read(alice)
	if code != http.StatusOK || cart.UserID != "user-alice" || len(cart.Items) != 1 || cart.Items[0].Quantity != 3 {
		t.Fatalf("alice's cart = %+v (status %d)", cart, code)
	}
	cart, code = read(bob)
	if code != http.StatusOK || cart.UserID != "user-bob" || len(cart.Items) != 0 {
		t.Fatalf("bob must see his own empty cart, got %+v (status %d)", cart, code)
	}

	// The cart can no longer be addressed by someone else's user id.
	if rec := gw.do(http.MethodGet, "/api/v1/carts/user-alice", bob, nil, ""); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "user-alice") {
		t.Fatalf("bob read alice's cart through her user id: %s", rec.Body.String())
	}
	// A client-supplied userId in the body is ignored: the cart is always the token subject's.
	rec := gw.do(http.MethodPut, "/api/v1/carts/me", bob, nil, `{"userId":"user-alice","items":[{"skuId":"SKU-BOTTLE-BLUE-500ML","quantity":1}]}`)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusOK {
		t.Fatalf("PUT with a userId in the body: %d %s", rec.Code, rec.Body.String())
	}
	if aliceCart, _ := read(alice); len(aliceCart.Items) != 1 || aliceCart.Items[0].SkuID != "SKU-BALO-BLACK-20L" {
		t.Fatalf("bob's write changed alice's cart: %+v", aliceCart)
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
