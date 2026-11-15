package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

type campaignResp struct {
	Data campaignJSON `json:"data"`
}

type campaignListResp struct {
	Data     []campaignJSON `json:"data"`
	Metadata struct {
		Total int64 `json:"total"`
		Page  int   `json:"page"`
	} `json:"metadata"`
}

type apiError struct {
	Code   string              `json:"code"`
	Errors map[string][]string `json:"errors"`
}

const createBody = `{"code":"spring-10","name":"Spring sale","discountPct":10,"totalQuota":50,"perCustomerLimit":2}`

func TestVoucherAdminIsMarketingOnly(t *testing.T) {
	srv := newTestServer(t, voucher.NewMemoryStore(voucher.DevCampaigns()...))
	calls := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/vouchers"},
		{http.MethodGet, "/api/v1/admin/vouchers"},
		{http.MethodGet, "/api/v1/admin/vouchers/WELCOME10"},
		{http.MethodPut, "/api/v1/admin/vouchers/WELCOME10"},
	}
	for _, c := range calls {
		if rec := call(srv, c.method, c.path, "", createBody); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous: %d", c.method, c.path, rec.Code)
		}
		for _, role := range []string{"CUSTOMER", "PRODUCT_STAFF", "ACCESS_ADMIN"} {
			if rec := call(srv, c.method, c.path, bearer(t, "u-1", role), createBody); rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s as %s: %d", c.method, c.path, role, rec.Code)
			}
		}
	}
}

func TestMarketingCreatesReadsListsAndPausesACampaign(t *testing.T) {
	srv := newTestServer(t, voucher.NewMemoryStore(voucher.DevCampaigns()...))
	mkt := bearer(t, "staff-marketing", "MARKETING")

	rec := call(srv, http.MethodPost, "/api/v1/admin/vouchers", mkt, createBody)
	created := decodeJSON[campaignResp](t, rec).Data
	if rec.Code != http.StatusCreated || created.Code != "SPRING-10" || created.AvailableQuota != 50 || created.Status != "ACTIVE" || created.CreatedBy != "staff-marketing" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec = call(srv, http.MethodPost, "/api/v1/admin/vouchers", mkt, createBody); rec.Code != http.StatusConflict || decodeJSON[apiError](t, rec).Code != "VOUCHER_CODE_EXISTS" {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}

	rec = call(srv, http.MethodGet, "/api/v1/admin/vouchers/spring-10", mkt, "")
	if got := decodeJSON[campaignResp](t, rec).Data; rec.Code != http.StatusOK || got.TotalQuota != 50 || got.ReservedQuota != 0 || got.ConsumedQuota != 0 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if rec = call(srv, http.MethodGet, "/api/v1/admin/vouchers/NOPE-1", mkt, ""); rec.Code != http.StatusNotFound || decodeJSON[apiError](t, rec).Code != "VOUCHER_NOT_FOUND" {
		t.Fatalf("get unknown: %d %s", rec.Code, rec.Body)
	}

	list := decodeJSON[campaignListResp](t, call(srv, http.MethodGet, "/api/v1/admin/vouchers?q=spring", mkt, ""))
	if len(list.Data) != 1 || list.Metadata.Total != 1 || list.Metadata.Page != 1 {
		t.Fatalf("list = %+v", list)
	}

	paused := `{"name":"Spring sale","discountPct":10,"totalQuota":50,"perCustomerLimit":2,"status":"PAUSED"}`
	rec = call(srv, http.MethodPut, "/api/v1/admin/vouchers/SPRING-10", mkt, paused)
	if got := decodeJSON[campaignResp](t, rec).Data; rec.Code != http.StatusOK || got.Status != "PAUSED" || got.Version != 2 {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body)
	}
	// A paused voucher is no longer eligible for customers.
	elig := call(srv, http.MethodGet, "/api/v1/vouchers/SPRING-10/eligibility", bearer(t, "user-lan", "CUSTOMER"), "")
	if got := decodeJSON[eligibilityResp](t, elig).Data; got.Eligible || got.Reason != voucher.ReasonNotActive {
		t.Fatalf("eligibility of a paused voucher: %+v", got)
	}
}

func TestVoucherAdminValidationAndConflicts(t *testing.T) {
	store := voucher.NewMemoryStore(voucher.DevCampaigns()...)
	srv := newTestServer(t, store)
	mkt := bearer(t, "staff-marketing", "MARKETING")

	bad := map[string]string{
		"missing name":  `{"code":"abc-1","discountPct":10,"totalQuota":5}`,
		"discount 0":    `{"code":"abc-1","name":"x","discountPct":0,"totalQuota":5}`,
		"quota 0":       `{"code":"abc-1","name":"x","discountPct":10,"totalQuota":0}`,
		"bad code":      `{"code":"a b","name":"x","discountPct":10,"totalQuota":5}`,
		"reversed":      `{"code":"abc-1","name":"x","discountPct":10,"totalQuota":5,"startsAt":"2026-12-02T00:00:00Z","endsAt":"2026-12-01T00:00:00Z"}`,
		"bad status":    `{"code":"abc-1","name":"x","discountPct":10,"totalQuota":5,"status":"GONE"}`,
		"unknown field": `{"code":"abc-1","name":"x","discountPct":10,"totalQuota":5,"consumedQuota":4}`,
	}
	for name, body := range bad {
		if rec := call(srv, http.MethodPost, "/api/v1/admin/vouchers", mkt, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec := call(srv, http.MethodPost, "/api/v1/admin/vouchers", mkt, `{"code":"abc-1","name":"x","discountPct":0,"totalQuota":5}`)
	if len(decodeJSON[apiError](t, rec).Errors["discountPct"]) == 0 {
		t.Fatalf("missing field error: %s", rec.Body)
	}

	// The checkout saga owns the buckets; editing must respect them.
	store.SetBuckets("WELCOME10", 3, 4)
	put := func(body string) *httptest.ResponseRecorder {
		return call(srv, http.MethodPut, "/api/v1/admin/vouchers/WELCOME10", mkt, body)
	}
	if rec := put(`{"name":"x","discountPct":20,"totalQuota":100,"perCustomerLimit":1,"status":"ACTIVE"}`); rec.Code != http.StatusConflict || decodeJSON[apiError](t, rec).Code != "VOUCHER_IN_USE" {
		t.Fatalf("discount change: %d %s", rec.Code, rec.Body)
	}
	if rec := put(`{"name":"x","discountPct":10,"totalQuota":6,"perCustomerLimit":1,"status":"ACTIVE"}`); rec.Code != http.StatusConflict || decodeJSON[apiError](t, rec).Code != "VOUCHER_QUOTA_BELOW_USE" {
		t.Fatalf("quota below use: %d %s", rec.Code, rec.Body)
	}
	if rec := put(`{"code":"OTHER","name":"x","discountPct":10,"totalQuota":100,"perCustomerLimit":1,"status":"ACTIVE"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("code change: %d", rec.Code)
	}
	if rec := call(srv, http.MethodGet, "/api/v1/admin/vouchers?status=NOPE&page=0", mkt, ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "status") {
		t.Fatalf("bad list query: %d %s", rec.Code, rec.Body)
	}
}
