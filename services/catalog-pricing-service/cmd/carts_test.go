package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cart"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

const (
	skuBackpack = "SKU-BALO-BLACK-20L"
	skuBottle   = "SKU-BOTTLE-BLUE-500ML"
)

func cartServer(t *testing.T) http.Handler {
	t.Helper()
	authtest.Configure(t)
	return NewServer(repository.NewCatalogPricingRepository(nil, nil), false, false)
}

func cartToken(t *testing.T, userID string) string {
	t.Helper()
	tok, err := auth.SignAccessToken(auth.Identity{UserID: userID, Roles: []string{"CUSTOMER"}})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func cartDo(srv http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	req := newJSONRequest(method, path, body)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return serve(srv, req)
}

type cartResp struct {
	Data struct {
		cart.View
		Skipped []cart.Skipped `json:"skipped"`
	} `json:"data"`
}

func TestCartRequiresAuthentication(t *testing.T) {
	srv := cartServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/carts/me"},
		{http.MethodPut, "/api/v1/carts/me/items/" + skuBackpack},
		{http.MethodDelete, "/api/v1/carts/me/items/" + skuBackpack},
		{http.MethodPost, "/api/v1/carts/me/merge"},
	} {
		if rec := cartDo(srv, c.method, c.path, "", `{}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: %d", c.method, c.path, rec.Code)
		}
	}
}

func TestCartAddChangeAndRemoveLines(t *testing.T) {
	srv := cartServer(t)
	alice := cartToken(t, "alice")

	rec := cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/"+skuBackpack, alice, `{"quantity":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/"+skuBottle, alice, `{"quantity":1}`)
	got := decode[cartResp](t, cartDo(srv, http.MethodGet, "/api/v1/carts/me", alice, "")).Data
	if got.SubtotalVND != 700000 || got.ItemCount != 3 || len(got.Items) != 2 {
		t.Fatalf("cart = %+v", got)
	}

	rec = cartDo(srv, http.MethodDelete, "/api/v1/carts/me/items/"+skuBottle, alice, "")
	if got = decode[cartResp](t, rec).Data; rec.Code != http.StatusOK || got.SubtotalVND != 600000 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec = cartDo(srv, http.MethodDelete, "/api/v1/carts/me/items/"+skuBottle, alice, ""); rec.Code != http.StatusOK {
		t.Fatalf("second delete must be a no-op: %d", rec.Code)
	}
}

func TestCartBelongsToTheTokenSubject(t *testing.T) {
	srv := cartServer(t)
	alice, bob := cartToken(t, "alice"), cartToken(t, "bob")
	cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/"+skuBackpack, alice, `{"quantity":3}`)

	got := decode[cartResp](t, cartDo(srv, http.MethodGet, "/api/v1/carts/me", bob, "")).Data
	if len(got.Items) != 0 {
		t.Fatalf("bob sees alice's lines: %+v", got)
	}
	// An identity header never selects the cart.
	req := newJSONRequest(http.MethodGet, "/api/v1/carts/me", "")
	req.Header.Set("Authorization", "Bearer "+bob)
	req.Header.Set("X-User-ID", "alice")
	if got = decode[cartResp](t, serve(srv, req)).Data; len(got.Items) != 0 {
		t.Fatalf("X-User-ID selected another cart: %+v", got)
	}
}

func TestCartPutValidation(t *testing.T) {
	srv := cartServer(t)
	alice := cartToken(t, "alice")
	for name, body := range map[string]string{
		"zero": `{"quantity":0}`, "over limit": `{"quantity":100}`, "fraction": `{"quantity":1.5}`,
		"missing": `{}`, "unknown field": `{"quantity":1,"unitPriceVnd":1}`,
	} {
		rec := cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/"+skuBackpack, alice, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/"+skuBackpack, alice, `{"quantity":0}`); decode[errorBody](t, rec).Code != "VALIDATION_FAILED" {
		t.Fatalf("code: %s", rec.Body)
	}
	if rec := cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/SKU-NOPE", alice, `{"quantity":1}`); rec.Code != http.StatusNotFound || decode[errorBody](t, rec).Code != "SKU_NOT_FOUND" {
		t.Fatalf("unknown sku: %d %s", rec.Code, rec.Body)
	}
	if rec := cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/bad%20sku", alice, `{"quantity":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed sku id: %d", rec.Code)
	}
}

func TestCartRejectsUnpublishedSku(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewCatalogPricingRepository(nil, nil)
	if err := repo.SetOrderable(nilCtx(), "PROD-BOTTLE-ECO", nil); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(repo, false, false)
	rec := cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/"+skuBottle, cartToken(t, "alice"), `{"quantity":1}`)
	if rec.Code != http.StatusConflict || decode[errorBody](t, rec).Code != "SKU_NOT_ORDERABLE" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCartMergeIsIdempotentAndReportsSkippedLines(t *testing.T) {
	srv := cartServer(t)
	alice := cartToken(t, "alice")
	cartDo(srv, http.MethodPut, "/api/v1/carts/me/items/"+skuBackpack, alice, `{"quantity":5}`)
	body := `{"items":[{"skuId":"` + skuBackpack + `","quantity":2},{"skuId":"` + skuBottle + `","quantity":4},{"skuId":"SKU-NOPE","quantity":1}]}`

	var got cartResp
	for range 2 {
		rec := cartDo(srv, http.MethodPost, "/api/v1/carts/me/merge", alice, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("merge: %d %s", rec.Code, rec.Body)
		}
		got = decode[cartResp](t, rec)
	}
	if got.Data.ItemCount != 9 || got.Data.SubtotalVND != 5*300000+4*100000 {
		t.Fatalf("cart = %+v", got.Data)
	}
	if len(got.Data.Skipped) != 1 || got.Data.Skipped[0].SkuID != "SKU-NOPE" || got.Data.Skipped[0].Reason != "SKU_NOT_FOUND" {
		t.Fatalf("skipped = %+v", got.Data.Skipped)
	}
}

func TestCartMergeValidation(t *testing.T) {
	srv := cartServer(t)
	alice := cartToken(t, "alice")
	var lines []string
	for range cart.MaxLines + 1 {
		lines = append(lines, `{"skuId":"`+skuBackpack+`","quantity":1}`)
	}
	for name, body := range map[string]string{
		"too many lines": `{"items":[` + strings.Join(lines, ",") + `]}`,
		"empty skuId":    `{"items":[{"skuId":"","quantity":1}]}`,
		"bad skuId":      `{"items":[{"skuId":"a b","quantity":1}]}`,
		"no items":       `{}`,
	} {
		if rec := cartDo(srv, http.MethodPost, "/api/v1/carts/me/merge", alice, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestCartStoreOutageAnswers503(t *testing.T) {
	authtest.Configure(t)
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Second, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	srv := NewServerWithDeps(repository.NewCatalogPricingRepository(nil, nil), false, false, Deps{Carts: cart.NewRedisStore(client)})

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/carts/me", ""},
		{http.MethodPut, "/api/v1/carts/me/items/" + skuBackpack, `{"quantity":1}`},
		{http.MethodDelete, "/api/v1/carts/me/items/" + skuBackpack, ""},
	} {
		rec := cartDo(srv, c.method, c.path, cartToken(t, "alice"), c.body)
		if rec.Code != http.StatusServiceUnavailable || decode[errorBody](t, rec).Code != "DEPENDENCY_UNAVAILABLE" {
			t.Fatalf("%s: %d %s", c.method, rec.Code, rec.Body)
		}
	}
}
