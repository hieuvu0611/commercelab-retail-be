package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

// Fakes of the three services the order service calls.
type fakeQuotes struct {
	quote checkout.Quote
	err   error
	calls atomic.Int32
}

func (f *fakeQuotes) Get(_ context.Context, bearer, quoteID string) (checkout.Quote, error) {
	f.calls.Add(1)
	if f.err != nil {
		return checkout.Quote{}, f.err
	}
	q := f.quote
	q.ID = quoteID
	return q, nil
}

type fakeStock struct {
	reserves, releases, commits, holds atomic.Int32
	err                                error
}

func (f *fakeStock) Reserve(context.Context, string, string, []checkout.StockLine) error {
	f.reserves.Add(1)
	return f.err
}
func (f *fakeStock) Release(context.Context, string) error { f.releases.Add(1); return nil }
func (f *fakeStock) Commit(context.Context, string) error  { f.commits.Add(1); return nil }
func (f *fakeStock) ExceptionHold(context.Context, string) error {
	f.holds.Add(1)
	return nil
}

type fakePayments struct {
	calls, closes atomic.Int32
	err           error
}

func (f *fakePayments) CreateAttempt(_ context.Context, orderID, _ string, _ int64) (checkout.Attempt, error) {
	f.calls.Add(1)
	if f.err != nil {
		return checkout.Attempt{}, f.err
	}
	return checkout.Attempt{AttemptID: "PAY-" + orderID, Status: "PROCESSING"}, nil
}

func (f *fakePayments) AttemptState(context.Context, string) (checkout.PaymentState, bool, error) {
	return checkout.PaymentState{}, false, nil
}

func (f *fakePayments) CloseAttempt(context.Context, string) error { f.closes.Add(1); return nil }

type orderFixture struct {
	srv      http.Handler
	quotes   *fakeQuotes
	stock    *fakeStock
	payments *fakePayments
	vouchers *voucher.MemoryStore
}

func newOrderFixture(t *testing.T) *orderFixture {
	t.Helper()
	authtest.Configure(t)
	f := &orderFixture{
		quotes: &fakeQuotes{quote: checkout.Quote{
			UserID: "user-lan", AddressID: "addr-1", VoucherCode: "WELCOME10",
			SubtotalVND: 700000, DiscountVND: 70000, ShippingFeeVND: 30000, TotalPayableVND: 660000,
			ExpiresAt: time.Now().Add(5 * time.Minute),
			Lines: []checkout.QuoteLine{
				{SkuID: "SKU-BALO-BLACK-20L", SkuName: "Backpack", Quantity: 2, UnitPriceVND: 300000, LineDiscountVND: 60000, AllocatedUnitPaidVND: 270000},
				{SkuID: "SKU-BOTTLE-BLUE-500ML", SkuName: "Bottle", Quantity: 1, UnitPriceVND: 100000, LineDiscountVND: 10000, AllocatedUnitPaidVND: 90000},
			},
		}},
		stock:    &fakeStock{},
		payments: &fakePayments{},
		vouchers: voucher.NewMemoryStore(voucher.DevCampaigns()...),
	}
	svc := checkout.New(checkout.Deps{Orders: orders.NewMemoryStore(), Quotes: f.quotes, Stock: f.stock, Payments: f.payments, Vouchers: f.vouchers, WarehouseID: "WH-HCM-01"})
	f.srv = NewServer(svc, voucher.NewService(f.vouchers), voucher.NewAdmin(f.vouchers))
	return f
}

func (f *orderFixture) place(t *testing.T, token, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

type placed struct {
	Message string `json:"message"`
	Data    struct {
		OrderID          string `json:"orderId"`
		Status           string `json:"status"`
		PaymentStatus    string `json:"paymentStatus"`
		PaymentAttemptID string `json:"paymentAttemptId"`
		TotalPayableVND  int64  `json:"totalPayableVnd"`
		UserID           string `json:"userId"`
		Items            []struct {
			SkuID                string `json:"skuId"`
			AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd"`
		} `json:"items"`
	} `json:"data"`
}

type orderError struct {
	Code          string `json:"code"`
	Status        int    `json:"status"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId"`
}

func TestPlaceOrderCreatesOneOrderAndReplaysIt_SC01_SC04(t *testing.T) {
	f := newOrderFixture(t)
	token := bearer(t, "user-lan", "CUSTOMER")

	first := f.place(t, token, "key-1", `{"quoteId":"Q-1"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first = %d %s", first.Code, first.Body.String())
	}
	got := decodeJSON[placed](t, first)
	if got.Data.Status != "RESERVED" || got.Data.PaymentStatus != "PROCESSING" || got.Data.TotalPayableVND != 660000 ||
		got.Data.PaymentAttemptID != "PAY-"+got.Data.OrderID || len(got.Data.Items) != 2 || got.Data.Items[0].AllocatedUnitPaidVND != 270000 {
		t.Fatalf("order = %+v", got.Data)
	}
	if got.Data.UserID != "" {
		t.Fatal("the response must not echo the user id")
	}

	second := f.place(t, token, "key-1", `{"quoteId":"Q-1"}`)
	if second.Code != http.StatusOK || decodeJSON[placed](t, second).Data.OrderID != got.Data.OrderID {
		t.Fatalf("replay = %d %s", second.Code, second.Body.String())
	}
	if f.stock.reserves.Load() != 1 || f.payments.calls.Load() != 1 {
		t.Fatalf("stock=%d payments=%d, want one each", f.stock.reserves.Load(), f.payments.calls.Load())
	}

	conflict := f.place(t, token, "key-1", `{"quoteId":"Q-2"}`)
	if conflict.Code != http.StatusConflict || decodeJSON[orderError](t, conflict).Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("different content = %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestPlaceOrderRequiresACustomerToken(t *testing.T) {
	f := newOrderFixture(t)
	if rec := f.place(t, "", "key-1", `{"quoteId":"Q-1"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	if rec := f.place(t, bearer(t, "staff-1", "WAREHOUSE_STAFF"), "key-1", `{"quoteId":"Q-1"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("staff = %d", rec.Code)
	}
	if f.quotes.calls.Load() != 0 {
		t.Fatal("an unauthenticated call reached the quote service")
	}
}

func TestPlaceOrderValidatesTheRequestShape(t *testing.T) {
	f := newOrderFixture(t)
	token := bearer(t, "user-lan", "CUSTOMER")
	tests := []struct {
		name, key, body string
		status          int
		code            string
	}{
		{"missing key", "", `{"quoteId":"Q-1"}`, 400, "MISSING_IDEMPOTENCY_KEY"},
		{"bad key", "has space", `{"quoteId":"Q-1"}`, 400, "INVALID_IDEMPOTENCY_KEY"},
		{"long key", strings.Repeat("k", 129), `{"quoteId":"Q-1"}`, 400, "INVALID_IDEMPOTENCY_KEY"},
		{"missing quote", "k", `{}`, 400, "VALIDATION_FAILED"},
		{"blank quote", "k", `{"quoteId":"  "}`, 400, "VALIDATION_FAILED"},
		{"client price is rejected", "k", `{"quoteId":"Q-1","totalPayableVnd":1}`, 400, ""},
		{"client user id is rejected", "k", `{"quoteId":"Q-1","userId":"user-minh"}`, 400, ""},
		{"not json", "k", `nope`, 400, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.place(t, token, tc.key, tc.body)
			e := decodeJSON[orderError](t, rec)
			if rec.Code != tc.status || (tc.code != "" && e.Code != tc.code) || e.Status != tc.status {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	if f.quotes.calls.Load() != 0 {
		t.Fatal("an invalid request reached the quote service")
	}
}

func TestPlaceOrderQuoteErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*orderFixture)
		status int
		code   string
	}{
		{"unknown quote", func(f *orderFixture) { f.quotes.err = checkout.ErrQuoteNotFound }, 404, "QUOTE_NOT_FOUND"},
		{"other customer's quote", func(f *orderFixture) { f.quotes.quote.UserID = "user-minh" }, 404, "QUOTE_NOT_FOUND"},
		{"changed quote", func(f *orderFixture) { f.quotes.quote.Stale = true }, 409, "QUOTE_CHANGED"},
		{"expired quote", func(f *orderFixture) { f.quotes.quote.ExpiresAt = time.Now().Add(-time.Second) }, 409, "QUOTE_EXPIRED"},
		{"inconsistent quote", func(f *orderFixture) { f.quotes.quote.TotalPayableVND = 1 }, 409, "QUOTE_INVALID"},
		{"catalog down", func(f *orderFixture) { f.quotes.err = dependency.Wrap(errors.New("refused")) }, 503, "DEPENDENCY_UNAVAILABLE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrderFixture(t)
			tc.mutate(f)
			rec := f.place(t, bearer(t, "user-lan", "CUSTOMER"), "key-1", `{"quoteId":"Q-1"}`)
			if rec.Code != tc.status || decodeJSON[orderError](t, rec).Code != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if f.stock.reserves.Load() != 0 || f.payments.calls.Load() != 0 {
				t.Fatal("a rejected quote reached stock or payment")
			}
		})
	}
}

func TestLastUnitIsNotOverSold_SC03(t *testing.T) {
	f := newOrderFixture(t)
	f.stock.err = checkout.ErrOutOfStock
	token := bearer(t, "user-lan", "CUSTOMER")
	rec := f.place(t, token, "key-1", `{"quoteId":"Q-1"}`)
	if rec.Code != http.StatusConflict || decodeJSON[orderError](t, rec).Code != "OUT_OF_STOCK" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if f.payments.calls.Load() != 0 {
		t.Fatal("a payment attempt was created without stock")
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ReservedQuota != 0 {
		t.Fatalf("voucher still held: %d", c.ReservedQuota)
	}
	// Same key again: the same answer, nothing is retried behind the customer's back.
	again := f.place(t, token, "key-1", `{"quoteId":"Q-1"}`)
	if again.Code != http.StatusConflict || f.stock.reserves.Load() != 1 {
		t.Fatalf("replay = %d, stock calls = %d", again.Code, f.stock.reserves.Load())
	}
}

func TestLastVoucherUseGoesToOneOrder_SC10(t *testing.T) {
	f := newOrderFixture(t)
	f.quotes.quote.VoucherCode = "LASTONE10"
	if rec := f.place(t, bearer(t, "user-lan", "CUSTOMER"), "key-1", `{"quoteId":"Q-1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d %s", rec.Code, rec.Body.String())
	}
	f.quotes.quote.UserID = "user-minh"
	rec := f.place(t, bearer(t, "user-minh", "CUSTOMER"), "key-2", `{"quoteId":"Q-2"}`)
	if rec.Code != http.StatusUnprocessableEntity || decodeJSON[orderError](t, rec).Code != "VOUCHER_EXHAUSTED" {
		t.Fatalf("second = %d %s", rec.Code, rec.Body.String())
	}
	if f.stock.reserves.Load() != 1 || f.payments.calls.Load() != 1 {
		t.Fatalf("stock=%d payments=%d, want the second order to stop at the voucher", f.stock.reserves.Load(), f.payments.calls.Load())
	}
}

func TestPaymentServiceOutageClosesTheOrderAndReleasesEverything(t *testing.T) {
	f := newOrderFixture(t)
	f.payments.err = dependency.Wrap(errors.New("refused"))
	rec := f.place(t, bearer(t, "user-lan", "CUSTOMER"), "key-1", `{"quoteId":"Q-1"}`)
	if rec.Code != http.StatusServiceUnavailable || decodeJSON[orderError](t, rec).Code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if f.stock.releases.Load() != 1 {
		t.Fatalf("stock releases = %d", f.stock.releases.Load())
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ReservedQuota != 0 {
		t.Fatalf("voucher still held: %d", c.ReservedQuota)
	}
}

func TestErrorResponsesDoNotLeakInternals(t *testing.T) {
	f := newOrderFixture(t)
	f.quotes.err = dependency.Wrap(errors.New("dial tcp 10.0.0.7:8081: connection refused"))
	rec := f.place(t, bearer(t, "user-lan", "CUSTOMER"), "key-1", `{"quoteId":"Q-1"}`)
	if strings.Contains(rec.Body.String(), "10.0.0.7") || strings.Contains(rec.Body.String(), "refused") {
		t.Fatalf("response leaks an internal address: %s", rec.Body.String())
	}
}
