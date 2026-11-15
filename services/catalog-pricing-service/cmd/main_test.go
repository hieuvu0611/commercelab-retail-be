package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cart"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogProductsAreListedWithoutAToken(t *testing.T) {
	srv := NewServer(repository.NewCatalogPricingRepository(nil, nil), false, false)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/v1/products, got %d", rec.Code)
	}
}

func TestStoreOutageAnswers503InsteadOfSeedData(t *testing.T) {
	authtest.Configure(t)
	pool, err := pgxpool.New(context.Background(), "postgres://pricing_svc_user:x@127.0.0.1:1/commerce_lab?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	mongoStore, err := db.NewMongoStore("mongodb://127.0.0.1:1/commerce_lab_doc", "commerce_lab_doc")
	if err != nil {
		t.Fatalf("create mongo store: %v", err)
	}
	carts := cart.NewMemoryStore()
	if err := carts.SetMany(context.Background(), "u-1", map[string]int64{"SKU-BALO-BLACK-20L": 1}); err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithDeps(repository.NewCatalogPricingRepository(pool, mongoStore), true, true,
		Deps{Carts: carts, Addresses: &fakeAddresses{owned: map[string][]string{"u-1": {"addr-1"}}}})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "product list (mongo down)", method: http.MethodGet, path: "/api/v1/products"},
		{name: "quote lookup (postgres down)", method: http.MethodGet, path: "/api/v1/quotes/Q-1"},
		{name: "quote creation price lookup (postgres down)", method: http.MethodPost, path: "/api/v1/quotes", body: `{"addressId":"addr-1"}`},
		{name: "cart view price lookup (postgres down)", method: http.MethodGet, path: "/api/v1/carts/me"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token(t, "CUSTOMER"))
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

func TestMemoryFallbackServesSeedProducts(t *testing.T) {
	// Seed products exist only when no Mongo store is configured (ALLOW_MEMORY_FALLBACK=true).
	repo := repository.NewCatalogPricingRepository(nil, nil)
	products, err := repo.ListPublishedProducts(context.Background())
	if err != nil || len(products) == 0 {
		t.Fatalf("memory fallback should serve seed products, got %d, err %v", len(products), err)
	}
}
