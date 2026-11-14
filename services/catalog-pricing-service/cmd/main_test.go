package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogProductsAndQuotePersistence_SC01(t *testing.T) {
	repo := repository.NewCatalogPricingRepository(nil, nil)
	srv := NewServer(repo, false, false)

	// 1. Kiểm tra GET /api/v1/products
	prodReq := httptest.NewRequest(http.MethodGet, "/api/v1/products", nil)
	prodRec := httptest.NewRecorder()
	srv.ServeHTTP(prodRec, prodReq)
	if prodRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/v1/products, got %d", prodRec.Code)
	}

	// 2. Tạo báo giá SC01 (2 Balo + 1 Bình nước + WELCOME10 = 660.000đ) không cần truyền tay đơn giá
	quotePayload := []byte(`{
		"quoteId": "Q-SC01-TEST",
		"userId": "user-lan",
		"voucherCode": "WELCOME10",
		"items": [
			{"skuId": "SKU-BALO-BLACK-20L", "quantity": 2},
			{"skuId": "SKU-BOTTLE-BLUE-500ML", "quantity": 1}
		]
	}`)
	qReq := httptest.NewRequest(http.MethodPost, "/api/v1/quotes", bytes.NewReader(quotePayload))
	qRec := httptest.NewRecorder()
	srv.ServeHTTP(qRec, qReq)

	if qRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on POST /api/v1/quotes, got %d: %s", qRec.Code, qRec.Body.String())
	}

	var createResp struct {
		Data domain.Quote `json:"data"`
	}
	if err := json.Unmarshal(qRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("failed to unmarshal quote: %v", err)
	}
	if createResp.Data.TotalPayableVND != 660000 {
		t.Fatalf("expected SC01 totalPayableVnd = 660000, got %d", createResp.Data.TotalPayableVND)
	}

	// 3. Truy vấn lại báo giá đã lưu qua GET /api/v1/quotes/Q-SC01-TEST
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/quotes/Q-SC01-TEST", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /api/v1/quotes/Q-SC01-TEST, got %d: %s", getRec.Code, getRec.Body.String())
	}
}

func TestStoreOutageAnswers503InsteadOfSeedData(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://pricing_svc_user:x@127.0.0.1:1/commerce_lab?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	mongoStore, err := db.NewMongoStore("mongodb://127.0.0.1:1/commerce_lab_doc", "commerce_lab_doc")
	if err != nil {
		t.Fatalf("create mongo store: %v", err)
	}
	srv := NewServer(repository.NewCatalogPricingRepository(pool, mongoStore), true, true)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "product list (mongo down)", method: http.MethodGet, path: "/api/v1/products"},
		{name: "quote lookup (postgres down)", method: http.MethodGet, path: "/api/v1/quotes/Q-1"},
		{name: "quote creation price lookup (postgres down)", method: http.MethodPost, path: "/api/v1/quotes",
			body: `{"quoteId":"Q-X","userId":"user-lan","items":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`},
		{name: "quote creation persist (postgres down)", method: http.MethodPost, path: "/api/v1/quotes",
			body: `{"quoteId":"Q-Y","userId":"user-lan","items":[{"skuId":"SKU-BALO-BLACK-20L","skuName":"Balo","unitPriceVnd":300000,"quantity":1}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
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
