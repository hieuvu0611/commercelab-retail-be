package orderclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/svcclient"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
)

func TestPaymentOutcomeSendsTheInternalTokenAndAnIdempotencyKey(t *testing.T) {
	_ = internalauth.Configure("test-internal-token-0123456789abcdef")
	t.Cleanup(internalauth.Reset)
	var got struct {
		path, token, key string
		body             map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.token, got.key = r.URL.Path, r.Header.Get(internalauth.Header), r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(srv.URL, svcclient.New(svcclient.Config{Timeout: time.Second, MaxRetries: 1, RetryDelay: time.Millisecond}))
	err := c.PaymentOutcome(context.Background(), attempt.Attempt{AttemptID: "PAY-O-1", OrderID: "O-1", Status: "CAPTURED", CapturedAmountVND: 660000})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/api/v1/orders/O-1/payment-outcome" || got.token == "" || got.key != "outcome-PAY-O-1-CAPTURED" ||
		got.body["status"] != "CAPTURED" || got.body["capturedAmountVnd"] != float64(660000) || got.body["attemptId"] != "PAY-O-1" {
		t.Fatalf("request = %+v", got)
	}
}

func TestPaymentOutcomeReturnsTheOrderServiceError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	c := New(srv.URL, svcclient.New(svcclient.Config{Timeout: time.Second, MaxRetries: 0}))
	if err := c.PaymentOutcome(context.Background(), attempt.Attempt{AttemptID: "PAY-O-1", OrderID: "O-1", Status: "FAILED"}); err == nil {
		t.Fatal("a 503 must be an error")
	}
}
