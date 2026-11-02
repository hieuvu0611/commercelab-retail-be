package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestWarehouseScopeFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		scope  string
		status int
	}{
		{"", http.StatusForbidden}, {"other", http.StatusForbidden},
		{"WH-HCM-01", http.StatusNoContent}, {"ALL", http.StatusNoContent},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Header.Set("X-Warehouse-Scope", tc.scope)
			w := httptest.NewRecorder()
			RequireWarehouseScope("WH-HCM-01", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}).ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d", w.Code, tc.status)
			}
		})
	}
}
