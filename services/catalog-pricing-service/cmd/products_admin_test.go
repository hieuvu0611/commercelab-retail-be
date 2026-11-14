package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

type productEnv struct {
	*categoryEnv
	category domain.Category
}

func newProductEnv(t *testing.T) *productEnv {
	t.Helper()
	authtest.Configure(t)
	catStore := domain.NewMemoryCategoryStore()
	cats := domain.NewCategoryService(catStore)
	prods := domain.NewProductService(domain.NewMemoryProductStore(), cats)
	srv := NewServerWithDeps(repository.NewCatalogPricingRepository(nil, nil), false, false, Deps{Categories: cats, Products: prods})
	env := &productEnv{categoryEnv: &categoryEnv{srv: srv, store: catStore}}
	env.category = env.create(t, "", "City", "city")
	return env
}

// doWith sends a request with an optional If-Match header.
func (e *productEnv) doWith(method, path, bearer, body, ifMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

type productResp struct {
	Data domain.ProductView `json:"data"`
}

func (e *productEnv) createProduct(t *testing.T, slug string) domain.ProductView {
	t.Helper()
	body := `{"slug":"` + slug + `","name":"Product ` + slug + `","primaryCategoryId":"` + e.category.ID + `","variantAxes":["color","capacity"]}`
	rec := e.do(http.MethodPost, "/api/v1/admin/products", token(t, "PRODUCT_STAFF"), body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create product: %d %s", rec.Code, rec.Body)
	}
	return decode[productResp](t, rec).Data
}

func TestAdminProductEndpointsRequireProductStaff(t *testing.T) {
	e := newProductEnv(t)
	p := e.createProduct(t, "city-backpack")
	calls := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/admin/products", `{"slug":"x","name":"X"}`},
		{http.MethodGet, "/api/v1/admin/products", ""},
		{http.MethodGet, "/api/v1/admin/products/" + p.ID, ""},
		{http.MethodPatch, "/api/v1/admin/products/" + p.ID, `{"name":"Y","version":1}`},
		{http.MethodPost, "/api/v1/admin/products/" + p.ID + "/skus", `{"skuId":"SKU-1","attributes":{},"version":1}`},
		{http.MethodPatch, "/api/v1/admin/products/" + p.ID + "/skus/SKU-1", `{"version":1}`},
		{http.MethodDelete, "/api/v1/admin/products/" + p.ID + "/skus/SKU-1", ""},
	}
	for _, c := range calls {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			if rec := e.do(c.method, c.path, "", c.body); rec.Code != http.StatusUnauthorized {
				t.Fatalf("no token: %d", rec.Code)
			}
			for _, role := range []string{"CUSTOMER", "MARKETING", "ACCESS_ADMIN"} {
				if rec := e.do(c.method, c.path, token(t, role), c.body); rec.Code != http.StatusForbidden {
					t.Fatalf("role %s: %d", role, rec.Code)
				}
			}
		})
	}
}

func TestAdminProductCreateAndRead(t *testing.T) {
	e := newProductEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	p := e.createProduct(t, "city-backpack")
	if p.Status != domain.ProductDraft || p.Version != 1 || p.CreatedBy != "u-1" || p.SKUs == nil {
		t.Fatalf("p = %+v", p)
	}
	rec := e.do(http.MethodGet, "/api/v1/admin/products/"+p.ID, staff, "")
	if rec.Code != 200 || rec.Header().Get("ETag") != `"1"` || decode[productResp](t, rec).Data.Slug != "city-backpack" {
		t.Fatalf("get: %d etag=%q %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if rec := e.do(http.MethodGet, "/api/v1/admin/products/nope", staff, ""); rec.Code != 404 || decode[errorBody](t, rec).Code != "PRODUCT_NOT_FOUND" {
		t.Fatalf("unknown: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminProductCreateValidation(t *testing.T) {
	e := newProductEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	e.createProduct(t, "taken")
	tests := []struct {
		name, body, code string
		status           int
	}{
		{"missing name and bad slug", `{"slug":"Bad Slug"}`, "VALIDATION_FAILED", 400},
		{"unknown field", `{"slug":"x","name":"X","status":"PUBLISHED"}`, "INVALID_JSON", 400},
		{"unknown category", `{"slug":"x","name":"X","primaryCategoryId":"nope"}`, "VALIDATION_FAILED", 400},
		{"duplicate slug", `{"slug":"taken","name":"X"}`, "PRODUCT_SLUG_TAKEN", 409},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(http.MethodPost, "/api/v1/admin/products", staff, tc.body)
			if rec.Code != tc.status || decode[errorBody](t, rec).Code != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestAdminProductPatchOptimisticConcurrency(t *testing.T) {
	e := newProductEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	p := e.createProduct(t, "city-backpack")
	path := "/api/v1/admin/products/" + p.ID

	// Version from the If-Match header.
	rec := e.doWith(http.MethodPatch, path, staff, `{"name":"Renamed"}`, `"1"`)
	got := decode[productResp](t, rec).Data
	if rec.Code != 200 || got.Name != "Renamed" || got.Version != 2 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("header version: %d %s", rec.Code, rec.Body)
	}
	// Version from the body.
	rec = e.do(http.MethodPatch, path, staff, `{"brand":"Acme","version":2}`)
	if got = decode[productResp](t, rec).Data; rec.Code != 200 || got.Brand != "Acme" || got.Version != 3 {
		t.Fatalf("body version: %d %s", rec.Code, rec.Body)
	}
	// Stale version.
	rec = e.doWith(http.MethodPatch, path, staff, `{"name":"Stale"}`, "1")
	if rec.Code != 409 || decode[errorBody](t, rec).Code != "VERSION_CONFLICT" {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}
	// Missing and malformed versions.
	rec = e.do(http.MethodPatch, path, staff, `{"name":"NoVersion"}`)
	if rec.Code != http.StatusPreconditionRequired || decode[errorBody](t, rec).Code != "VERSION_REQUIRED" {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body)
	}
	if rec = e.doWith(http.MethodPatch, path, staff, `{"name":"x"}`, `"abc"`); rec.Code != 400 {
		t.Fatalf("malformed: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminSKUEndpoints(t *testing.T) {
	e := newProductEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	p := e.createProduct(t, "city-backpack")
	skus := "/api/v1/admin/products/" + p.ID + "/skus"

	rec := e.do(http.MethodPost, skus, staff, `{"skuId":"SKU-CITY-BLACK","attributes":{"color":"Black","capacity":"20 L"},"version":1}`)
	got := decode[productResp](t, rec).Data
	if rec.Code != http.StatusCreated || len(got.SKUs) != 1 || got.Version != 2 {
		t.Fatalf("create sku: %d %s", rec.Code, rec.Body)
	}
	// Duplicate combination, missing axis, duplicate code.
	rec = e.do(http.MethodPost, skus, staff, `{"skuId":"SKU-OTHER","attributes":{"color":"black","capacity":"20 l"},"version":2}`)
	if rec.Code != 400 || len(decode[errorBody](t, rec).Errors["attributes"]) == 0 {
		t.Fatalf("duplicate combination: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(http.MethodPost, skus, staff, `{"skuId":"SKU-OTHER","attributes":{"color":"Red"},"version":2}`)
	if rec.Code != 400 || len(decode[errorBody](t, rec).Errors["attributes.capacity"]) == 0 {
		t.Fatalf("missing axis: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(http.MethodPost, skus, staff, `{"skuId":"SKU-CITY-BLACK","attributes":{"color":"Red","capacity":"20 L"},"version":2}`)
	if rec.Code != 409 || decode[errorBody](t, rec).Code != "SKU_CODE_TAKEN" {
		t.Fatalf("duplicate code: %d %s", rec.Code, rec.Body)
	}
	// Stale version.
	rec = e.do(http.MethodPost, skus, staff, `{"skuId":"SKU-RED","attributes":{"color":"Red","capacity":"20 L"},"version":1}`)
	if rec.Code != 409 || decode[errorBody](t, rec).Code != "VERSION_CONFLICT" {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}

	// Patch and delete.
	rec = e.doWith(http.MethodPatch, skus+"/SKU-CITY-BLACK", staff, `{"isActive":false}`, "2")
	got = decode[productResp](t, rec).Data
	if rec.Code != 200 || got.SKUs[0].IsActive || got.Version != 3 {
		t.Fatalf("patch sku: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do(http.MethodDelete, skus+"/SKU-CITY-BLACK", staff, ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("delete without version: %d", rec.Code)
	}
	rec = e.doWith(http.MethodDelete, skus+"/SKU-CITY-BLACK", staff, "", `"3"`)
	if got = decode[productResp](t, rec).Data; rec.Code != 200 || len(got.SKUs) != 0 || got.Version != 4 {
		t.Fatalf("delete sku: %d %s", rec.Code, rec.Body)
	}
	if rec = e.doWith(http.MethodDelete, skus+"/SKU-CITY-BLACK", staff, "", "4"); rec.Code != 404 || decode[errorBody](t, rec).Code != "SKU_NOT_FOUND" {
		t.Fatalf("delete twice: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminProductList(t *testing.T) {
	e := newProductEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	for _, s := range []string{"alpha", "beta", "gamma"} {
		e.createProduct(t, s)
	}
	type listResp struct {
		Data     []domain.Product `json:"data"`
		Metadata struct {
			Total     int64 `json:"total"`
			Page      int   `json:"page"`
			PageCount int   `json:"pageCount"`
			Limit     int   `json:"limit"`
		} `json:"metadata"`
	}
	rec := e.do(http.MethodGet, "/api/v1/admin/products?page=2&limit=2", staff, "")
	got := decode[listResp](t, rec)
	if rec.Code != 200 || len(got.Data) != 1 || got.Metadata.Total != 3 || got.Metadata.PageCount != 2 || got.Metadata.Page != 2 || got.Metadata.Limit != 2 {
		t.Fatalf("page 2: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do(http.MethodGet, "/api/v1/admin/products?q=.*", staff, ""); decode[listResp](t, rec).Metadata.Total != 0 {
		t.Fatalf("regex search matched: %s", rec.Body)
	}
	if rec = e.do(http.MethodGet, "/api/v1/admin/products?status=BOGUS", staff, ""); rec.Code != 400 {
		t.Fatalf("bad status: %d", rec.Code)
	}
	if rec = e.do(http.MethodGet, "/api/v1/admin/products?page=abc", staff, ""); rec.Code != 400 {
		t.Fatalf("bad page: %d", rec.Code)
	}
}
