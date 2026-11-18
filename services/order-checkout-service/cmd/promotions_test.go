package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/promotion"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

func promoBearer(t *testing.T, userID string, roles ...string) string {
	t.Helper()
	tok, err := auth.SignAccessToken(auth.Identity{UserID: userID, Roles: roles})
	if err != nil {
		t.Fatalf("SignAccessToken: %v", err)
	}
	return tok
}

func TestPromotionRoutesLifecycleAndRBAC(t *testing.T) {
	authtest.Configure(t)
	store := promotion.NewMemoryStore(promotion.DevCampaigns()...)
	vStore := voucher.NewMemoryStore(voucher.DevCampaigns()...)
	srv := NewServer(nil, voucher.NewService(vStore), voucher.NewAdmin(vStore), store)

	// 1. Public active list works without auth.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/promotions/active", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /promotions/active = %d (%s)", rec.Code, rec.Body.String())
	}

	// 2. Customer role cannot create admin promotion.
	custTok := promoBearer(t, "user-lan", "CUSTOMER")
	createBody := `{"code":"SPRING20K","name":"Spring 20k","discountType":"FIXED_AMOUNT","discountValue":20000,"minOrderVnd":100000,"totalQuota":50,"budgetVnd":1000000,"perCustomerLimit":1,"autoApply":false,"stackable":true,"priority":15,"status":"ACTIVE"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/promotions", bytes.NewBufferString(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+custTok)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for CUSTOMER creating promotion, got %d", rec.Code)
	}

	// 3. Marketing role creates campaign and customer evaluates it.
	mktTok := promoBearer(t, "mkt-1", "MARKETING")
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/promotions", bytes.NewBufferString(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mktTok)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /admin/promotions = %d (%s)", rec.Code, rec.Body.String())
	}

	evalBody := `{"campaignIds":["SPRING20K"],"lines":[{"skuId":"SKU-1","quantity":2,"unitPriceVnd":550000}]}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/promotions/evaluate", bytes.NewBufferString(evalBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+custTok)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /promotions/evaluate = %d (%s)", rec.Code, rec.Body.String())
	}
	var env struct {
		Data promotion.EvalResult `json:"data"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&env)
	// Subtotal 1,100,000 triggers SPRING20K (20,000, priority 15, stackable=true) + seeded CAMP-TIER1M (5% of 1,080,000 = 54,000) = 74,000.
	if env.Data.TotalDiscountVND != 74000 || len(env.Data.Applied) != 2 {
		t.Fatalf("unexpected evaluate result: %+v", env.Data)
	}
}
