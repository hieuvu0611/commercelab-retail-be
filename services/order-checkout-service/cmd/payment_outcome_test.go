package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
)

const outcomeToken = "test-internal-token-0123456789abcdef"

func (f *orderFixture) outcome(t *testing.T, orderID, body string, internal bool) *httptest.ResponseRecorder {
	t.Helper()
	_ = internalauth.Configure(outcomeToken)
	t.Cleanup(internalauth.Reset)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+orderID+"/payment-outcome", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if internal {
		req.Header.Set(internalauth.Header, outcomeToken)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *orderFixture) placeOne(t *testing.T) string {
	t.Helper()
	rec := f.place(t, bearer(t, "user-lan", "CUSTOMER"), "key-1", `{"quoteId":"Q-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("place = %d %s", rec.Code, rec.Body.String())
	}
	return decodeJSON[placed](t, rec).Data.OrderID
}

func outcomeBody(orderID, status string, captured int64) string {
	return fmt.Sprintf(`{"attemptId":"PAY-%s","status":%q,"capturedAmountVnd":%d}`, orderID, status, captured)
}

type outcomeResp struct {
	Data struct {
		OrderID       string `json:"orderId"`
		Status        string `json:"status"`
		PaymentStatus string `json:"paymentStatus"`
	} `json:"data"`
}

func TestPaymentOutcomeRouteIsInternalOnly(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	if rec := f.outcome(t, id, outcomeBody(id, "CAPTURED", 660000), false); rec.Code != http.StatusUnauthorized || decodeJSON[orderError](t, rec).Code != "INTERNAL_AUTH_REQUIRED" {
		t.Fatalf("no token = %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+id+"/payment-outcome", strings.NewReader(outcomeBody(id, "CAPTURED", 660000)))
	req.Header.Set("Authorization", "Bearer "+bearer(t, "user-lan", "CUSTOMER"))
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a customer token must not confirm an order: %d", rec.Code)
	}
	if f.stock.commits.Load() != 0 {
		t.Fatal("stock was committed without the internal token")
	}
}

func TestCapturedPaymentConfirmsTheOrderOnceAndConsumesTheVoucher_SC01(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	for i := 0; i < 2; i++ {
		rec := f.outcome(t, id, outcomeBody(id, "CAPTURED", 660000), true)
		got := decodeJSON[outcomeResp](t, rec)
		if rec.Code != http.StatusOK || got.Data.Status != "CONFIRMED" || got.Data.PaymentStatus != "CAPTURED" || got.Data.OrderID != id {
			t.Fatalf("delivery #%d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ConsumedQuota != 1 || c.ReservedQuota != 0 {
		t.Fatalf("voucher = %+v", c)
	}
}

func TestPaymentOutcomeErrors(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	tests := []struct {
		name, order, body string
		status            int
		code              string
	}{
		{"unknown order", "O-nope", outcomeBody("O-nope", "FAILED", 0), 404, "ORDER_NOT_FOUND"},
		{"other attempt", id, `{"attemptId":"PAY-other","status":"CAPTURED","capturedAmountVnd":660000}`, 409, "PAYMENT_ATTEMPT_MISMATCH"},
		{"bogus status", id, outcomeBody(id, "BOGUS", 0), 400, "VALIDATION_FAILED"},
		{"negative amount", id, outcomeBody(id, "CAPTURED", -1), 400, "VALIDATION_FAILED"},
		{"unknown field", id, `{"attemptId":"PAY-` + id + `","status":"FAILED","expectedAmountVnd":1}`, 400, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.outcome(t, tc.order, tc.body, true)
			e := decodeJSON[orderError](t, rec)
			if rec.Code != tc.status || (tc.code != "" && e.Code != tc.code) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	// A failure after the order was confirmed is a conflict, not a silent flip.
	f.outcome(t, id, outcomeBody(id, "CAPTURED", 660000), true)
	rec := f.outcome(t, id, outcomeBody(id, "FAILED", 0), true)
	if rec.Code != http.StatusConflict || decodeJSON[orderError](t, rec).Code != "PAYMENT_OUTCOME_CONFLICT" {
		t.Fatalf("failure after confirmation = %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownThenLateCaptureOwesARefund_SC05(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	if got := decodeJSON[outcomeResp](t, f.outcome(t, id, outcomeBody(id, "UNKNOWN", 0), true)); got.Data.Status != "UNKNOWN_HOLD" || f.stock.holds.Load() != 1 {
		t.Fatalf("unknown = %+v holds=%d", got, f.stock.holds.Load())
	}
	got := decodeJSON[outcomeResp](t, f.outcome(t, id, outcomeBody(id, "CAPTURED_LATE", 660000), true))
	if got.Data.Status != "FAILED_CLOSED" || got.Data.PaymentStatus != "REFUND_PENDING" || f.stock.commits.Load() != 0 {
		t.Fatalf("late = %+v commits=%d", got, f.stock.commits.Load())
	}
}
