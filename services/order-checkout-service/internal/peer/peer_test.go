package peer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/svcclient"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
)

func client() *svcclient.Client {
	return svcclient.New(svcclient.Config{Timeout: time.Second, MaxRetries: 1, RetryDelay: time.Millisecond})
}

func TestQuotesReadsTheQuoteWithTheCallersToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/quotes/Q-1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"isExpired":false,"isStale":true,"quote":{"quoteId":"Q-1","userId":"user-lan","addressId":"addr-1","voucherCode":"WELCOME10",
			"subtotalVnd":700000,"discountVnd":70000,"shippingFeeVnd":30000,"totalPayableVnd":660000,"expiresAt":"2026-11-15T10:05:00Z",
			"lines":[{"skuId":"SKU-A","skuName":"Backpack","quantity":2,"unitPriceVnd":300000,"lineDiscountVnd":60000,"allocatedUnitPaidVnd":270000}]}}}`))
	}))
	defer srv.Close()
	q, err := NewQuotes(srv.URL, client()).Get(context.Background(), "tok", "Q-1")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" || q.ID != "Q-1" || q.UserID != "user-lan" || q.TotalPayableVND != 660000 || q.VoucherCode != "WELCOME10" ||
		len(q.Lines) != 1 || q.Lines[0].AllocatedUnitPaidVND != 270000 || q.ExpiresAt.IsZero() || !q.Stale {
		t.Fatalf("auth=%q quote=%+v", auth, q)
	}
}

func TestQuotesMapsNotFoundAndOutages(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Quote not found","code":"QUOTE_NOT_FOUND","status":404}`))
	}))
	defer notFound.Close()
	if _, err := NewQuotes(notFound.URL, client()).Get(context.Background(), "t", "Q-x"); !errors.Is(err, checkout.ErrQuoteNotFound) {
		t.Fatalf("404 err = %v", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer down.Close()
	if _, err := NewQuotes(down.URL, client()).Get(context.Background(), "t", "Q-x"); !dependency.IsUnavailable(err) {
		t.Fatalf("500 err = %v", err)
	}
}

func TestStockReserveSendsTheInternalTokenAndAnIdempotencyKey(t *testing.T) {
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure("0123456789abcdef0123456789abcdef")
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	err := NewStock(srv.URL, client()).Reserve(context.Background(), "O-1", "WH-HCM-01", []checkout.StockLine{{SkuID: "SKU-A", Quantity: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Get("Idempotency-Key") != "reserve-O-1" || hdr.Get(internalauth.Header) == "" || got["orderId"] != "O-1" || got["warehouseId"] != "WH-HCM-01" {
		t.Fatalf("headers=%v body=%v", hdr, got)
	}
}

func TestStockReserveMapsInsufficientStockAndReleaseTreatsMissingAsDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		if r.URL.Path == "/api/v1/inventory/reservations" {
			_, _ = w.Write([]byte(`{"message":"not enough","code":"INSUFFICIENT_STOCK","status":409}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"reservation not found","code":"RELEASE_RESERVATION_FAILED","status":409}`))
	}))
	defer srv.Close()
	s := NewStock(srv.URL, client())
	if err := s.Reserve(context.Background(), "O-1", "WH", nil); !errors.Is(err, checkout.ErrOutOfStock) {
		t.Fatalf("reserve err = %v", err)
	}
	if err := s.Release(context.Background(), "O-1"); err != nil {
		t.Fatalf("release of nothing = %v", err)
	}
}

func TestPaymentsCreateAttemptUsesTheOrderIdAsIdempotencyKey(t *testing.T) {
	var hdr http.Header
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got)
		if r.URL.Path != "/api/v1/payments/attempts" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"attemptId":"PAY-O-1","status":"PROCESSING"}}`))
	}))
	defer srv.Close()
	a, err := NewPayments(srv.URL, client()).CreateAttempt(context.Background(), "O-1", "user-lan", 660000)
	if err != nil || a.AttemptID != "PAY-O-1" || a.Status != "PROCESSING" {
		t.Fatalf("attempt = %+v, %v", a, err)
	}
	if hdr.Get("Idempotency-Key") != "pay-O-1" || got["orderId"] != "O-1" || got["userId"] != "user-lan" || got["amountVnd"] != float64(660000) {
		t.Fatalf("headers=%v body=%v", hdr, got)
	}
}

func TestStockCommitAndExceptionHoldMapAGoneReservation(t *testing.T) {
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure("0123456789abcdef0123456789abcdef")
	var paths []string
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		key = r.Header.Get("Idempotency-Key")
		switch r.URL.Path {
		case "/api/v1/inventory/reservations/O-1/commit":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"COMMIT_RESERVATION_FAILED","status":409}`))
		case "/api/v1/inventory/reservations/O-1/exception-hold":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"RESERVATION_EXPIRED","status":409}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	defer srv.Close()
	s := NewStock(srv.URL, client())
	if err := s.Commit(context.Background(), "O-1"); !errors.Is(err, checkout.ErrReservationLost) || key != "commit-O-1" {
		t.Fatalf("commit err = %v key=%q", err, key)
	}
	if err := s.ExceptionHold(context.Background(), "O-1"); !errors.Is(err, checkout.ErrReservationLost) {
		t.Fatalf("hold err = %v", err)
	}
	if err := s.Commit(context.Background(), "O-2"); err != nil {
		t.Fatalf("commit of a held reservation = %v", err)
	}
}

func TestPaymentsCloseAttemptTreatsAnUnknownAttemptAsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/payments/attempts/PAY-O-1/close" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"PAYMENT_NOT_FOUND","status":404}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	p := NewPayments(srv.URL, client())
	if err := p.CloseAttempt(context.Background(), "PAY-O-1"); err != nil {
		t.Fatalf("close of an unknown attempt = %v", err)
	}
	if err := p.CloseAttempt(context.Background(), "PAY-O-2"); err != nil {
		t.Fatalf("close = %v", err)
	}
}

func TestCartsRemoveOrderedSendsTheUserAndTheSkus(t *testing.T) {
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure("0123456789abcdef0123456789abcdef")
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got)
		if r.URL.Path != "/api/v1/carts/internal/ordered-items" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	if err := NewCarts(srv.URL, client()).RemoveOrdered(context.Background(), "user-lan", []string{"SKU-A", "SKU-B"}); err != nil {
		t.Fatal(err)
	}
	if got["userId"] != "user-lan" || len(got["skuIds"].([]any)) != 2 || hdr.Get(internalauth.Header) == "" || hdr.Get("Idempotency-Key") == "" {
		t.Fatalf("headers=%v body=%v", hdr, got)
	}
}

func TestPaymentsAttemptStateReadsTheAttemptAndMapsMissing(t *testing.T) {
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure("0123456789abcdef0123456789abcdef")
	var token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get(internalauth.Header)
		if r.URL.Path == "/api/v1/payments/orders/O-none/attempt" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"PAYMENT_NOT_FOUND","status":404}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"attemptId":"PAY-O-1","status":"CAPTURED","capturedAmountVnd":660000}}`))
	}))
	defer srv.Close()
	p := NewPayments(srv.URL, client())
	st, found, err := p.AttemptState(context.Background(), "O-1")
	if err != nil || !found || st.Status != "CAPTURED" || st.CapturedVND != 660000 || st.AttemptID != "PAY-O-1" || token == "" {
		t.Fatalf("state = %+v found=%v err=%v token=%q", st, found, err, token)
	}
	if _, found, err := p.AttemptState(context.Background(), "O-none"); err != nil || found {
		t.Fatalf("missing = found=%v err=%v", found, err)
	}
}
