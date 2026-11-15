package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fakePrices map[string]SKUPrice

func (f fakePrices) OrderablePrices(_ context.Context, ids []string) (map[string]SKUPrice, error) {
	out := map[string]SKUPrice{}
	for _, id := range ids {
		if p, ok := f[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

type storefrontEnv struct {
	*productEnv
	front  *StorefrontService
	prices fakePrices
	cats   map[string]string // slug -> id
	clock  time.Time
}

func newStorefrontEnv(t *testing.T) *storefrontEnv {
	t.Helper()
	e := newProductEnv(t)
	catSvc := NewCategoryService(e.categories)
	s := &storefrontEnv{productEnv: e, prices: fakePrices{}, cats: map[string]string{}, clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	mk := func(parent, slug string) string {
		c, err := catSvc.Create(context.Background(), CreateCategoryInput{ParentID: parent, Name: slug, Slug: slug})
		if err != nil {
			t.Fatal(err)
		}
		s.cats[slug] = c.ID
		return c.ID
	}
	backpacks := mk("", "backpacks")
	mk(backpacks, "city")
	mk(backpacks, "travel")
	mk("", "bottles")
	s.front = NewStorefrontService(e.store, catSvc, s.prices)
	return s
}

type skuSpec struct {
	code, color, cap string
	price            int64
}

// publish stores a published product directly (the workflow is tested elsewhere).
func (s *storefrontEnv) publish(t *testing.T, slug, name, category string, skus ...skuSpec) Product {
	t.Helper()
	ctx := context.Background()
	s.clock = s.clock.Add(time.Hour)
	at := s.clock
	p := Product{
		ID: "p-" + slug, Slug: slug, Name: name, Description: name + " description", Brand: "Acme",
		PrimaryCategoryID: s.cats[category], CategoryIDs: []string{s.cats[category]}, VariantAxes: []string{"color", "capacity"},
		Specs: map[string]any{}, Images: []ProductImage{{ID: "i1", Key: "https://img.test/" + slug + ".png"}},
		Status: ProductPublished, Version: 1, CreatedAt: at, UpdatedAt: at, PublishedAt: &at,
	}
	if err := s.store.InsertProduct(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, k := range skus {
		if err := s.store.InsertSKU(ctx, SKU{SkuID: k.code, ProductID: p.ID, Attributes: map[string]string{"color": k.color, "capacity": k.cap}, IsActive: true}); err != nil {
			t.Fatal(err)
		}
		s.prices[k.code] = SKUPrice{UnitPriceVND: k.price, VatRatePct: 10}
	}
	return p
}

func slugs(items []StorefrontItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Slug
	}
	return out
}

func (s *storefrontEnv) list(t *testing.T, q StorefrontQuery) StorefrontPage {
	t.Helper()
	page, err := s.front.List(context.Background(), q)
	if err != nil {
		t.Fatalf("list %+v: %v", q, err)
	}
	return page
}

func eq(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func TestListShowsOnlyPublishedProductsNewestFirst(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "old", "Old Pack", "city", skuSpec{"SKU-OLD", "Black", "20 L", 100000})
	s.publish(t, "new", "New Pack", "city", skuSpec{"SKU-NEW", "Black", "20 L", 200000})
	d := s.newProduct(t, "draft") // a draft is never listed
	_ = d
	page := s.list(t, StorefrontQuery{})
	if !eq(slugs(page.Items), []string{"new", "old"}) || page.Total != 2 || page.Page != 1 || page.PageCount != 1 || page.Limit != 20 {
		t.Fatalf("page = %+v", page)
	}
	if page.Items[0].MinPriceVND != 200000 || page.Items[0].ImageKey != "https://img.test/new.png" {
		t.Fatalf("item = %+v", page.Items[0])
	}
}

func TestListFiltersByCategoryIncludingDescendants(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "city-pack", "City Pack", "city", skuSpec{"SKU-1", "Black", "20 L", 100})
	s.publish(t, "travel-pack", "Travel Pack", "travel", skuSpec{"SKU-2", "Black", "40 L", 100})
	s.publish(t, "flask", "Flask", "bottles", skuSpec{"SKU-3", "Blue", "500 ml", 100})

	for path, want := range map[string][]string{
		"backpacks":       {"travel-pack", "city-pack"},
		"/backpacks":      {"travel-pack", "city-pack"},
		"backpacks/city":  {"city-pack"},
		"/bottles":        {"flask"},
		"backpacks/city/": {"city-pack"},
	} {
		if got := slugs(s.list(t, StorefrontQuery{CategoryPath: path}).Items); !eq(got, want) {
			t.Errorf("category %q = %v, want %v", path, got, want)
		}
	}
	if _, err := s.front.List(context.Background(), StorefrontQuery{CategoryPath: "nope"}); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("unknown category: %v", err)
	}
	// An inactive category hides itself and its products from the storefront.
	off := false
	if _, err := NewCategoryService(s.categories).Update(context.Background(), s.cats["city"], CategoryPatch{IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.front.List(context.Background(), StorefrontQuery{CategoryPath: "backpacks/city"}); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("inactive category: %v", err)
	}
	if got := slugs(s.list(t, StorefrontQuery{CategoryPath: "backpacks"}).Items); !eq(got, []string{"travel-pack"}) {
		t.Fatalf("parent after child deactivated = %v", got)
	}
}

func TestListPriceFilterSortAndOptions(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "cheap", "Cheap", "city", skuSpec{"C1", "Black", "20 L", 100000}, skuSpec{"C2", "Red", "20 L", 150000})
	s.publish(t, "mid", "Mid", "city", skuSpec{"M1", "Black", "20 L", 300000})
	s.publish(t, "pricey", "Pricey", "city", skuSpec{"P1", "Green", "30 L", 900000})

	if got := slugs(s.list(t, StorefrontQuery{Sort: "price_asc"}).Items); !eq(got, []string{"cheap", "mid", "pricey"}) {
		t.Fatalf("price_asc = %v", got)
	}
	if got := slugs(s.list(t, StorefrontQuery{Sort: "price_desc"}).Items); !eq(got, []string{"pricey", "mid", "cheap"}) {
		t.Fatalf("price_desc = %v", got)
	}
	min, max := int64(120000), int64(400000)
	page := s.list(t, StorefrontQuery{MinPrice: &min, MaxPrice: &max, Sort: "price_asc"})
	if !eq(slugs(page.Items), []string{"cheap", "mid"}) {
		t.Fatalf("range = %v", slugs(page.Items))
	}
	// The cheap product matches only through its 150,000 SKU, so that is the price shown.
	if page.Items[0].MinPriceVND != 150000 || page.Items[0].MaxPriceVND != 150000 {
		t.Fatalf("range-limited price = %+v", page.Items[0])
	}
	all := s.list(t, StorefrontQuery{Sort: "price_asc"})
	if all.Items[0].MinPriceVND != 100000 || all.Items[0].MaxPriceVND != 150000 {
		t.Fatalf("price range = %+v", all.Items[0])
	}
	if got := all.Items[0].Options["color"]; !eq(got, []string{"Black", "Red"}) {
		t.Fatalf("color options = %v", got)
	}
}

func TestListAttributeFilterAppliesToTheSameSKUAsThePrice(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "pack", "Pack", "city", skuSpec{"A", "Black", "20 L", 500000}, skuSpec{"B", "Red", "20 L", 100000})
	max := int64(200000)
	// Black exists and a SKU under 200,000 exists, but not the same SKU.
	if got := s.list(t, StorefrontQuery{Attrs: map[string][]string{"color": {"black"}}, MaxPrice: &max}).Items; len(got) != 0 {
		t.Fatalf("matched through different SKUs: %v", slugs(got))
	}
	if got := s.list(t, StorefrontQuery{Attrs: map[string][]string{"color": {"BLACK"}}}).Items; len(got) != 1 {
		t.Fatalf("case-insensitive attribute: %v", slugs(got))
	}
	if got := s.list(t, StorefrontQuery{Attrs: map[string][]string{"color": {"red"}}, MaxPrice: &max}).Items; len(got) != 1 {
		t.Fatalf("red under max: %v", slugs(got))
	}
	// Attribute values are literal text.
	if got := s.list(t, StorefrontQuery{Attrs: map[string][]string{"color": {".*"}}}).Items; len(got) != 0 {
		t.Fatalf("regex attribute matched %v", slugs(got))
	}
}

func TestListTextSearchIsLiteral(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "city-pack", "City Backpack", "city", skuSpec{"A", "Black", "20 L", 1})
	s.publish(t, "flask", "Steel Flask", "bottles", skuSpec{"B", "Blue", "500 ml", 1})
	if got := slugs(s.list(t, StorefrontQuery{Q: "backpack"}).Items); !eq(got, []string{"city-pack"}) {
		t.Fatalf("q = %v", got)
	}
	for _, q := range []string{".*", "(", "[a-z]+", "\\"} {
		if got := s.list(t, StorefrontQuery{Q: q}).Items; len(got) != 0 {
			t.Errorf("q=%q matched %v", q, slugs(got))
		}
	}
}

func TestListPaginationMetadata(t *testing.T) {
	s := newStorefrontEnv(t)
	for i := 0; i < 5; i++ {
		s.publish(t, fmt.Sprintf("p%d", i), "P", "city", skuSpec{fmt.Sprintf("S%d", i), "Black", "20 L", 1})
	}
	p2 := s.list(t, StorefrontQuery{Page: 2, Limit: 2})
	if p2.Total != 5 || p2.PageCount != 3 || p2.Page != 2 || p2.Limit != 2 || !eq(slugs(p2.Items), []string{"p2", "p1"}) {
		t.Fatalf("page 2 = %+v", p2)
	}
	if p4 := s.list(t, StorefrontQuery{Page: 4, Limit: 2}); len(p4.Items) != 0 || p4.Total != 5 {
		t.Fatalf("beyond the last page = %+v", p4)
	}
}

func TestListValidation(t *testing.T) {
	s := newStorefrontEnv(t)
	neg, one := int64(-1), int64(1)
	tests := []struct {
		name  string
		q     StorefrontQuery
		field string
	}{
		{"unknown sort", StorefrontQuery{Sort: "random"}, "sort"},
		{"negative min", StorefrontQuery{MinPrice: &neg}, "minPrice"},
		{"min above max", StorefrontQuery{MinPrice: &one, MaxPrice: new(int64)}, "maxPrice"},
		{"limit too big", StorefrontQuery{Limit: 101}, "limit"},
		{"negative page", StorefrontQuery{Page: -1}, "page"},
		{"bad attribute name", StorefrontQuery{Attrs: map[string][]string{"$where": {"x"}}}, "attr.$where"},
		{"long q", StorefrontQuery{Q: string(make([]byte, 101))}, "q"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.front.List(context.Background(), tc.q)
			if ve, ok := err.(*ValidationError); !ok || len(ve.Fields[tc.field]) == 0 {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestDetailShowsActiveSKUsWithCurrentPricesAndUnknownAvailability(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "city-pack", "City Pack", "city", skuSpec{"A", "Black", "20 L", 300000}, skuSpec{"B", "Red", "20 L", 310000})
	if err := s.store.SaveSKU(context.Background(), SKU{SkuID: "B", ProductID: "p-city-pack", Attributes: map[string]string{"color": "Red", "capacity": "20 L"}, IsActive: false}); err != nil {
		t.Fatal(err)
	}
	d, err := s.front.Detail(context.Background(), "city-pack")
	if err != nil {
		t.Fatal(err)
	}
	if d.Availability != "unknown" || len(d.SKUs) != 1 || d.SKUs[0].SkuID != "A" || d.SKUs[0].UnitPriceVND == nil || *d.SKUs[0].UnitPriceVND != 300000 ||
		d.SKUs[0].VatRatePct != 10 || d.SKUs[0].Availability != "unknown" {
		t.Fatalf("detail = %+v", d)
	}
	if _, err := s.front.Detail(context.Background(), "missing"); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("unknown slug: %v", err)
	}
	// Unpublished and draft products are not found.
	d2 := s.newProduct(t, "draft-only")
	if _, err := s.front.Detail(context.Background(), d2.Slug); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("draft: %v", err)
	}
	p, _ := s.store.GetProduct(context.Background(), "p-city-pack")
	p.Status = ProductUnpublished
	if err := s.store.SaveProduct(context.Background(), p, p.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.front.Detail(context.Background(), "city-pack"); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("unpublished: %v", err)
	}
	if got := s.list(t, StorefrontQuery{}); got.Total != 0 {
		t.Fatalf("unpublished product listed: %+v", got)
	}
}

func TestListAttributeFilterAcceptsSeveralValuesForOneAxis(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "pack", "Pack", "city", skuSpec{"A", "Black", "20 L", 500000})
	s.publish(t, "bag", "Bag", "city", skuSpec{"B", "Navy", "20 L", 400000})
	s.publish(t, "tote", "Tote", "city", skuSpec{"C", "Red", "20 L", 300000})
	got := s.list(t, StorefrontQuery{Attrs: map[string][]string{"color": {"black", "NAVY"}}}).Items
	if len(got) != 2 {
		t.Fatalf("black or navy: %v", slugs(got))
	}
}

type fakeStock struct {
	states map[string]string
	err    error
	delay  time.Duration
	calls  int
}

func (f *fakeStock) States(ctx context.Context, skuIDs []string) (map[string]string, error) {
	f.calls++
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.states, f.err
}

func TestProductAvailabilitySummarisesItsSKUs(t *testing.T) {
	for name, tc := range map[string]struct {
		states []string
		want   string
	}{
		"any in stock wins":      {[]string{"OUT", "LOW", "IN_STOCK"}, "IN_STOCK"},
		"low beats out":          {[]string{"OUT", "LOW"}, "LOW"},
		"all out":                {[]string{"OUT", "OUT"}, "OUT"},
		"unknown is not out":     {[]string{"OUT", "unknown"}, "unknown"},
		"low beats unknown":      {[]string{"unknown", "LOW"}, "LOW"},
		"no skus":                {nil, "unknown"},
		"in stock beats unknown": {[]string{"unknown", "IN_STOCK"}, "IN_STOCK"},
	} {
		if got := ProductAvailability(tc.states); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestListAndDetailCarryAvailabilityFromInventoryWithOneBatchCall(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "city-pack", "City Pack", "city", skuSpec{"A", "Black", "20 L", 300000}, skuSpec{"B", "Red", "20 L", 310000})
	s.publish(t, "flask", "Flask", "bottles", skuSpec{"C", "Blue", "500 ml", 100000})
	s.publish(t, "tumbler", "Tumbler", "bottles", skuSpec{"D", "Blue", "350 ml", 90000})
	stock := &fakeStock{states: map[string]string{"A": "OUT", "B": "LOW", "C": "OUT"}} // D has no answer
	s.front.WithAvailability(stock)

	page := s.list(t, StorefrontQuery{})
	got := map[string]string{}
	for _, it := range page.Items {
		got[it.Slug] = it.Availability
	}
	if got["city-pack"] != "LOW" || got["flask"] != "OUT" || got["tumbler"] != "unknown" || stock.calls != 1 {
		t.Fatalf("list availability = %v after %d call(s); want city-pack LOW, flask OUT, tumbler unknown in one call", got, stock.calls)
	}

	d, err := s.front.Detail(context.Background(), "city-pack")
	if err != nil || d.Availability != "LOW" || d.SKUs[0].Availability != "OUT" || d.SKUs[1].Availability != "LOW" {
		t.Fatalf("detail = %+v, err %v", d, err)
	}
}

func TestInventoryFailureOrTimeoutNeverFailsThePage(t *testing.T) {
	s := newStorefrontEnv(t)
	s.publish(t, "city-pack", "City Pack", "city", skuSpec{"A", "Black", "20 L", 300000})
	for name, stock := range map[string]*fakeStock{
		"error":   {err: errors.New("inventory is down")},
		"timeout": {delay: 5 * time.Second},
		"empty":   {states: map[string]string{}},
	} {
		s.front.WithAvailability(stock)
		start := time.Now()
		page, err := s.front.List(context.Background(), StorefrontQuery{})
		if err != nil || len(page.Items) != 1 || page.Items[0].Availability != "unknown" {
			t.Errorf("%s: list = %+v, err %v; want the item with availability unknown", name, page.Items, err)
		}
		d, err := s.front.Detail(context.Background(), "city-pack")
		if err != nil || d.Availability != "unknown" || d.SKUs[0].Availability != "unknown" {
			t.Errorf("%s: detail = %+v, err %v", name, d, err)
		}
		if time.Since(start) > 3*AvailabilityTimeout {
			t.Errorf("%s: took %v, the inventory call must be bounded by %v", name, time.Since(start), AvailabilityTimeout)
		}
	}
}
