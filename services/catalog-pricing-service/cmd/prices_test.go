package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

// newPriceEnv builds a server whose product, SKU and price stores all live in memory, with one draft product
// that has the SKU SKU-CITY-BLACK.
func newPriceEnv(t *testing.T) (*productEnv, domain.ProductView) {
	t.Helper()
	authtest.Configure(t)
	catStore := domain.NewMemoryCategoryStore()
	cats := domain.NewCategoryService(catStore)
	repo := repository.NewCatalogPricingRepository(nil, nil)
	prods := domain.NewProductService(domain.NewMemoryProductStore(), cats).WithPricing(repo, repo)
	srv := NewServerWithDeps(repo, false, false, Deps{Categories: cats, Products: prods})
	env := &productEnv{categoryEnv: &categoryEnv{srv: srv, store: catStore}}
	env.category = env.create(t, "", "City", "city")
	p := env.createProduct(t, "city-backpack")
	rec := env.do(http.MethodPost, "/api/v1/admin/products/"+p.ID+"/skus", token(t, "PRODUCT_STAFF"),
		`{"skuId":"SKU-CITY-BLACK","attributes":{"color":"Black","capacity":"20 L"},"version":1}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create sku: %d %s", rec.Code, rec.Body)
	}
	return env, decode[productResp](t, rec).Data
}

type priceResp struct {
	Data domain.PriceView `json:"data"`
}

func TestPriceEndpointsAreMarketingOnly(t *testing.T) {
	e, _ := newPriceEnv(t)
	for _, c := range []struct{ method, body string }{{http.MethodPut, `{"unitPriceVnd":1000}`}, {http.MethodGet, ""}} {
		path := "/api/v1/admin/skus/SKU-CITY-BLACK/price"
		if rec := e.do(c.method, path, "", c.body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token: %d", c.method, rec.Code)
		}
		for _, role := range []string{"PRODUCT_STAFF", "CUSTOMER", "ACCESS_ADMIN"} {
			if rec := e.do(c.method, path, token(t, role), c.body); rec.Code != http.StatusForbidden {
				t.Fatalf("%s as %s: %d", c.method, role, rec.Code)
			}
		}
	}
}

func TestSetPriceAndReadHistory(t *testing.T) {
	e, _ := newPriceEnv(t)
	mkt := token(t, "MARKETING")
	path := "/api/v1/admin/skus/SKU-CITY-BLACK/price"

	rec := e.do(http.MethodPut, path, mkt, `{"unitPriceVnd":300000}`)
	got := decode[priceResp](t, rec).Data
	if rec.Code != 200 || got.Current == nil || got.Current.UnitPriceVND != 300000 || got.Current.ChangedBy != "u-1" || len(got.History) != 1 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	future := time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339)
	rec = e.do(http.MethodPut, path, mkt, `{"unitPriceVnd":350000,"effectiveFrom":"`+future+`"}`)
	got = decode[priceResp](t, rec).Data
	if rec.Code != 200 || got.Current.UnitPriceVND != 300000 || len(got.History) != 2 || got.History[0].UnitPriceVND != 350000 {
		t.Fatalf("scheduled: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(http.MethodGet, path, mkt, "")
	if got = decode[priceResp](t, rec).Data; rec.Code != 200 || len(got.History) != 2 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
}

func TestSetPriceValidation(t *testing.T) {
	e, _ := newPriceEnv(t)
	mkt := token(t, "MARKETING")
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	tests := []struct {
		name, body, field string
	}{
		{"fractional amount", `{"unitPriceVnd":1000.5}`, "unitPriceVnd"},
		{"exponent amount", `{"unitPriceVnd":1e3}`, "unitPriceVnd"},
		{"missing amount", `{}`, "unitPriceVnd"},
		{"zero", `{"unitPriceVnd":0}`, "unitPriceVnd"},
		{"negative", `{"unitPriceVnd":-5}`, "unitPriceVnd"},
		{"too large", `{"unitPriceVnd":1000000001}`, "unitPriceVnd"},
		{"backdated", `{"unitPriceVnd":1000,"effectiveFrom":"` + past + `"}`, "effectiveFrom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(http.MethodPut, "/api/v1/admin/skus/SKU-CITY-BLACK/price", mkt, tc.body)
			if rec.Code != 400 || len(decode[errorBody](t, rec).Errors[tc.field]) == 0 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	if rec := e.do(http.MethodPut, "/api/v1/admin/skus/SKU-NOPE/price", mkt, `{"unitPriceVnd":1000}`); rec.Code != 404 || decode[errorBody](t, rec).Code != "SKU_NOT_FOUND" {
		t.Fatalf("unknown SKU: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPut, "/api/v1/admin/skus/SKU-CITY-BLACK/price", mkt, `{"unitPriceVnd":1000,"extra":1}`); rec.Code != 400 {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestQuoteIgnoresClientPricesAndRejectsClosedSKUs(t *testing.T) {
	authtest.Configure(t)
	repo := repository.NewCatalogPricingRepository(nil, nil)
	srv := NewServer(repo, false, false)
	post := func(body string) (int, string) {
		req := newJSONRequest(http.MethodPost, "/api/v1/quotes", body)
		rec := serve(srv, req)
		return rec.Code, rec.Body.String()
	}

	// SC01 with client-supplied prices and names that must be ignored: 2 x 300,000 + 100,000, 10% off, free shipping
	// is not reached, so 630,000 + 30,000 = 660,000.
	code, body := post(`{"quoteId":"Q-IGNORE","voucherCode":"WELCOME10","items":[
		{"skuId":"SKU-BALO-BLACK-20L","quantity":2,"unitPriceVnd":1,"skuName":"Hacked"},
		{"skuId":"SKU-BOTTLE-BLUE-500ML","quantity":1,"unitPriceVnd":1}]}`)
	if code != http.StatusCreated || !strings.Contains(body, `"totalPayableVnd":660000`) || strings.Contains(body, "Hacked") {
		t.Fatalf("quote with forged prices: %d %s", code, body)
	}

	if err := repo.SetOrderable(nilCtx(), "PROD-BALO-CITY", nil); err != nil {
		t.Fatal(err)
	}
	code, body = post(`{"items":[{"skuId":"SKU-BALO-BLACK-20L","quantity":1}]}`)
	if code != http.StatusConflict || !strings.Contains(body, "SKU_NOT_ORDERABLE") {
		t.Fatalf("unpublished SKU: %d %s", code, body)
	}
	code, body = post(`{"items":[{"skuId":"SKU-NOPE","quantity":1}]}`)
	if code != http.StatusNotFound || !strings.Contains(body, "SKU_NOT_FOUND") {
		t.Fatalf("unknown SKU: %d %s", code, body)
	}
}

type priceListResp struct {
	Data []domain.PriceRow `json:"data"`
	Meta struct {
		Total int64 `json:"total"`
	} `json:"metadata"`
}

func TestPriceListShowsEverySKUWithItsCurrentPrice(t *testing.T) {
	e, _ := newPriceEnv(t)
	mkt := token(t, "MARKETING")
	for _, role := range []string{"PRODUCT_STAFF", "CUSTOMER"} {
		if rec := e.do(http.MethodGet, "/api/v1/admin/prices", token(t, role), ""); rec.Code != http.StatusForbidden {
			t.Fatalf("as %s: %d", role, rec.Code)
		}
	}
	rec := e.do(http.MethodGet, "/api/v1/admin/prices", mkt, "")
	list := decode[priceListResp](t, rec)
	if rec.Code != 200 || len(list.Data) != 1 || list.Data[0].UnitPriceVND != nil || list.Data[0].ProductName == "" {
		t.Fatalf("unpriced: %d %s", rec.Code, rec.Body)
	}
	e.do(http.MethodPut, "/api/v1/admin/skus/SKU-CITY-BLACK/price", mkt, `{"unitPriceVnd":300000}`)
	rec = e.do(http.MethodGet, "/api/v1/admin/prices?q=city", mkt, "")
	list = decode[priceListResp](t, rec)
	if len(list.Data) != 1 || list.Data[0].UnitPriceVND == nil || *list.Data[0].UnitPriceVND != 300000 || list.Data[0].SkuID != "SKU-CITY-BLACK" {
		t.Fatalf("priced: %s", rec.Body)
	}
	rec = e.do(http.MethodGet, "/api/v1/admin/prices?q=nothing-matches", mkt, "")
	if list = decode[priceListResp](t, rec); len(list.Data) != 0 {
		t.Fatalf("filter: %s", rec.Body)
	}
}
