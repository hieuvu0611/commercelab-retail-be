package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
)

func TestReindexRequiresCatalogOrOperationsRole(t *testing.T) {
	authtest.Configure(t)
	mux := http.NewServeMux()
	registerSearchAdminRoutes(mux, nil, nil, nil)
	for _, tc := range []struct {
		role string
		code int
	}{
		{"ACCESS_ADMIN", http.StatusForbidden},
		{"PRODUCT_STAFF", http.StatusServiceUnavailable},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/search/reindex", nil)
		req.Header.Set("Authorization", "Bearer "+token(t, tc.role))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Errorf("%s: got %d, want %d", tc.role, rec.Code, tc.code)
		}
	}
}
