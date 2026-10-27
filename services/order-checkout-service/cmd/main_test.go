package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/eventbus"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/repository"
)

func TestPlaceOrderIdempotencyAndPaymentOutcome_SC01_SC04(t *testing.T) {
	repo := repository.NewOrderRepository(nil, nil)
	bus := eventbus.New()
	srv := NewServer(repo, bus, false, false)

	// 1. Place order O-001 (660,000 VND) with Idempotency-Key
	payload := []byte(`{
		"orderId": "O-001",
		"userId": "user-lan",
		"quoteId": "Q-001",
		"voucherCode": "WELCOME10",
		"totalPayableVnd": 660000
	}`)
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewReader(payload))
	req1.Header.Set("Idempotency-Key", "idem-o-001")
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on first place order, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 2. Replay identical request with same Idempotency-Key -> returns 200 OK with same order O-001 (SC04)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewReader(payload))
	req2.Header.Set("Idempotency-Key", "idem-o-001")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on idempotent replay, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// 3. Apply payment outcome CAPTURED -> order becomes CONFIRMED / CAPTURED
	outReq := httptest.NewRequest(http.MethodPost, "/api/v1/orders/O-001/payment-outcome", bytes.NewReader([]byte(`{"outcome":"CAPTURED"}`)))
	outRec := httptest.NewRecorder()
	srv.ServeHTTP(outRec, outReq)

	if outRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on payment outcome CAPTURED, got %d: %s", outRec.Code, outRec.Body.String())
	}

	// 4. Verify GET /api/v1/orders/O-001 returns CONFIRMED and allocated line item prices (I04, SC07)
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/orders/O-001", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /api/v1/orders/O-001, got %d", getRec.Code)
	}
	var detailResp struct {
		Data domain.OrderRecord `json:"data"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("failed to decode order detail: %v", err)
	}
	if detailResp.Data.Status != "CONFIRMED" || detailResp.Data.PaymentStatus != "CAPTURED" {
		t.Fatalf("expected CONFIRMED/CAPTURED, got %+v", detailResp.Data)
	}
	if len(detailResp.Data.Items) == 0 || detailResp.Data.Items[0].AllocatedUnitPaidVND != 270000 {
		t.Fatalf("expected allocatedUnitPaidVnd = 270000 for first item, got %+v", detailResp.Data.Items)
	}
}
