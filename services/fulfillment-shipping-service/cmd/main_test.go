package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAnswersWithoutStores(t *testing.T) {
	rec := httptest.NewRecorder()
	NewServer().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestTheOldUnauthenticatedStubRoutesAreGone(t *testing.T) {
	for _, path := range []string{"/api/v1/shipments/S-001/handover", "/api/v1/shipments/S-001/stop-for-cancel"} {
		rec := httptest.NewRecorder()
		NewServer().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d", path, rec.Code)
		}
	}
}
