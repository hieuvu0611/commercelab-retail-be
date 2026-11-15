package main

import (
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
