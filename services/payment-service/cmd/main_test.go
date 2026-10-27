package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/eventbus"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/repository"
)

func TestPaymentUnknown30mLateWebhookAndRefundCeiling_SC05_SC08(t *testing.T) {
	eng := domain.NewPaymentEngine()
	repo := repository.NewPaymentRepository(nil, nil, eng)
	bus := eventbus.New()
	handler := NewServer(repo, bus, false, false)

	// 1. SC05: Create payment attempt for order O-002 (660,000 VND)
	attBody := []byte(`{"attemptId":"PAY-002","orderId":"O-002","idempotencyKey":"IDEM-PAY-002-1","amountVnd":660000}`)
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/attempts", bytes.NewReader(attBody))
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 2. Gateway returns UNKNOWN -> attempt enters 30m hold window
	unkBody := []byte(`{"orderId":"O-002","attemptId":"PAY-002","outcome":"UNKNOWN","amountVnd":660000}`)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhooks", bytes.NewReader(unkBody))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on UNKNOWN webhook, got %d", rec2.Code)
	}

	// 3. Customer clicks "Pay Again" while attempt is UNKNOWN -> must be blocked with 409 Conflict (SC05)
	dupBody := []byte(`{"attemptId":"PAY-002-B","orderId":"O-002","idempotencyKey":"IDEM-PAY-002-2","amountVnd":660000}`)
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/attempts", bytes.NewReader(dupBody))
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for duplicate payment during UNKNOWN, got %d", rec3.Code)
	}

	// 4. 30-minute window expires -> status transitions to EXPIRED_30M
	expBody := []byte(`{"orderId":"O-002","attemptId":"PAY-002","outcome":"EXPIRE_30M","amountVnd":660000}`)
	req4 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhooks", bytes.NewReader(expBody))
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on EXPIRE_30M webhook, got %d", rec4.Code)
	}

	// 5. At minute 40, late CAPTURED webhook arrives -> autoRefundRequired must be true (SC05)
	lateBody := []byte(`{"orderId":"O-002","attemptId":"PAY-002","outcome":"CAPTURED","amountVnd":660000}`)
	req5 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/webhooks", bytes.NewReader(lateBody))
	rec5 := httptest.NewRecorder()
	handler.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on late CAPTURED webhook, got %d", rec5.Code)
	}
	var env struct {
		Data struct {
			AutoRefundRequired bool                  `json:"autoRefundRequired"`
			Summary            domain.PaymentSummary `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec5.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !env.Data.AutoRefundRequired || env.Data.Summary.RefundPending != 660000 {
		t.Fatalf("expected autoRefundRequired=true and refundPending=660000, got %+v", env.Data)
	}

	// 6. SC08: Verify refund ceiling enforcement on O-001 (660,000 VND captured)
	// First refund: 270,000 VND reserved and completed
	r1 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/O-001/refund-reservations", bytes.NewReader([]byte(`{"amountVnd":270000}`)))
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201 on first refund reservation, got %d", w1.Code)
	}
	c1 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/O-001/refund-completions", bytes.NewReader([]byte(`{"amountVnd":270000}`)))
	wc1 := httptest.NewRecorder()
	handler.ServeHTTP(wc1, c1)
	if wc1.Code != http.StatusOK {
		t.Fatalf("expected 200 on first refund completion, got %d", wc1.Code)
	}

	// Second refund: 300,000 VND reserved (pending) -> remaining ceiling = 90,000 VND
	r2 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/O-001/refund-reservations", bytes.NewReader([]byte(`{"amountVnd":300000}`)))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("expected 201 on second refund reservation, got %d", w2.Code)
	}

	// Third refund: 100,000 VND requested -> must be rejected with 409 Conflict (SC08)
	r3 := httptest.NewRequest(http.MethodPost, "/api/v1/payments/O-001/refund-reservations", bytes.NewReader([]byte(`{"amountVnd":100000}`)))
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, r3)
	if w3.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when exceeding refund ceiling, got %d", w3.Code)
	}
}
