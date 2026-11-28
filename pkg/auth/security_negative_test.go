package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestNegative_JWTRejectsAlgNoneAndTamperedPayload(t *testing.T) {
	configureForTest(t)

	tok := accessToken(t, Identity{UserID: "cust-1", Roles: []string{"CUSTOMER"}})
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed test JWT")
	}

	noneHdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	adminPayloadBytes, _ := json.Marshal(Claims{
		UserID:          "cust-1",
		Roles:           []string{"ACCESS_ADMIN"},
		WarehouseScopes: []string{AllWarehouses},
		TokenType:       "ACCESS",
	})
	adminPayload := base64.RawURLEncoding.EncodeToString(adminPayloadBytes)

	if _, err := ParseAccessToken(noneHdr + "." + adminPayload + "."); err == nil {
		t.Fatal("ParseAccessToken accepted unsigned alg:none JWT")
	}
	if _, err := ParseAccessToken(parts[0] + "." + adminPayload + "." + parts[2]); err == nil {
		t.Fatal("ParseAccessToken accepted tampered payload with original signature")
	}
	rsHdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	if _, err := ParseAccessToken(rsHdr + "." + parts[1] + "." + parts[2]); err == nil {
		t.Fatal("ParseAccessToken accepted non-HMAC signing algorithm")
	}
}

func TestNegative_RequireAuthFailsClosedWhenUnconfigured(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	rec := call(RequireAuth(ok), "Bearer some.jwt.token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when JWT secrets are unconfigured, got %d", rec.Code)
	}
}

func TestNegative_CrossRoleAndWarehousePrivilegeEscalationBlocked(t *testing.T) {
	configureForTest(t)
	fromParam := func(r *http.Request) string { return r.URL.Query().Get("warehouse") }
	posHandler := RequireAuth(RequireRole("CASHIER", "WAREHOUSE_STAFF", "OPS_MGR")(RequireWarehouseScope(fromParam)(ok)))

	custTok := accessToken(t, Identity{UserID: "user-lan", Roles: []string{"CUSTOMER"}, WarehouseScopes: []string{AllWarehouses}})
	hcmCashierTok := accessToken(t, Identity{UserID: "staff-cashier-hcm", Roles: []string{"CASHIER"}, WarehouseScopes: []string{"WH-HCM-01"}})
	legacyOnlyTok := accessToken(t, Identity{UserID: "legacy-1", Role: "CASHIER", WarehouseScope: "WH-HCM-01"})

	t.Run("customer denied even with forged ALL warehouse scope claim", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/pos?warehouse=WH-HCM-01", nil)
		req.Header.Set("Authorization", "Bearer "+custTok)
		rec := httptest.NewRecorder()
		posHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d; want 403", rec.Code)
		}
	})

	t.Run("HCM cashier denied access to HN warehouse", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/pos?warehouse=WH-HN-01", nil)
		req.Header.Set("Authorization", "Bearer "+hcmCashierTok)
		rec := httptest.NewRecorder()
		posHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "FORBIDDEN_WAREHOUSE_SCOPE") {
			t.Fatalf("got %d (%s); want 403 FORBIDDEN_WAREHOUSE_SCOPE", rec.Code, rec.Body.String())
		}
	})

	t.Run("HCM cashier denied empty warehouse target", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/pos", nil)
		req.Header.Set("Authorization", "Bearer "+hcmCashierTok)
		rec := httptest.NewRecorder()
		posHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d; want 403", rec.Code)
		}
	})

	t.Run("legacy singular claims rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/pos?warehouse=WH-HCM-01", nil)
		req.Header.Set("Authorization", "Bearer "+legacyOnlyTok)
		rec := httptest.NewRecorder()
		posHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("got %d; want 403", rec.Code)
		}
	})

	_ = jwt.ErrTokenSignatureInvalid
}
