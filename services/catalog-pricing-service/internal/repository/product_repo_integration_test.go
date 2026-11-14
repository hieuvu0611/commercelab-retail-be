//go:build integration

package repository

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

func TestMongoProductWorkflow(t *testing.T) {
	_, cats, store, ctx := newMongoCategoryFixture(t)
	products, err := NewMongoProductStore(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	catSvc := domain.NewCategoryService(cats)
	city, err := catSvc.Create(ctx, domain.CreateCategoryInput{Name: "City", Slug: "city"})
	if err != nil {
		t.Fatal(err)
	}
	svc := domain.NewProductService(products, cats)

	v, err := svc.CreateProduct(ctx, "staff-1", domain.ProductInput{
		Slug: "city-backpack", Name: "City Backpack", PrimaryCategoryID: city.ID, VariantAxes: []string{"color", "capacity"},
		Specs: map[string]any{"capacity_liters": 20, "dims_cm": map[string]any{"max_height": 45}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || fmt.Sprint(v.Specs["capacityLiters"]) != "20" {
		t.Fatalf("view = %+v", v.Product)
	}

	// Unique slug.
	if _, err := svc.CreateProduct(ctx, "staff-1", domain.ProductInput{Slug: "city-backpack", Name: "Other"}); !errors.Is(err, domain.ErrProductSlugTaken) {
		t.Fatalf("slug: %v", err)
	}

	// Optimistic concurrency: only one of two writers with the same version wins.
	name := "City Backpack 2"
	if _, err := svc.UpdateProduct(ctx, "a", v.ID, 1, domain.ProductPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateProduct(ctx, "b", v.ID, 1, domain.ProductPatch{Name: &name}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale write: %v", err)
	}
	if _, err := svc.UpdateProduct(ctx, "b", "nope", 1, domain.ProductPatch{Name: &name}); !errors.Is(err, domain.ErrProductNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	// SKUs: combination unique per product, code unique globally.
	cur, _ := svc.GetProduct(ctx, v.ID)
	_, err = svc.CreateSKU(ctx, "a", v.ID, cur.Version, domain.SKUInput{SkuID: "SKU-CITY-BLACK-20L", Attributes: map[string]string{"color": "Black", "capacity": "20 L"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateProduct(ctx, "a", domain.ProductInput{Slug: "other", Name: "Other", VariantAxes: []string{"color"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSKU(ctx, "a", other.ID, other.Version, domain.SKUInput{SkuID: "SKU-CITY-BLACK-20L", Attributes: map[string]string{"color": "Black"}}); !errors.Is(err, domain.ErrSKUCodeTaken) {
		t.Fatalf("global sku code: %v", err)
	}
	if err := products.InsertSKU(ctx, domain.SKU{SkuID: "SKU-CITY-BLACK-20L", ProductID: other.ID}); !errors.Is(err, domain.ErrSKUCodeTaken) {
		t.Fatalf("unique _id index: %v", err)
	}

	// Editing a published product writes a draft revision and leaves the live document alone.
	live, _ := products.GetProduct(ctx, v.ID)
	live.Status = domain.ProductPublished
	if err := products.SaveProduct(ctx, live, live.Version); err != nil {
		t.Fatal(err)
	}
	brand := "Acme"
	drafted, err := svc.UpdateProduct(ctx, "a", v.ID, live.Version, domain.ProductPatch{Brand: &brand})
	if err != nil || !drafted.HasDraft || drafted.Brand != "Acme" || drafted.Version != live.Version+1 {
		t.Fatalf("draft = %+v, %v", drafted.Product, err)
	}
	if again, _ := products.GetProduct(ctx, v.ID); again.Brand != "" {
		t.Fatalf("live product changed: %+v", again)
	}
	if _, err := svc.UpdateProduct(ctx, "a", v.ID, live.Version, domain.ProductPatch{Brand: &brand}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale draft write: %v", err)
	}

	// List: status filter, pagination, regex characters are literal.
	page, total, err := svc.ListProducts(ctx, domain.ProductFilter{Page: 1, Limit: 1})
	if err != nil || total != 2 || len(page) != 1 {
		t.Fatalf("page = %d of %d, %v", len(page), total, err)
	}
	if _, total, _ = svc.ListProducts(ctx, domain.ProductFilter{Q: ".*", Page: 1, Limit: 10}); total != 0 {
		t.Fatalf("regex query matched %d products", total)
	}
	if _, total, _ = svc.ListProducts(ctx, domain.ProductFilter{Q: "CITY", Page: 1, Limit: 10}); total != 1 {
		t.Fatalf("case-insensitive search matched %d", total)
	}
	if _, total, _ = svc.ListProducts(ctx, domain.ProductFilter{Status: domain.ProductPublished, Page: 1, Limit: 10}); total != 1 {
		t.Fatalf("status filter matched %d", total)
	}

	// Categories with products cannot be deleted (the store counts both category fields).
	if n, err := cats.CountProducts(ctx, city.ID); err != nil || n != 1 {
		t.Fatalf("category product count = %d, %v", n, err)
	}
}
