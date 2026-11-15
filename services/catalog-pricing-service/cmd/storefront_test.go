package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

// newStorefrontServer returns a server with two published products (priced through the memory repository).
func newStorefrontServer(t *testing.T) http.Handler {
	t.Helper()
	authtest.Configure(t)
	ctx := context.Background()
	catStore := domain.NewMemoryCategoryStore()
	cats := domain.NewCategoryService(catStore)
	backpacks, _ := cats.Create(ctx, domain.CreateCategoryInput{Name: "Backpacks", Slug: "backpacks"})
	city, _ := cats.Create(ctx, domain.CreateCategoryInput{ParentID: backpacks.ID, Name: "City", Slug: "city"})
	products := domain.NewMemoryProductStore()
	repo := repository.NewCatalogPricingRepository(nil, nil)
	prods := domain.NewProductService(products, cats).WithPricing(repo, repo)
	prices := domain.NewPriceService(prods, repo)

	add := func(slug, name, code, color string, price int64, at time.Time) {
		p := domain.Product{
			ID: "p-" + slug, Slug: slug, Name: name, Description: "d", PrimaryCategoryID: city.ID, CategoryIDs: []string{city.ID},
			VariantAxes: []string{"color"}, Specs: map[string]any{"material": "nylon"},
			Images: []domain.ProductImage{{ID: "i", Key: "https://img.test/" + slug + ".png"}},
			Status: domain.ProductPublished, Version: 1, PublishedAt: &at,
		}
		if err := products.InsertProduct(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := products.InsertSKU(ctx, domain.SKU{SkuID: code, ProductID: p.ID, Attributes: map[string]string{"color": color}, IsActive: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := prices.SetPrice(ctx, "m", code, price, nil); err != nil {
			t.Fatal(err)
		}
		if err := repo.SetOrderable(ctx, p.ID, []string{code}); err != nil {
			t.Fatal(err)
		}
	}
	add("city-pack", "City Backpack", "SKU-CITY", "Black", 300000, time.Now().Add(-2*time.Hour))
	add("mini-pack", "Mini Backpack", "SKU-MINI", "Red", 150000, time.Now().Add(-time.Hour))
	return NewServerWithDeps(repo, false, false, Deps{Categories: cats, Products: prods, Prices: prices, Storefront: domain.NewStorefrontService(products, cats, repo)})
}

type listResp struct {
	Data []struct {
		Slug        string `json:"slug"`
		ImageURL    string `json:"imageUrl"`
		MinPriceVND int64  `json:"minPriceVnd"`
	} `json:"data"`
	Metadata struct {
		Total     int64 `json:"total"`
		Page      int   `json:"page"`
		PageCount int   `json:"pageCount"`
		Limit     int   `json:"limit"`
	} `json:"metadata"`
}

func get(srv http.Handler, path string) (int, []byte) {
	rec := serve(srv, newJSONRequest(http.MethodGet, path, ""))
	return rec.Code, rec.Body.Bytes()
}

func TestStorefrontListIsPublicWithRealMetadata(t *testing.T) {
	srv := newStorefrontServer(t)
	rec := serve(srv, newJSONRequest(http.MethodGet, "/api/v1/products?category=backpacks&limit=1&page=2", ""))
	got := decode[listResp](t, rec)
	if rec.Code != 200 || len(got.Data) != 1 || got.Data[0].Slug != "city-pack" || got.Metadata.Total != 2 || got.Metadata.PageCount != 2 ||
		got.Metadata.Page != 2 || got.Metadata.Limit != 1 || got.Data[0].ImageURL != "https://img.test/city-pack.png" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestStorefrontListFilters(t *testing.T) {
	srv := newStorefrontServer(t)
	tests := []struct {
		query string
		want  []string
	}{
		{"?sort=price_asc", []string{"mini-pack", "city-pack"}},
		{"?sort=price_desc", []string{"city-pack", "mini-pack"}},
		{"?minPrice=200000", []string{"city-pack"}},
		{"?maxPrice=200000", []string{"mini-pack"}},
		{"?attr.color=red", []string{"mini-pack"}},
		{"?attr.color=red,black", []string{"mini-pack", "city-pack"}},
		{"?q=mini", []string{"mini-pack"}},
		{"?q=.*", nil},
		{"?attr.color=.*", nil},
		{"?category=backpacks/city", []string{"mini-pack", "city-pack"}},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			code, body := get(srv, "/api/v1/products"+tc.query)
			rec := httpRec(code, body)
			got := decode[listResp](t, rec)
			var slugs []string
			for _, d := range got.Data {
				slugs = append(slugs, d.Slug)
			}
			if code != 200 || len(slugs) != len(tc.want) || (len(slugs) > 0 && slugs[0] != tc.want[0]) {
				t.Fatalf("%s -> %d %s", tc.query, code, body)
			}
		})
	}
}

func TestStorefrontListErrors(t *testing.T) {
	srv := newStorefrontServer(t)
	for query, want := range map[string]int{
		"?sort=random": 400, "?limit=1000": 400, "?page=abc": 400, "?minPrice=1.5": 400, "?minPrice=9&maxPrice=1": 400,
		"?attr.$where=x": 400, "?category=nope": 404,
	} {
		if code, body := get(srv, "/api/v1/products"+query); code != want {
			t.Errorf("%s -> %d %s, want %d", query, code, body, want)
		}
	}
}

func TestStorefrontDetail(t *testing.T) {
	srv := newStorefrontServer(t)
	type detailResp struct {
		Data domain.StorefrontDetail `json:"data"`
	}
	rec := serve(srv, newJSONRequest(http.MethodGet, "/api/v1/products/city-pack", ""))
	got := decode[detailResp](t, rec).Data
	if rec.Code != 200 || got.Availability != "unknown" || len(got.SKUs) != 1 || *got.SKUs[0].UnitPriceVND != 300000 ||
		got.SKUs[0].Availability != "unknown" || got.Images[0].URL == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = serve(srv, newJSONRequest(http.MethodGet, "/api/v1/products/nope", ""))
	if rec.Code != 404 || decode[errorBody](t, rec).Code != "PRODUCT_NOT_FOUND" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
