package peer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
)

func TestVoucherEligibilityForwardsTheTokenAndEscapesTheCode(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.EscapedPath(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"code":"WELCOME10","eligible":true,"discountPct":10}}`))
	}))
	t.Cleanup(srv.Close)

	got, err := NewVouchers(srv.URL, nil).Eligibility(context.Background(), "tok-1", "WELCOME10")
	if err != nil || !got.Eligible || got.DiscountPct != 10 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if gotAuth != "Bearer tok-1" || gotPath != "/api/v1/vouchers/WELCOME10/eligibility" {
		t.Fatalf("path %q auth %q", gotPath, gotAuth)
	}
	if _, err = NewVouchers(srv.URL, nil).Eligibility(context.Background(), "tok-1", "a/../b?x=1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/vouchers/a%2F..%2Fb%3Fx=1/eligibility" {
		t.Fatalf("code was not escaped: %q", gotPath)
	}
}

func TestVoucherEligibilityFailuresAreUnavailableDependencies(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"500":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"401":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) },
		"malformed": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{`)) },
	} {
		srv := httptest.NewServer(handler)
		_, err := NewVouchers(srv.URL, nil).Eligibility(context.Background(), "t", "CODE")
		srv.Close()
		if !dependency.IsUnavailable(err) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if _, err := NewVouchers("http://127.0.0.1:1", nil).Eligibility(context.Background(), "t", "CODE"); !dependency.IsUnavailable(err) {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestAddressExistsMapsStatusCodes(t *testing.T) {
	var gotPath string
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	client := NewAddresses(srv.URL, nil)

	if ok, err := client.Exists(context.Background(), "t", "addr-1"); !ok || err != nil || gotPath != "/api/v1/users/me/addresses/addr-1" {
		t.Fatalf("200: %v %v %q", ok, err, gotPath)
	}
	status = 404
	if ok, err := client.Exists(context.Background(), "t", "addr-1"); ok || err != nil {
		t.Fatalf("404: %v %v", ok, err)
	}
	status = 500
	if _, err := client.Exists(context.Background(), "t", "addr-1"); !dependency.IsUnavailable(err) {
		t.Fatalf("500: %v", err)
	}
}
