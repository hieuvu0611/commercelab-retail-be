package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

type productEnv struct {
	svc        *ProductService
	store      *MemoryProductStore
	categories *MemoryCategoryStore
}

func newProductEnv(t *testing.T) *productEnv {
	t.Helper()
	categories := NewMemoryCategoryStore()
	for _, id := range []string{"cat-city", "cat-sale"} {
		if err := categories.Insert(context.Background(), Category{ID: id, Slug: id, Path: "/" + id, Depth: 1, IsActive: true}); err != nil {
			t.Fatal(err)
		}
	}
	store := NewMemoryProductStore()
	svc := NewProductService(store, categories)
	n := 0
	svc.newID = func() string { n++; return "prod-" + string(rune('a'+n-1)) }
	svc.now = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }
	return &productEnv{svc: svc, store: store, categories: categories}
}

func (e *productEnv) newProduct(t *testing.T, slug string) ProductView {
	t.Helper()
	v, err := e.svc.CreateProduct(context.Background(), "staff-1", ProductInput{
		Slug: slug, Name: "Product " + slug, PrimaryCategoryID: "cat-city", VariantAxes: []string{"color", "capacity"},
		Specs: map[string]any{"capacity_liters": 20},
	})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return v
}

func (e *productEnv) addSKU(t *testing.T, v ProductView, code, color string) ProductView {
	t.Helper()
	out, err := e.svc.CreateSKU(context.Background(), "staff-1", v.ID, v.Version, SKUInput{
		SkuID: code, Attributes: map[string]string{"color": color, "capacity": "20 L"},
	})
	if err != nil {
		t.Fatalf("add sku %s: %v", code, err)
	}
	return out
}

func (e *productEnv) publishDirectly(t *testing.T, id string) {
	t.Helper()
	p, _ := e.store.GetProduct(context.Background(), id)
	p.Status = ProductPublished
	if err := e.store.SaveProduct(context.Background(), p, p.Version); err != nil {
		t.Fatal(err)
	}
}

func TestCreateProductStartsAsDraftAtVersion1(t *testing.T) {
	e := newProductEnv(t)
	v := e.newProduct(t, "city-backpack")
	if v.Status != ProductDraft || v.Version != 1 || v.CreatedBy != "staff-1" || v.UpdatedBy != "staff-1" {
		t.Fatalf("v = %+v", v.Product)
	}
	if !reflectEqualStrings(v.CategoryIDs, []string{"cat-city"}) || v.Specs["capacityLiters"] != 20 {
		t.Fatalf("not normalized: %+v", v.Product)
	}
	if v.HasDraft || len(v.SKUs) != 0 {
		t.Fatalf("view = %+v", v)
	}
}

func reflectEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCreateProductRules(t *testing.T) {
	e := newProductEnv(t)
	e.newProduct(t, "taken")
	ctx := context.Background()
	tests := []struct {
		name string
		in   ProductInput
		want error
	}{
		{"duplicate slug", ProductInput{Slug: "taken", Name: "X"}, ErrProductSlugTaken},
		{"unknown primary category", ProductInput{Slug: "x", Name: "X", PrimaryCategoryID: "nope"}, ErrCategoryNotFound},
		{"unknown additional category", ProductInput{Slug: "x", Name: "X", CategoryIDs: []string{"nope"}}, ErrCategoryNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.svc.CreateProduct(ctx, "s", tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	_, err := e.svc.CreateProduct(ctx, "s", ProductInput{Slug: "Bad Slug", Name: ""})
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("err = %v, want validation error", err)
	}
}

func TestUpdateDraftProductAppliesPatchAndBumpsVersion(t *testing.T) {
	e := newProductEnv(t)
	v := e.newProduct(t, "city-backpack")
	name, brand := "City Backpack", "Acme"
	got, err := e.svc.UpdateProduct(context.Background(), "staff-2", v.ID, 1, ProductPatch{Name: &name, Brand: &brand})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.Brand != brand || got.Version != 2 || got.UpdatedBy != "staff-2" || got.CreatedBy != "staff-1" || got.HasDraft {
		t.Fatalf("got = %+v", got.Product)
	}
}

func TestUpdateWithStaleVersionConflicts(t *testing.T) {
	e := newProductEnv(t)
	v := e.newProduct(t, "city-backpack")
	name := "A"
	if _, err := e.svc.UpdateProduct(context.Background(), "s", v.ID, 1, ProductPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	name = "B"
	if _, err := e.svc.UpdateProduct(context.Background(), "s", v.ID, 1, ProductPatch{Name: &name}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v", err)
	}
	got, _ := e.svc.GetProduct(context.Background(), v.ID)
	if got.Name != "A" {
		t.Fatalf("stale write must not apply, name = %q", got.Name)
	}
}

func TestEditingAPublishedProductCreatesADraftRevision(t *testing.T) {
	e := newProductEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	e.publishDirectly(t, v.ID)
	live, _ := e.store.GetProduct(ctx, v.ID)
	name := "New name"

	got, err := e.svc.UpdateProduct(ctx, "staff-2", v.ID, live.Version, ProductPatch{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || !got.HasDraft || got.Version != live.Version+1 || got.Status != ProductPublished {
		t.Fatalf("view = %+v hasDraft=%v", got.Product, got.HasDraft)
	}
	stillLive, _ := e.store.GetProduct(ctx, v.ID)
	if stillLive.Name != "Product city-backpack" || stillLive.Version != live.Version {
		t.Fatalf("live product must not change, got %+v", stillLive)
	}

	// A second edit continues the same draft and is guarded by the draft version.
	brand := "Acme"
	next, err := e.svc.UpdateProduct(ctx, "staff-2", v.ID, got.Version, ProductPatch{Brand: &brand})
	if err != nil || next.Name != name || next.Brand != brand || next.Version != got.Version+1 {
		t.Fatalf("next = %+v, %v", next.Product, err)
	}
	if _, err := e.svc.UpdateProduct(ctx, "staff-2", v.ID, got.Version, ProductPatch{Brand: &brand}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale draft edit: %v", err)
	}
	// Reading returns the draft view with the draft version.
	read, _ := e.svc.GetProduct(ctx, v.ID)
	if !read.HasDraft || read.Brand != brand || read.Version != next.Version {
		t.Fatalf("read = %+v", read.Product)
	}
}

func TestFirstDraftEditNeedsTheLiveVersion(t *testing.T) {
	e := newProductEnv(t)
	v := e.newProduct(t, "city-backpack")
	e.publishDirectly(t, v.ID)
	name := "x"
	if _, err := e.svc.UpdateProduct(context.Background(), "s", v.ID, 99, ProductPatch{Name: &name}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestInReviewProductIsLocked(t *testing.T) {
	e := newProductEnv(t)
	v := e.newProduct(t, "city-backpack")
	p, _ := e.store.GetProduct(context.Background(), v.ID)
	p.Status = ProductInReview
	_ = e.store.SaveProduct(context.Background(), p, p.Version)
	name := "x"
	live, _ := e.store.GetProduct(context.Background(), v.ID)
	if _, err := e.svc.UpdateProduct(context.Background(), "s", v.ID, live.Version, ProductPatch{Name: &name}); !errors.Is(err, ErrProductNotEditable) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateUnknownProductAndSlugClash(t *testing.T) {
	e := newProductEnv(t)
	name := "x"
	if _, err := e.svc.UpdateProduct(context.Background(), "s", "nope", 1, ProductPatch{Name: &name}); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("err = %v", err)
	}
	e.newProduct(t, "one")
	two := e.newProduct(t, "two")
	slug := "one"
	if _, err := e.svc.UpdateProduct(context.Background(), "s", two.ID, 1, ProductPatch{Slug: &slug}); !errors.Is(err, ErrProductSlugTaken) {
		t.Fatalf("err = %v", err)
	}
}

func TestVariantAxesAreLockedOnceSKUsExist(t *testing.T) {
	e := newProductEnv(t)
	v := e.newProduct(t, "city-backpack")
	axes := []string{"color"}
	changed, err := e.svc.UpdateProduct(context.Background(), "s", v.ID, v.Version, ProductPatch{VariantAxes: &axes})
	if err != nil {
		t.Fatalf("axes change without SKUs: %v", err)
	}
	axes = []string{"color", "capacity"}
	changed, err = e.svc.UpdateProduct(context.Background(), "s", v.ID, changed.Version, ProductPatch{VariantAxes: &axes})
	if err != nil {
		t.Fatal(err)
	}
	withSKU := e.addSKU(t, changed, "SKU-A", "Black")
	axes = []string{"color"}
	if _, err := e.svc.UpdateProduct(context.Background(), "s", v.ID, withSKU.Version, ProductPatch{VariantAxes: &axes}); !errors.Is(err, ErrAxesLocked) {
		t.Fatalf("err = %v", err)
	}
	same := []string{"color", "capacity"}
	if _, err := e.svc.UpdateProduct(context.Background(), "s", v.ID, withSKU.Version, ProductPatch{VariantAxes: &same}); err != nil {
		t.Fatalf("unchanged axes must be accepted: %v", err)
	}
}

func TestSKULifecycle(t *testing.T) {
	e := newProductEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	v = e.addSKU(t, v, "SKU-A", "Black")
	if v.Version != 2 || len(v.SKUs) != 1 || !v.SKUs[0].IsActive || v.SKUs[0].ProductID != v.ID {
		t.Fatalf("v = %+v", v)
	}
	v = e.addSKU(t, v, "SKU-B", "Red")

	// Same combination (case-insensitive) is rejected.
	if _, err := e.svc.CreateSKU(ctx, "s", v.ID, v.Version, SKUInput{SkuID: "SKU-C", Attributes: map[string]string{"color": "BLACK", "capacity": "20 l"}}); err == nil {
		t.Fatal("duplicate combination accepted")
	} else if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("err = %v", err)
	}

	off := false
	color := "Blue"
	updated, err := e.svc.UpdateSKU(ctx, "s", v.ID, "SKU-B", v.Version, SKUPatch{IsActive: &off, Attributes: map[string]string{"color": color, "capacity": "20 L"}})
	if err != nil {
		t.Fatal(err)
	}
	var b SKU
	for _, s := range updated.SKUs {
		if s.SkuID == "SKU-B" {
			b = s
		}
	}
	if b.IsActive || b.Attributes["color"] != "Blue" || updated.Version != v.Version+1 {
		t.Fatalf("updated = %+v", updated)
	}

	removed, err := e.svc.DeleteSKU(ctx, "s", v.ID, "SKU-B", updated.Version)
	if err != nil || len(removed.SKUs) != 1 || removed.Version != updated.Version+1 {
		t.Fatalf("removed = %+v, %v", removed, err)
	}
	if _, err := e.svc.DeleteSKU(ctx, "s", v.ID, "SKU-B", removed.Version); !errors.Is(err, ErrSKUNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestSKURules(t *testing.T) {
	e := newProductEnv(t)
	ctx := context.Background()
	a := e.newProduct(t, "a")
	b := e.newProduct(t, "b")
	e.addSKU(t, a, "SKU-A", "Black")

	// The code is unique across all products.
	if _, err := e.svc.CreateSKU(ctx, "s", b.ID, b.Version, SKUInput{SkuID: "SKU-A", Attributes: map[string]string{"color": "Black", "capacity": "20 L"}}); !errors.Is(err, ErrSKUCodeTaken) {
		t.Fatalf("global duplicate: %v", err)
	}
	// Stale version.
	if _, err := e.svc.CreateSKU(ctx, "s", a.ID, 1, SKUInput{SkuID: "SKU-Z", Attributes: map[string]string{"color": "Z", "capacity": "20 L"}}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale: %v", err)
	}
	// Missing axis value.
	if _, err := e.svc.CreateSKU(ctx, "s", b.ID, b.Version, SKUInput{SkuID: "SKU-Y", Attributes: map[string]string{"color": "Z"}}); err == nil {
		t.Fatal("missing axis accepted")
	}
	// Unknown product, unknown SKU, SKU of another product.
	if _, err := e.svc.CreateSKU(ctx, "s", "nope", 1, SKUInput{SkuID: "SKU-Q"}); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("unknown product: %v", err)
	}
	cur, _ := e.svc.GetProduct(ctx, b.ID)
	if _, err := e.svc.UpdateSKU(ctx, "s", b.ID, "SKU-A", cur.Version, SKUPatch{}); !errors.Is(err, ErrSKUNotFound) {
		t.Fatalf("sku of another product: %v", err)
	}
}

func TestSKUChangesAreOnlyAllowedWhileTheProductIsNotLive(t *testing.T) {
	e := newProductEnv(t)
	ctx := context.Background()
	v := e.newProduct(t, "city-backpack")
	v = e.addSKU(t, v, "SKU-A", "Black")
	e.publishDirectly(t, v.ID)
	live, _ := e.store.GetProduct(ctx, v.ID)
	in := SKUInput{SkuID: "SKU-B", Attributes: map[string]string{"color": "Red", "capacity": "20 L"}}
	if _, err := e.svc.CreateSKU(ctx, "s", v.ID, live.Version, in); !errors.Is(err, ErrProductNotEditable) {
		t.Fatalf("published: %v", err)
	}
	if _, err := e.svc.UpdateSKU(ctx, "s", v.ID, "SKU-A", live.Version, SKUPatch{}); !errors.Is(err, ErrProductNotEditable) {
		t.Fatalf("published update: %v", err)
	}
	if _, err := e.svc.DeleteSKU(ctx, "s", v.ID, "SKU-A", live.Version); !errors.Is(err, ErrProductNotEditable) {
		t.Fatalf("published delete: %v", err)
	}
}

func TestListProductsFiltersAndPaginates(t *testing.T) {
	e := newProductEnv(t)
	ctx := context.Background()
	for _, s := range []string{"alpha", "beta", "gamma"} {
		e.newProduct(t, s)
	}
	e.publishDirectly(t, "prod-a")

	all, total, err := e.svc.ListProducts(ctx, ProductFilter{Page: 1, Limit: 2})
	if err != nil || total != 3 || len(all) != 2 {
		t.Fatalf("page 1: %d of %d, %v", len(all), total, err)
	}
	rest, _, _ := e.svc.ListProducts(ctx, ProductFilter{Page: 2, Limit: 2})
	if len(rest) != 1 {
		t.Fatalf("page 2: %d", len(rest))
	}
	pub, total, _ := e.svc.ListProducts(ctx, ProductFilter{Status: ProductPublished, Page: 1, Limit: 10})
	if total != 1 || pub[0].ID != "prod-a" {
		t.Fatalf("published: %+v", pub)
	}
	// User text is matched literally, never as a regular expression.
	found, total, _ := e.svc.ListProducts(ctx, ProductFilter{Q: ".*", Page: 1, Limit: 10})
	if total != 0 || len(found) != 0 {
		t.Fatalf("regex query matched %d products", total)
	}
	found, total, _ = e.svc.ListProducts(ctx, ProductFilter{Q: "BET", Page: 1, Limit: 10})
	if total != 1 || found[0].Slug != "beta" {
		t.Fatalf("substring search: %+v", found)
	}
}
