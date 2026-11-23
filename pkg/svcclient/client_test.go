package svcclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/telemetry"
)

func newClient() *Client {
	return New(Config{Timeout: 200 * time.Millisecond, MaxRetries: 2, RetryDelay: time.Millisecond})
}

func TestForwardsCorrelationIdempotencyAndInternalToken(t *testing.T) {
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure("0123456789abcdef0123456789abcdef")
	var seen http.Header
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":"ok","data":{"attemptId":"PAY-1"}}`))
	}))
	defer srv.Close()

	ctx := middleware.WithCorrelationID(context.Background(), "corr-42")
	var out struct{ AttemptID string }
	res, err := newClient().Do(ctx, http.MethodPost, srv.URL+"/x", Options{IdempotencyKey: "pay-O-1", Internal: true}, map[string]any{"orderId": "O-1"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusCreated || out.AttemptID != "PAY-1" {
		t.Fatalf("res=%+v out=%+v", res, out)
	}
	if seen.Get("X-Correlation-ID") != "corr-42" || seen.Get("Idempotency-Key") != "pay-O-1" || seen.Get(internalauth.Header) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("headers = %v", seen)
	}
	if body["orderId"] != "O-1" || seen.Get("Content-Type") != "application/json" {
		t.Fatalf("body=%v content-type=%q", body, seen.Get("Content-Type"))
	}
}

func TestForwardsABearerTokenAndNoInternalTokenUnlessAsked(t *testing.T) {
	t.Cleanup(internalauth.Reset)
	_ = internalauth.Configure("0123456789abcdef0123456789abcdef")
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	if _, err := newClient().Do(context.Background(), http.MethodGet, srv.URL, Options{Bearer: "tok"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if seen.Get("Authorization") != "Bearer tok" || seen.Get(internalauth.Header) != "" {
		t.Fatalf("headers = %v", seen)
	}
}

func TestMapsErrorEnvelopeToATypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"No stock","code":"INSUFFICIENT_STOCK","status":409,"correlationId":"c"}`))
	}))
	defer srv.Close()
	_, err := newClient().Do(context.Background(), http.MethodPost, srv.URL, Options{}, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "INSUFFICIENT_STOCK" || apiErr.Message != "No stock" {
		t.Fatalf("err = %v", err)
	}
	if dependency.IsUnavailable(err) {
		t.Fatal("a 409 must not look like an outage")
	}
}

func TestTimeoutIsADependencyFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) }))
	defer srv.Close()
	c := New(Config{Timeout: 50 * time.Millisecond})
	start := time.Now()
	_, err := c.Do(context.Background(), http.MethodGet, srv.URL, Options{}, nil, nil)
	if !dependency.IsUnavailable(err) {
		t.Fatalf("timeout err = %v", err)
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatalf("took %v, timeout did not fire", time.Since(start))
	}
}

func TestServerErrorsAreDependencyFailuresWithTheStatusKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"secret internals","code":"INTERNAL_ERROR","status":500}`))
	}))
	defer srv.Close()
	_, err := newClient().Do(context.Background(), http.MethodGet, srv.URL, Options{}, nil, nil)
	var apiErr *APIError
	if !dependency.IsUnavailable(err) || !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Fatalf("err = %v", err)
	}
}

func TestRetriesOnlyIdempotentCalls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	c := newClient()

	// POST without an Idempotency-Key is never retried: it could charge twice.
	calls.Store(0)
	if _, err := c.Do(context.Background(), http.MethodPost, srv.URL, Options{}, nil, nil); err == nil || calls.Load() != 1 {
		t.Fatalf("POST without key: err=%v calls=%d, want 1 call and an error", err, calls.Load())
	}
	// POST with a key is retried until it succeeds.
	calls.Store(0)
	if _, err := c.Do(context.Background(), http.MethodPost, srv.URL, Options{IdempotencyKey: "k"}, nil, nil); err != nil || calls.Load() != 3 {
		t.Fatalf("POST with key: err=%v calls=%d, want success on call 3", err, calls.Load())
	}
	// GET is retried.
	calls.Store(0)
	if _, err := c.Do(context.Background(), http.MethodGet, srv.URL, Options{}, nil, nil); err != nil || calls.Load() != 3 {
		t.Fatalf("GET: err=%v calls=%d", err, calls.Load())
	}
	// A 4xx is never retried.
	calls.Store(-100)
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
	defer srv4.Close()
	calls.Store(0)
	_, _ = c.Do(context.Background(), http.MethodGet, srv4.URL, Options{}, nil, nil)
	if calls.Load() != 1 {
		t.Fatalf("4xx retried: %d calls", calls.Load())
	}
}

func TestRetriesStopWhenTheContextIsDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newClient().Do(ctx, http.MethodGet, srv.URL, Options{}, nil, nil); err == nil {
		t.Fatal("cancelled context succeeded")
	}
}

func TestResponseBodyIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"x":"`))
		chunk := make([]byte, 1<<16)
		for i := range chunk {
			chunk[i] = 'a'
		}
		for i := 0; i < 20; i++ {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`"}}`))
	}))
	defer srv.Close()
	var out map[string]any
	if _, err := newClient().Do(context.Background(), http.MethodGet, srv.URL, Options{}, nil, &out); err == nil {
		t.Fatal("a 1.3 MiB body was accepted")
	}
}

func TestForwardsTraceparentFromContext(t *testing.T) {
	var seenTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTraceparent = r.Header.Get("traceparent")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	want := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	sc, ok := telemetry.ParseTraceparent(want)
	if !ok {
		t.Fatal("failed to parse traceparent")
	}
	ctx := telemetry.WithSpanContext(context.Background(), sc)
	if _, err := newClient().Do(ctx, http.MethodGet, srv.URL, Options{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if seenTraceparent != want {
		t.Fatalf("forwarded traceparent = %q, want %q", seenTraceparent, want)
	}
}
