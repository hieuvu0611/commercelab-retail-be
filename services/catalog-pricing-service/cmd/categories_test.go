package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cache"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

type categoryEnv struct {
	srv   http.Handler
	cache *cache.Memory
	store *domain.MemoryCategoryStore
}

func newCategoryEnv(t *testing.T) *categoryEnv {
	t.Helper()
	authtest.Configure(t)
	store := domain.NewMemoryCategoryStore()
	mem := cache.NewMemory()
	srv := NewServerWithDeps(repository.NewCatalogPricingRepository(nil, nil), false, false, Deps{
		Categories: domain.NewCategoryService(store), Cache: mem,
	})
	return &categoryEnv{srv: srv, cache: mem, store: store}
}

func token(t *testing.T, roles ...string) string {
	t.Helper()
	tok, err := auth.SignAccessToken(auth.Identity{UserID: "u-1", Roles: roles})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *categoryEnv) do(method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

type errorBody struct {
	Code   string              `json:"code"`
	Errors map[string][]string `json:"errors"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

type categoryResp struct {
	Data domain.Category `json:"data"`
}

func (e *categoryEnv) create(t *testing.T, parentID, name, slug string) domain.Category {
	t.Helper()
	body := `{"parentId":"` + parentID + `","name":"` + name + `","slug":"` + slug + `"}`
	rec := e.do(http.MethodPost, "/api/v1/admin/categories", token(t, "PRODUCT_STAFF"), body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", slug, rec.Code, rec.Body)
	}
	return decode[categoryResp](t, rec).Data
}

func TestAdminCategoryEndpointsRequireProductStaff(t *testing.T) {
	e := newCategoryEnv(t)
	cat := e.create(t, "", "Bags", "bags")
	calls := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/admin/categories", `{"name":"X","slug":"x"}`},
		{http.MethodPatch, "/api/v1/admin/categories/" + cat.ID, `{"name":"Y"}`},
		{http.MethodPost, "/api/v1/admin/categories/" + cat.ID + "/move", `{"parentId":null}`},
		{http.MethodDelete, "/api/v1/admin/categories/" + cat.ID, ""},
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

func TestAdminCreateValidatesInput(t *testing.T) {
	e := newCategoryEnv(t)
	tests := []struct{ name, body string }{
		{"missing name", `{"slug":"ok"}`},
		{"bad slug", `{"name":"Ok","slug":"Not Ok"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(http.MethodPost, "/api/v1/admin/categories", token(t, "PRODUCT_STAFF"), tc.body)
			body := decode[errorBody](t, rec)
			if rec.Code != http.StatusBadRequest || body.Code != "VALIDATION_FAILED" || len(body.Errors) == 0 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	if rec := e.do(http.MethodPost, "/api/v1/admin/categories", token(t, "PRODUCT_STAFF"), `{"name":"x","slug":"x","bogus":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestAdminCategoryConflictsAnswer409(t *testing.T) {
	e := newCategoryEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	a := e.create(t, "", "A", "a")
	b := e.create(t, a.ID, "B", "b")
	c := e.create(t, b.ID, "C", "c")
	other := e.create(t, "", "Other", "other")
	e.create(t, "", "Dup", "dup")

	tests := []struct {
		name, method, path, body, code string
		status                         int
	}{
		{"duplicate sibling slug", http.MethodPost, "/api/v1/admin/categories", `{"name":"A2","slug":"a"}`, "CATEGORY_CONFLICT", 409},
		{"depth exceeded", http.MethodPost, "/api/v1/admin/categories", `{"parentId":"` + c.ID + `","name":"D","slug":"d"}`, "CATEGORY_DEPTH_EXCEEDED", 409},
		{"move under own child", http.MethodPost, "/api/v1/admin/categories/" + a.ID + "/move", `{"parentId":"` + b.ID + `"}`, "CATEGORY_CYCLE", 409},
		{"rename to taken slug", http.MethodPatch, "/api/v1/admin/categories/" + other.ID, `{"slug":"dup"}`, "CATEGORY_CONFLICT", 409},
		{"delete with children", http.MethodDelete, "/api/v1/admin/categories/" + a.ID, "", "CATEGORY_HAS_CHILDREN", 409},
		{"unknown parent", http.MethodPost, "/api/v1/admin/categories", `{"parentId":"nope","name":"X","slug":"x"}`, "CATEGORY_NOT_FOUND", 404},
		{"unknown id", http.MethodDelete, "/api/v1/admin/categories/nope", "", "CATEGORY_NOT_FOUND", 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(tc.method, tc.path, staff, tc.body)
			if rec.Code != tc.status || decode[errorBody](t, rec).Code != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}

	e.store.SetProductCount(c.ID, 1)
	rec := e.do(http.MethodDelete, "/api/v1/admin/categories/"+c.ID, staff, "")
	if rec.Code != http.StatusConflict || decode[errorBody](t, rec).Code != "CATEGORY_HAS_PRODUCTS" {
		t.Fatalf("delete with products: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminCategoryLifecycle(t *testing.T) {
	e := newCategoryEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	a := e.create(t, "", "A", "a")
	b := e.create(t, "", "B", "b")

	rec := e.do(http.MethodPatch, "/api/v1/admin/categories/"+a.ID, staff, `{"name":"Alpha","slug":"alpha","sortOrder":3,"isActive":false}`)
	got := decode[categoryResp](t, rec).Data
	if rec.Code != 200 || got.Name != "Alpha" || got.Path != "/alpha" || got.SortOrder != 3 || got.IsActive {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(http.MethodPost, "/api/v1/admin/categories/"+a.ID+"/move", staff, `{"parentId":"`+b.ID+`"}`)
	got = decode[categoryResp](t, rec).Data
	if rec.Code != 200 || got.Path != "/b/alpha" || got.Depth != 2 {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(http.MethodPost, "/api/v1/admin/categories/"+a.ID+"/move", staff, `{"parentId":null}`)
	if got = decode[categoryResp](t, rec).Data; rec.Code != 200 || got.Path != "/alpha" {
		t.Fatalf("move to root: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do(http.MethodDelete, "/api/v1/admin/categories/"+a.ID, staff, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}

type treeResp struct {
	Data []domain.CategoryNode `json:"data"`
}

func TestPublicTreeShowsOnlyActiveAndIsCachedUntilAWrite(t *testing.T) {
	e := newCategoryEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	backpacks := e.create(t, "", "Backpacks", "backpacks")
	city := e.create(t, backpacks.ID, "City", "city")
	hidden := e.create(t, backpacks.ID, "Hidden", "hidden")
	e.do(http.MethodPatch, "/api/v1/admin/categories/"+hidden.ID, staff, `{"isActive":false}`)

	rec := e.do(http.MethodGet, "/api/v1/categories", "", "")
	tree := decode[treeResp](t, rec).Data
	if rec.Code != 200 || len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].ID != city.ID {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body)
	}
	if !e.cache.Has(categoryTreeCacheKey) {
		t.Fatal("public tree was not cached")
	}

	// A write behind the cache's back is invisible, which proves the second read came from the cache.
	if err := e.store.Insert(context.Background(), domain.Category{ID: "late", Name: "Late", Slug: "late", Path: "/late", Depth: 1, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if got := decode[treeResp](t, e.do(http.MethodGet, "/api/v1/categories", "", "")).Data; len(got) != 1 {
		t.Fatalf("expected cached tree, got %d roots", len(got))
	}

	// An admin write invalidates the cache.
	e.create(t, "", "Bottles", "bottles")
	if e.cache.Has(categoryTreeCacheKey) {
		t.Fatal("cache must be invalidated after a write")
	}
	if got := decode[treeResp](t, e.do(http.MethodGet, "/api/v1/categories", "", "")).Data; len(got) != 3 {
		t.Fatalf("expected 3 roots after the write, got %d", len(got))
	}
}

func TestPublicCategoryByPath(t *testing.T) {
	e := newCategoryEnv(t)
	staff := token(t, "PRODUCT_STAFF")
	backpacks := e.create(t, "", "Backpacks", "backpacks")
	city := e.create(t, backpacks.ID, "City", "city")
	gone := e.create(t, backpacks.ID, "Gone", "gone")
	e.do(http.MethodPatch, "/api/v1/admin/categories/"+gone.ID, staff, `{"isActive":false}`)

	type nodeResp struct {
		Data domain.CategoryNode `json:"data"`
	}
	rec := e.do(http.MethodGet, "/api/v1/categories/backpacks/city", "", "")
	if got := decode[nodeResp](t, rec).Data; rec.Code != 200 || got.ID != city.ID {
		t.Fatalf("city: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(http.MethodGet, "/api/v1/categories/backpacks", "", "")
	if got := decode[nodeResp](t, rec).Data; rec.Code != 200 || len(got.Children) != 1 {
		t.Fatalf("backpacks: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/api/v1/categories/backpacks/gone", "/api/v1/categories/nope", "/api/v1/categories/backpack"} {
		if rec := e.do(http.MethodGet, p, "", ""); rec.Code != 404 || decode[errorBody](t, rec).Code != "CATEGORY_NOT_FOUND" {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

type brokenCache struct{}

func (brokenCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errors.New("redis down")
}
func (brokenCache) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("redis down")
}
func (brokenCache) Delete(context.Context, string) error { return errors.New("redis down") }

func TestCacheFailureDoesNotBreakCategoryEndpoints(t *testing.T) {
	authtest.Configure(t)
	srv := NewServerWithDeps(repository.NewCatalogPricingRepository(nil, nil), false, false, Deps{
		Categories: domain.NewCategoryService(domain.NewMemoryCategoryStore()), Cache: brokenCache{},
	})
	e := &categoryEnv{srv: srv}
	e.create(t, "", "Bags", "bags")
	if rec := e.do(http.MethodGet, "/api/v1/categories", "", ""); rec.Code != 200 || len(decode[treeResp](t, rec).Data) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func newJSONRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func nilCtx() context.Context { return context.Background() }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
