package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
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
