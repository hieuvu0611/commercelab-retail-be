package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerErrorDoesNotExposeInternalDetails(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Correlation-ID", "req-123")
	w := httptest.NewRecorder()
	Error(w, req, http.StatusBadGateway, "UPSTREAM_ERROR", "password and database address", nil)
	var got ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Message != "Upstream service error" || got.CorrelationID != "req-123" || got.Status != w.Code {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestJSONEncodingFailureAndNoContent(t *testing.T) {
	w := httptest.NewRecorder()
	JSON(w, http.StatusOK, "", make(chan int), nil)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "INTERNAL_ERROR") {
		t.Fatalf("unexpected encoding error: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	JSON(w, http.StatusNoContent, "", "ignored", nil)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("unexpected no content response: %d %s", w.Code, w.Body.String())
	}
}
