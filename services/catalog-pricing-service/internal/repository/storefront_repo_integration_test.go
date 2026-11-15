//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

type flatPrices map[string]int64

func (f flatPrices) OrderablePrices(_ context.Context, ids []string) (map[string]domain.SKUPrice, error) {
	out := map[string]domain.SKUPrice{}
	for _, id := range ids {
		if p, ok := f[id]; ok {
			out[id] = domain.SKUPrice{UnitPriceVND: p, VatRatePct: 10}
		}
	}
	return out, nil
}

func TestMongoStorefrontQueries(t *testing.T) {
	catSvc, cats, store, ctx := newMongoCategoryFixture(t)
	products, err := NewMongoProductStore(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	backpacks, _ := catSvc.Create(ctx, domain.CreateCategoryInput{Name: "Backpacks", Slug: "backpacks"})
	city, _ := catSvc.Create(ctx, domain.CreateCategoryInput{ParentID: backpacks.ID, Name: "City", Slug: "city"})
	bottles, _ := catSvc.Create(ctx, domain.CreateCategoryInput{Name: "Bottles", Slug: "bottles"})

	prices := flatPrices{}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	add := func(i int, slug, name, categoryID, color string, price int64, status domain.ProductStatus) {
		t.Helper()
		at := base.Add(time.Duration(i) * time.Minute)
		p := domain.Product{
			ID: "p-" + slug, Slug: slug, Name: name, Description: name + " for daily use", Brand: "Acme", PrimaryCategoryID: categoryID,
			CategoryIDs: []string{categoryID}, VariantAxes: []string{"color"}, Specs: map[string]any{}, Images: []domain.ProductImage{{ID: "i", Key: "k"}},
			Status: status, Version: 1, CreatedAt: at, UpdatedAt: at, PublishedAt: &at,
		}
		if err := products.InsertProduct(ctx, p); err != nil {
			t.Fatal(err)
		}
		code := fmt.Sprintf("SKU-%s", slug)
		if err := products.InsertSKU(ctx, domain.SKU{SkuID: code, ProductID: p.ID, Attributes: map[string]string{"color": color}, IsActive: true}); err != nil {
			t.Fatal(err)
		}
		prices[code] = price
	}
	add(1, "city-pack", "City Backpack", city.ID, "Black", 300000, domain.ProductPublished)
	add(2, "mini-pack", "Mini Backpack", city.ID, "Red", 150000, domain.ProductPublished)
	add(3, "steel-flask", "Steel Flask", bottles.ID, "Blue", 100000, domain.ProductPublished)
	add(4, "draft-pack", "Draft Backpack", city.ID, "Black", 1, domain.ProductDraft)

	front := domain.NewStorefrontService(products, cats, prices)
	list := func(q domain.StorefrontQuery) domain.StorefrontPage {
		t.Helper()
		page, err := front.List(ctx, q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		return page
	}
	slugs := func(p domain.StorefrontPage) []string {
		out := []string{}
		for _, it := range p.Items {
			out = append(out, it.Slug)
		}
		return out
	}
	want := func(name string, got domain.StorefrontPage, expected ...string) {
		t.Helper()
		if fmt.Sprint(slugs(got)) != fmt.Sprint(expected) || got.Total != int64(len(expected)) {
			t.Errorf("%s = %v (total %d), want %v", name, slugs(got), got.Total, expected)
		}
	}

	want("all published, newest first", list(domain.StorefrontQuery{}), "steel-flask", "mini-pack", "city-pack")
	want("descendants of backpacks", list(domain.StorefrontQuery{CategoryPath: "backpacks"}), "mini-pack", "city-pack")
	want("price asc", list(domain.StorefrontQuery{Sort: "price_asc"}), "steel-flask", "mini-pack", "city-pack")
	max := int64(200000)
	want("max price", list(domain.StorefrontQuery{MaxPrice: &max, Sort: "price_desc"}), "mini-pack", "steel-flask")
	want("attribute is case-insensitive", list(domain.StorefrontQuery{Attrs: map[string][]string{"color": {"bLaCk"}}}), "city-pack")
	want("text word search", list(domain.StorefrontQuery{Q: "backpack"}), "mini-pack", "city-pack")
	want("drafts are never found by text", list(domain.StorefrontQuery{Q: "draft"}))

	// Injection attempts match nothing.
	want("regex text", list(domain.StorefrontQuery{Q: ".*"}))
	want("regex attribute", list(domain.StorefrontQuery{Attrs: map[string][]string{"color": {".*"}}}))
	want("regex attribute anchored", list(domain.StorefrontQuery{Attrs: map[string][]string{"color": {"Bla.k"}}}))

	page2 := list(domain.StorefrontQuery{Page: 2, Limit: 2})
	if page2.Total != 3 || page2.PageCount != 2 || len(page2.Items) != 1 || page2.Items[0].Slug != "city-pack" {
		t.Fatalf("page 2 = %+v", page2)
	}

	d, err := front.Detail(ctx, "city-pack")
	if err != nil || len(d.SKUs) != 1 || d.SKUs[0].UnitPriceVND == nil || *d.SKUs[0].UnitPriceVND != 300000 || d.Availability != "unknown" {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	if _, err := front.Detail(ctx, "draft-pack"); err != domain.ErrProductNotFound {
		t.Fatalf("draft detail: %v", err)
	}
}
