package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func configureForTest(t *testing.T) {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
	if err := Configure(Secrets{Access: "access-secret-0123456789abcdef-test", Refresh: "refresh-secret-0123456789abcdef-test"}); err != nil {
		t.Fatal(err)
	}
}

func accessToken(t *testing.T, id Identity) string {
	t.Helper()
	tok, err := SignAccessToken(id)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

// expiredAccessToken is correctly signed but expired an hour ago.
func expiredAccessToken(t *testing.T) string {
	t.Helper()
	secrets, _ := currentSecrets()
	claims := Claims{
		UserID: "u", Roles: []string{"WAREHOUSE_STAFF"}, TokenType: "ACCESS",
		RegisteredClaims: jwt.RegisteredClaims{Subject: "u", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secrets.Access))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func call(h http.Handler, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x?warehouse=WH-HCM-01", nil)
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var ok = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	claims, found := ClaimsFrom(r.Context())
	if !found {
		http.Error(w, "no claims in context", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte(claims.UserID))
})

func TestRequireAuthFailsClosed(t *testing.T) {
	configureForTest(t)
	h := RequireAuth(ok)
	good := accessToken(t, Identity{UserID: "user-1", Roles: []string{"CUSTOMER"}})
	refresh, _ := SignRefreshToken("user-1", "CUSTOMER", "NONE", "rt-1")

	tests := []struct {
		name   string
		bearer string
		want   int
		code   string
	}{
		{"no header", "", http.StatusUnauthorized, "UNAUTHORIZED"},
		{"not a bearer scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized, "UNAUTHORIZED"},
		{"empty bearer", "Bearer ", http.StatusUnauthorized, "UNAUTHORIZED"},
		{"garbage token", "Bearer not-a-jwt", http.StatusUnauthorized, "INVALID_ACCESS_TOKEN"},
		{"expired token", "Bearer " + expiredAccessToken(t), http.StatusUnauthorized, "INVALID_ACCESS_TOKEN"},
		{"refresh token as access token", "Bearer " + refresh, http.StatusUnauthorized, "INVALID_ACCESS_TOKEN"},
		{"valid token", "Bearer " + good, http.StatusOK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(h, tc.bearer)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s; want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				if !strings.Contains(rec.Body.String(), tc.code) {
					t.Fatalf("body %s must contain %s", rec.Body.String(), tc.code)
				}
				if rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatalf("401 must carry WWW-Authenticate: Bearer, got %q", rec.Header().Get("WWW-Authenticate"))
				}
			}
		})
	}
	if rec := call(h, "Bearer "+good); rec.Body.String() != "user-1" {
		t.Fatalf("handler must see the claims of the token, got %q", rec.Body.String())
	}
}

func TestRequireAuthRejectsATokenSignedWithAnotherSecret(t *testing.T) {
	configureForTest(t)
	tok := accessToken(t, Identity{UserID: "u", Roles: []string{"CUSTOMER"}})
	if err := Configure(Secrets{Access: "another-access-secret-0123456789abcd", Refresh: "refresh-secret-0123456789abcdef-test"}); err != nil {
		t.Fatal(err)
	}
	if rec := call(RequireAuth(ok), "Bearer "+tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", rec.Code)
	}
}

func TestRequireRole(t *testing.T) {
	configureForTest(t)
	h := RequireAuth(RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(ok))

	staff := accessToken(t, Identity{UserID: "s", Roles: []string{"WAREHOUSE_STAFF"}})
	multi := accessToken(t, Identity{UserID: "m", Roles: []string{"SUPPORT_AGENT", "WAREHOUSE_MGR"}})
	customer := accessToken(t, Identity{UserID: "c", Roles: []string{"CUSTOMER"}})
	legacyOnly := accessToken(t, Identity{UserID: "l", Role: "WAREHOUSE_STAFF"}) // legacy claim alone is not enough

	for name, tc := range map[string]struct {
		bearer string
		want   int
	}{
		"matching role":          {staff, http.StatusOK},
		"one of several roles":   {multi, http.StatusOK},
		"wrong role":             {customer, http.StatusForbidden},
		"legacy role claim only": {legacyOnly, http.StatusForbidden},
		"anonymous":              {"", http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			bearer := ""
			if tc.bearer != "" {
				bearer = "Bearer " + tc.bearer
			}
			rec := call(h, bearer)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s; want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "FORBIDDEN") {
				t.Fatalf("403 body must carry FORBIDDEN: %s", rec.Body.String())
			}
		})
	}
}

func TestRequireRoleWithoutAuthenticationIs401NotAPanic(t *testing.T) {
	configureForTest(t)
	rec := call(RequireRole("WAREHOUSE_STAFF")(ok), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 when RequireAuth was not applied first", rec.Code)
	}
}

func TestCanAccessWarehouse(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		target string
		want   bool
	}{
		{"own warehouse", []string{"WH-HCM-01"}, "WH-HCM-01", true},
		{"other warehouse", []string{"WH-HCM-01"}, "WH-HN-01", false},
		{"every warehouse", []string{"ALL"}, "WH-HN-01", true},
		{"one of several", []string{"WH-HCM-01", "WH-HN-01"}, "WH-HN-01", true},
		{"no scopes", nil, "WH-HCM-01", false},
		{"empty target fails closed", []string{"ALL"}, "", false},
		{"nil claims", nil, "WH-HCM-01", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c *Claims
			if tc.name != "nil claims" {
				c = &Claims{WarehouseScopes: tc.scopes}
			}
			if got := CanAccessWarehouse(c, tc.target); got != tc.want {
				t.Fatalf("CanAccessWarehouse = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequireWarehouseScope(t *testing.T) {
	configureForTest(t)
	fromQuery := func(r *http.Request) string { return r.URL.Query().Get("warehouse") }
	h := RequireAuth(RequireWarehouseScope(fromQuery)(ok))

	hcm := accessToken(t, Identity{UserID: "hcm", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HCM-01"}})
	hn := accessToken(t, Identity{UserID: "hn", Roles: []string{"WAREHOUSE_STAFF"}, WarehouseScopes: []string{"WH-HN-01"}})
	all := accessToken(t, Identity{UserID: "all", Roles: []string{"WAREHOUSE_MGR"}, WarehouseScopes: []string{"ALL"}})
	legacyAll := accessToken(t, Identity{UserID: "x", Role: "WAREHOUSE_STAFF", WarehouseScope: "ALL"}) // legacy claim alone is not enough

	for name, tc := range map[string]struct {
		bearer string
		want   int
	}{
		"own warehouse":           {hcm, http.StatusOK},
		"other warehouse":         {hn, http.StatusForbidden},
		"all warehouses":          {all, http.StatusOK},
		"legacy scope claim only": {legacyAll, http.StatusForbidden},
		"anonymous":               {"", http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			bearer := ""
			if tc.bearer != "" {
				bearer = "Bearer " + tc.bearer
			}
			rec := call(h, bearer)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, body = %s; want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "FORBIDDEN_WAREHOUSE_SCOPE") {
				t.Fatalf("403 body must carry FORBIDDEN_WAREHOUSE_SCOPE: %s", rec.Body.String())
			}
		})
	}

	noWarehouse := RequireAuth(RequireWarehouseScope(func(*http.Request) string { return "" })(ok))
	if rec := call(noWarehouse, "Bearer "+all); rec.Code != http.StatusForbidden {
		t.Fatalf("a request that names no warehouse must fail closed, got %d", rec.Code)
	}
}
