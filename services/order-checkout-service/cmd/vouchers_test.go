package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

func newTestServer(t *testing.T, store *voucher.MemoryStore) http.Handler {
	t.Helper()
	authtest.Configure(t)
	return NewServer(nil, voucher.NewService(store), voucher.NewAdmin(store))
}

func bearer(t *testing.T, userID string, roles ...string) string {
	t.Helper()
	tok, err := auth.SignAccessToken(auth.Identity{UserID: userID, Roles: roles})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func call(srv http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

type eligibilityResp struct {
	Data voucher.Eligibility `json:"data"`
}

func TestEligibilityRequiresAuthentication(t *testing.T) {
	srv := newTestServer(t, voucher.NewMemoryStore(voucher.DevCampaigns()...))
	if rec := call(srv, http.MethodGet, "/api/v1/vouchers/WELCOME10/eligibility", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
}

func TestEligibilityAnswersEveryReasonWithoutReservingQuota(t *testing.T) {
	paused := voucher.Campaign{Code: "PAUSED10", DiscountPct: 10, TotalQuota: 5, PerCustomerLimit: 1, Status: voucher.StatusPaused}
	full := voucher.Campaign{Code: "FULL10", DiscountPct: 10, TotalQuota: 1, ReservedQuota: 1, PerCustomerLimit: 1, Status: voucher.StatusActive}
	limited := voucher.Campaign{Code: "ONCE10", DiscountPct: 10, TotalQuota: 5, PerCustomerLimit: 1, Status: voucher.StatusActive}
	srv := newTestServer(t, voucher.NewMemoryStore(append(voucher.DevCampaigns(), paused, full, limited)...))
	token := bearer(t, "user-lan", "CUSTOMER")

	tests := []struct {
		code     string
		eligible bool
		reason   string
		pct      int
	}{
		{"WELCOME10", true, "", 10},
		{"welcome10", true, "", 10},
		{"PAUSED10", false, voucher.ReasonNotActive, 0},
		{"FULL10", false, voucher.ReasonExhausted, 0},
		{"NOPE", false, voucher.ReasonNotFound, 0},
	}
	for _, tc := range tests {
		rec := call(srv, http.MethodGet, "/api/v1/vouchers/"+tc.code+"/eligibility", token, "")
		got := decodeJSON[eligibilityResp](t, rec).Data
		if rec.Code != http.StatusOK || got.Eligible != tc.eligible || got.Reason != tc.reason || got.DiscountPct != tc.pct {
			t.Fatalf("%s: %d %s", tc.code, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "uota") {
			t.Fatalf("%s: the response must not expose quota counts: %s", tc.code, rec.Body)
		}
	}
}

func TestEligibilityUsesTheTokenSubjectForTheCustomerLimit(t *testing.T) {
	store := voucher.NewMemoryStore(voucher.Campaign{Code: "ONCE10", DiscountPct: 10, TotalQuota: 5, PerCustomerLimit: 1, Status: voucher.StatusActive})
	store.SetUses("ONCE10", "user-lan", 1)
	srv := newTestServer(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/vouchers/ONCE10/eligibility", nil)
	req.Header.Set("Authorization", "Bearer "+bearer(t, "user-new", "CUSTOMER"))
	req.Header.Set("X-User-ID", "user-lan") // must be ignored
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if got := decodeJSON[eligibilityResp](t, rec).Data; !got.Eligible {
		t.Fatalf("an identity header must not change the customer: %+v", got)
	}
	rec = call(srv, http.MethodGet, "/api/v1/vouchers/ONCE10/eligibility", bearer(t, "user-lan", "CUSTOMER"), "")
	if got := decodeJSON[eligibilityResp](t, rec).Data; got.Eligible || got.Reason != voucher.ReasonLimitReached {
		t.Fatalf("user-lan: %+v", got)
	}
}
