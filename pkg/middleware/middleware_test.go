package middleware

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

func TestChainRecoversPanicWithCorrelationID(t *testing.T) {
	handler := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret detail") }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Correlation-ID", "request-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var body response.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 500 || body.Code != "INTERNAL_ERROR" || body.Message != "Internal server error" || body.CorrelationID != "request-123" {
		t.Fatalf("unexpected error response: status=%d body=%+v", w.Code, body)
	}
}

func TestRecoveryAbortsAfterResponseIsCommitted(t *testing.T) {
	handler := Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("unexpected error")
	}))
	w := httptest.NewRecorder()
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("expected aborted response, got %v", got)
		}
		if w.Body.String() != "partial" {
			t.Fatalf("unexpected body: %s", w.Body.String())
		}
	}()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestCorrelationIDReplacesInvalidHeader(t *testing.T) {
	handler := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Correlation-ID") == "invalid value" || r.Header.Get("X-Correlation-ID") == "" {
			t.Error("invalid correlation ID was not replaced")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Correlation-ID", "invalid value")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Header().Get("X-Correlation-ID") != req.Header.Get("X-Correlation-ID") {
		t.Error("response and request correlation IDs differ")
	}
}

func TestCorrelationIDIsAvailableInTheRequestContext(t *testing.T) {
	var got string
	h := CorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = CorrelationIDFrom(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Correlation-ID", "abc-123")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "abc-123" {
		t.Fatalf("context correlation id = %q", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got == "" || got != rec.Header().Get("X-Correlation-ID") {
		t.Fatalf("generated id %q does not match header %q", got, rec.Header().Get("X-Correlation-ID"))
	}
}

func TestAccessLogIncludesCorrelationIDAndSkipsHealthyProbes(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevOut)

	handler := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthReq.Header.Set("X-Correlation-ID", "probe-1")
	handler.ServeHTTP(httptest.NewRecorder(), healthReq)
	if buf.Len() != 0 {
		t.Fatalf("expected healthy /health probe to be skipped, got %q", buf.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/boom?token=secret", nil)
	req.Header.Set("X-Correlation-ID", "trace-stg-42")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	logged := buf.String()
	if !strings.Contains(logged, "correlationId=trace-stg-42") || !strings.Contains(logged, "status=409") || !strings.Contains(logged, "path=/boom") {
		t.Fatalf("expected structured access log line, got %q", logged)
	}
	if strings.Contains(logged, "secret") {
		t.Fatalf("expected query params to be omitted from access log, got %q", logged)
	}
}

func TestAccessLogAndRecoveryRedactPII(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevOut)

	handler := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "panic") {
			panic(`lookup failed email=lan@example.com phone=0901234567 address="123 Le Loi"`)
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/users/lan@example.com/0901234567", nil)
	req1.Header.Set("X-Correlation-ID", "trace-pii-1")
	handler.ServeHTTP(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil)
	req2.Header.Set("X-Correlation-ID", "trace-pii-2")
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	logged := buf.String()
	for _, secret := range []string{"lan@example.com", "0901234567", "123 Le Loi"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("middleware log leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "l***@example.com") || !strings.Contains(logged, "***67") {
		t.Fatalf("expected masked PII in logs: %s", logged)
	}
}
