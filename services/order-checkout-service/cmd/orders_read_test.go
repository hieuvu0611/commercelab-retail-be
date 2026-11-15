package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
)

func (f *orderFixture) get(t *testing.T, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

type listResp struct {
	Data []struct {
		OrderID         string `json:"orderId"`
		Status          string `json:"status"`
		PaymentStatus   string `json:"paymentStatus"`
		FailureCode     string `json:"failureCode"`
		TotalPayableVND int64  `json:"totalPayableVnd"`
		ItemCount       int64  `json:"itemCount"`
		WarehouseID     string `json:"warehouseId"`
	} `json:"data"`
	Metadata struct {
		Total     int64 `json:"total"`
		Page      int   `json:"page"`
		PageCount int   `json:"pageCount"`
		Limit     int   `json:"limit"`
	} `json:"metadata"`
}

func TestCustomerOrderHistoryIsPagedNewestFirstAndOwnOnly(t *testing.T) {
	f := newOrderFixture(t)
	lan := bearer(t, "user-lan", "CUSTOMER")
	var ids []string
	for i := 0; i < 3; i++ {
		rec := f.place(t, lan, fmt.Sprintf("key-%d", i), fmt.Sprintf(`{"quoteId":"Q-%d"}`, i))
		if rec.Code != http.StatusCreated {
			t.Fatalf("place #%d = %d %s", i, rec.Code, rec.Body.String())
		}
		ids = append(ids, decodeJSON[placed](t, rec).Data.OrderID)
	}
	f.quotes.quote.UserID = "user-minh"
	f.place(t, bearer(t, "user-minh", "CUSTOMER"), "key-m", `{"quoteId":"Q-m"}`)

	got := decodeJSON[listResp](t, f.get(t, lan, "/api/v1/orders?page=1&limit=2"))
	if len(got.Data) != 2 || got.Data[0].OrderID != ids[2] || got.Data[1].OrderID != ids[1] ||
		got.Metadata.Total != 3 || got.Metadata.PageCount != 2 || got.Metadata.Page != 1 || got.Metadata.Limit != 2 {
		t.Fatalf("page 1 = %+v", got)
	}
	if got.Data[0].ItemCount != 3 || got.Data[0].TotalPayableVND != 660000 || got.Data[0].Status != "RESERVED" || got.Data[0].WarehouseID != "" {
		t.Fatalf("summary = %+v", got.Data[0])
	}
	if p2 := decodeJSON[listResp](t, f.get(t, lan, "/api/v1/orders?page=2&limit=2")); len(p2.Data) != 1 || p2.Data[0].OrderID != ids[0] {
		t.Fatalf("page 2 = %+v", p2)
	}
	if empty := decodeJSON[listResp](t, f.get(t, bearer(t, "user-nobody", "CUSTOMER"), "/api/v1/orders")); len(empty.Data) != 0 || empty.Metadata.Total != 0 {
		t.Fatalf("a customer without orders = %+v", empty)
	}
	if filtered := decodeJSON[listResp](t, f.get(t, lan, "/api/v1/orders?status=CONFIRMED")); len(filtered.Data) != 0 {
		t.Fatalf("status filter = %+v", filtered)
	}
}

func TestOrderHistoryValidatesPagingAndNeedsACustomerToken(t *testing.T) {
	f := newOrderFixture(t)
	lan := bearer(t, "user-lan", "CUSTOMER")
	for _, q := range []string{"?page=0", "?page=x", "?limit=0", "?limit=101", "?status=NOPE"} {
		if rec := f.get(t, lan, "/api/v1/orders"+q); rec.Code != http.StatusBadRequest || decodeJSON[orderError](t, rec).Code != "VALIDATION_FAILED" {
			t.Errorf("%s = %d %s", q, rec.Code, rec.Body.String())
		}
	}
	if rec := f.get(t, "", "/api/v1/orders"); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if rec := f.get(t, bearer(t, "s", "WAREHOUSE_STAFF"), "/api/v1/orders"); rec.Code != http.StatusForbidden {
		t.Errorf("staff on the customer list = %d", rec.Code)
	}
}

type detailResp struct {
	Data struct {
		OrderID       string `json:"orderId"`
		Status        string `json:"status"`
		PaymentStatus string `json:"paymentStatus"`
		FailureCode   string `json:"failureCode"`
		VoucherCode   string `json:"voucherCode"`
		Items         []struct {
			SkuID                string `json:"skuId"`
			SkuName              string `json:"skuName"`
			Quantity             int64  `json:"quantity"`
			UnitPriceVND         int64  `json:"unitPriceVnd"`
			AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd"`
		} `json:"items"`
		ShippingAddress *struct {
			Recipient string `json:"recipient"`
			Phone     string `json:"phone"`
			Line1     string `json:"line1"`
			District  string `json:"district"`
		} `json:"shippingAddress"`
		Payment struct {
			AttemptID         string `json:"attemptId"`
			Status            string `json:"status"`
			ExpectedAmountVND int64  `json:"expectedAmountVnd"`
			CapturedAmountVND int64  `json:"capturedAmountVnd"`
		} `json:"payment"`
		RefundObligation *struct {
			AmountVND int64  `json:"amountVnd"`
			Status    string `json:"status"`
		} `json:"refundObligation"`
		SubtotalVND     int64  `json:"subtotalVnd"`
		TotalPayableVND int64  `json:"totalPayableVnd"`
		UserID          string `json:"userId"`
		WarehouseID     string `json:"warehouseId"`
	} `json:"data"`
}

func TestOrderDetailShowsTheFrozenSnapshotPaymentAndOwnerOnly_I04_SC07(t *testing.T) {
	f := newOrderFixture(t)
	lan := bearer(t, "user-lan", "CUSTOMER")
	id := f.placeOne(t)

	rec := f.get(t, lan, "/api/v1/orders/"+id)
	got := decodeJSON[detailResp](t, rec).Data
	if rec.Code != http.StatusOK || got.OrderID != id || got.Status != "RESERVED" || got.VoucherCode != "WELCOME10" || got.TotalPayableVND != 660000 || got.SubtotalVND != 700000 {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}
	if len(got.Items) != 2 || got.Items[0].SkuName != "Backpack" || got.Items[0].AllocatedUnitPaidVND != 270000 || got.Items[0].UnitPriceVND != 300000 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.ShippingAddress == nil || got.ShippingAddress.District != "District 1" || got.ShippingAddress.Recipient != "Nguyen Van Lan" {
		t.Fatalf("address = %+v", got.ShippingAddress)
	}
	if got.Payment.AttemptID != "PAY-"+id || got.Payment.Status != "PROCESSING" || got.Payment.ExpectedAmountVND != 660000 || got.RefundObligation != nil {
		t.Fatalf("payment = %+v refund = %+v", got.Payment, got.RefundObligation)
	}
	if got.UserID != "" || got.WarehouseID != "" {
		t.Fatalf("internal fields leaked: %s", rec.Body.String())
	}

	// The frozen snapshot is what is shown, even when the stored lines would say something else later.
	snap, _, _ := f.snaps.Get(context.Background(), id)
	snap.Lines[0].SkuName = "Backpack (as sold)"
	frozen := snapshot.NewMemoryStore()
	_ = frozen.Save(context.Background(), snap)
	f.snaps.Save(context.Background(), snap) // no-op: frozen
	if again := decodeJSON[detailResp](t, f.get(t, lan, "/api/v1/orders/"+id)).Data; again.Items[0].SkuName != "Backpack" {
		t.Fatalf("snapshot was rewritten: %+v", again.Items[0])
	}

	if rec := f.get(t, bearer(t, "user-minh", "CUSTOMER"), "/api/v1/orders/"+id); rec.Code != http.StatusNotFound || decodeJSON[orderError](t, rec).Code != "ORDER_NOT_FOUND" {
		t.Fatalf("another customer = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.get(t, lan, "/api/v1/orders/O-nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown order = %d", rec.Code)
	}
	if rec := f.get(t, "", "/api/v1/orders/"+id); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
}

func TestOrderDetailShowsTheRefundObligationOfALateCapture_SC05(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	f.outcome(t, id, outcomeBody(id, "UNKNOWN", 0), true)
	f.outcome(t, id, outcomeBody(id, "CAPTURED_LATE", 660000), true)
	got := decodeJSON[detailResp](t, f.get(t, bearer(t, "user-lan", "CUSTOMER"), "/api/v1/orders/"+id)).Data
	if got.Status != "FAILED_CLOSED" || got.PaymentStatus != "REFUND_PENDING" || got.FailureCode != "" && got.FailureCode != "PAYMENT_UNKNOWN_EXPIRED" {
		t.Fatalf("detail = %+v", got)
	}
	if got.RefundObligation == nil || got.RefundObligation.AmountVND != 660000 || got.RefundObligation.Status != "PENDING" || got.Payment.CapturedAmountVND != 660000 {
		t.Fatalf("refund = %+v payment = %+v", got.RefundObligation, got.Payment)
	}
}

type failingSnapshotReads struct{ snapshot.Store }

func (failingSnapshotReads) Get(context.Context, string) (snapshot.Snapshot, bool, error) {
	return snapshot.Snapshot{}, false, dependency.Wrap(fmt.Errorf("mongo down"))
}

func TestOrderDetailStillAnswersFromTheOrderRowsWhenTheSnapshotStoreIsDown(t *testing.T) {
	f := newOrderFixture(t)
	id := f.placeOne(t)
	f.srv = NewServer(f.svcWith(failingSnapshotReads{f.snaps}), nil, nil)
	rec := f.get(t, bearer(t, "user-lan", "CUSTOMER"), "/api/v1/orders/"+id)
	got := decodeJSON[detailResp](t, rec).Data
	if rec.Code != http.StatusOK || len(got.Items) != 2 || got.Items[0].AllocatedUnitPaidVND != 270000 || got.ShippingAddress != nil {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}
}
