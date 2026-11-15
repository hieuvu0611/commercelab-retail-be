package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
)

type cancelResp struct {
	Message string `json:"message"`
	Data    struct {
		OrderID          string `json:"orderId"`
		Status           string `json:"status"`
		PaymentStatus    string `json:"paymentStatus"`
		RefundObligation *struct {
			AmountVND int64  `json:"amountVnd"`
			Status    string `json:"status"`
		} `json:"refundObligation"`
	} `json:"data"`
}

func (f *orderFixture) cancel(t *testing.T, token, orderID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+orderID+"/cancel", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func (f *orderFixture) paidOrder(t *testing.T) string {
	t.Helper()
	id := f.placeOne(t)
	if rec := f.outcome(t, id, outcomeBody(id, "CAPTURED", 660000), true); rec.Code != http.StatusOK {
		t.Fatalf("capture = %d %s", rec.Code, rec.Body.String())
	}
	return id
}

func TestCustomerCancelsAnUnpaidOrderAndRepeatingIsHarmless_Case1(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	token := bearer(t, "user-lan", "CUSTOMER")
	for i := 0; i < 2; i++ {
		rec := f.cancel(t, token, id)
		got := decodeJSON[cancelResp](t, rec)
		if rec.Code != http.StatusOK || got.Data.OrderID != id || got.Data.Status != "CANCELLED" || got.Data.PaymentStatus != "UNPAID" || got.Data.RefundObligation != nil {
			t.Fatalf("cancel #%d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if f.stock.releases.Load() != 1 {
		t.Fatalf("stock releases = %d, want 1", f.stock.releases.Load())
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ReservedQuota != 0 || c.ConsumedQuota != 0 {
		t.Fatalf("voucher = %+v, want the use released", c)
	}
}

func TestCustomerCancelsAPaidOrderAndSeesTheRefund_Case2(t *testing.T) {
	f := newOrderFixture(t)
	id := f.paidOrder(t)
	rec := f.cancel(t, bearer(t, "user-lan", "CUSTOMER"), id)
	got := decodeJSON[cancelResp](t, rec)
	if rec.Code != http.StatusOK || got.Data.Status != "CANCELLED" || got.Data.PaymentStatus != "REFUND_PENDING" ||
		got.Data.RefundObligation == nil || got.Data.RefundObligation.AmountVND != 660000 || got.Data.RefundObligation.Status != "PENDING" {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	if f.stock.committedReleases.Load() != 1 || f.stock.releases.Load() != 0 {
		t.Fatalf("committedReleases=%d releases=%d", f.stock.committedReleases.Load(), f.stock.releases.Load())
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ConsumedQuota != 1 {
		t.Fatalf("voucher = %+v, a paid cancellation keeps the use consumed", c)
	}
	// The order now shows up under the new status filter and its detail shows the refund.
	list := f.get(t, bearer(t, "user-lan", "CUSTOMER"), "/api/v1/orders?status=CANCELLED")
	if list.Code != http.StatusOK {
		t.Fatalf("status filter = %d %s", list.Code, list.Body.String())
	}
}

func TestCancelRefusalsUseDocumentedCodes(t *testing.T) {
	t.Run("handed over (case 4)", func(t *testing.T) {
		f := newOrderFixture(t)
		token := bearer(t, "user-lan", "CUSTOMER")
		id := f.paidOrder(t)
		f.ful.result = checkout.StopTooLate
		rec := f.cancel(t, token, id)
		if rec.Code != http.StatusConflict || decodeJSON[orderError](t, rec).Code != "ORDER_HANDED_OVER" || f.stock.committedReleases.Load() != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("payment unknown (case 5)", func(t *testing.T) {
		f := newOrderFixture(t)
		token := bearer(t, "user-lan", "CUSTOMER")
		id := f.placeOne(t)
		f.outcome(t, id, outcomeBody(id, "UNKNOWN", 0), true)
		rec := f.cancel(t, token, id)
		if rec.Code != http.StatusConflict || decodeJSON[orderError](t, rec).Code != "PAYMENT_UNRESOLVED" || f.stock.releases.Load() != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("already closed", func(t *testing.T) {
		f := newOrderFixture(t)
		token := bearer(t, "user-lan", "CUSTOMER")
		id := f.placeOne(t)
		f.outcome(t, id, outcomeBody(id, "FAILED", 0), true)
		rec := f.cancel(t, token, id)
		if rec.Code != http.StatusConflict || decodeJSON[orderError](t, rec).Code != "ORDER_NOT_CANCELLABLE" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestCancelIsForTheOwningCustomerOnly(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	if rec := f.cancel(t, "", id); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec := f.cancel(t, bearer(t, "user-staff", "SUPPORT_AGENT"), id); rec.Code != http.StatusForbidden {
		t.Fatalf("staff = %d", rec.Code)
	}
	rec := f.cancel(t, bearer(t, "user-other", "CUSTOMER"), id)
	if rec.Code != http.StatusNotFound || decodeJSON[orderError](t, rec).Code != "ORDER_NOT_FOUND" {
		t.Fatalf("another customer = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.cancel(t, bearer(t, "user-lan", "CUSTOMER"), "O-nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown order = %d", rec.Code)
	}
	if f.stock.releases.Load() != 0 {
		t.Fatal("a refused cancel released stock")
	}
}

func TestCancelWhileThePackingIsInProgressIsAcceptedAsARequest_Case3(t *testing.T) {
	f := newOrderFixture(t)
	token := bearer(t, "user-lan", "CUSTOMER")
	id := f.paidOrder(t)
	f.ful.result = checkout.StopRequested
	for i := 0; i < 2; i++ {
		rec := f.cancel(t, token, id)
		got := decodeJSON[cancelResp](t, rec)
		if rec.Code != http.StatusAccepted || got.Data.Status != "CANCEL_REQUESTED" || got.Data.PaymentStatus != "CAPTURED" || got.Data.RefundObligation != nil {
			t.Fatalf("request #%d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if f.stock.committedReleases.Load() != 0 {
		t.Fatal("a requested cancellation released stock")
	}
	list := f.get(t, token, "/api/v1/orders?status=CANCEL_REQUESTED")
	if list.Code != http.StatusOK {
		t.Fatalf("status filter = %d %s", list.Code, list.Body.String())
	}
	// The warehouse stops the parcel; asking again now settles the order as cancelled with the refund.
	f.ful.result = checkout.StopStopped
	rec := f.cancel(t, token, id)
	got := decodeJSON[cancelResp](t, rec)
	if rec.Code != http.StatusOK || got.Data.Status != "CANCELLED" || got.Data.RefundObligation == nil || got.Data.RefundObligation.AmountVND != 660000 {
		t.Fatalf("after the stop = %d %s", rec.Code, rec.Body.String())
	}
}
