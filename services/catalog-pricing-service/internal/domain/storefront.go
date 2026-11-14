package domain

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	defaultStorefrontLimit = 20
	maxStorefrontLimit     = 100
	maxSearchTextLen       = 100
	// maxStorefrontCandidates bounds how many products one request evaluates before paginating.
	maxStorefrontCandidates = 5000

	SortNewest    = "newest"
	SortPriceAsc  = "price_asc"
	SortPriceDesc = "price_desc"

	// AvailabilityUnknown is reported until inventory is connected (Phase 3).
	AvailabilityUnknown = "unknown"
)

// SKUPrice is the price in effect for an orderable SKU. Prices include VAT.
type SKUPrice struct {
	UnitPriceVND int64
	VatRatePct   int
}

// PriceLookup returns the current price of every given SKU that can be ordered now; other SKUs are absent.
type PriceLookup interface {
	OrderablePrices(ctx context.Context, skuIDs []string) (map[string]SKUPrice, error)
}

// PublishedRef identifies a published product found by a search.
type PublishedRef struct {
	ID          string
	PublishedAt time.Time
}

// StorefrontStore reads published catalog data. Unpublished products are never returned.
type StorefrontStore interface {
	// FindPublished returns published products in any of categoryIDs (all when nil) matching the search text
	// (word search, not a pattern; empty means no text filter).
	FindPublished(ctx context.Context, text string, categoryIDs []string) ([]PublishedRef, error)
	// ActiveSKUs returns the active SKUs of the products whose attributes equal attrs (case-insensitive).
	ActiveSKUs(ctx context.Context, productIDs []string, attrs map[string]string) ([]SKU, error)
	// PublishedByIDs returns the published products with the given ids, in that order.
	PublishedByIDs(ctx context.Context, ids []string) ([]Product, error)
	// PublishedBySlug returns ErrProductNotFound for unknown or unpublished products.
	PublishedBySlug(ctx context.Context, slug string) (Product, error)
}

// CategoryLister provides the category tree.
type CategoryLister interface {
	List(ctx context.Context) ([]Category, error)
}

// StorefrontQuery is a product search. Zero values mean "no filter" / defaults.
type StorefrontQuery struct {
	CategoryPath string
	Q            string
	MinPrice     *int64
	MaxPrice     *int64
	Attrs        map[string]string // variant axis -> value
	Sort         string
	Page         int
	Limit        int
}

// StorefrontItem is one product card. Prices are the range over the SKUs that match the filters.
type StorefrontItem struct {
	ID                string              `json:"id"`
	Slug              string              `json:"slug"`
	Name              string              `json:"name"`
	Brand             string              `json:"brand"`
	PrimaryCategoryID string              `json:"primaryCategoryId"`
	ImageKey          string              `json:"-"`
	ImageURL          string              `json:"imageUrl,omitempty"`
	MinPriceVND       int64               `json:"minPriceVnd"`
	MaxPriceVND       int64               `json:"maxPriceVnd"`
	Options           map[string][]string `json:"options"`
}

// StorefrontPage is one page of results with real totals.
type StorefrontPage struct {
	Items     []StorefrontItem
	Total     int64
	Page      int
	PageCount int
	Limit     int
}

// StorefrontSKU is a SKU of the product detail. UnitPriceVND is nil when the SKU cannot be ordered now.
type StorefrontSKU struct {
	SkuID        string            `json:"skuId"`
	Attributes   map[string]string `json:"attributes"`
	UnitPriceVND *int64            `json:"unitPriceVnd"`
	VatRatePct   int               `json:"vatRatePct"`
	Availability string            `json:"availability"`
}

// StorefrontDetail is the product page.
type StorefrontDetail struct {
	ID                string          `json:"id"`
	Slug              string          `json:"slug"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Brand             string          `json:"brand"`
	PrimaryCategoryID string          `json:"primaryCategoryId"`
	CategoryIDs       []string        `json:"categoryIds"`
	VariantAxes       []string        `json:"variantAxes"`
	Specs             map[string]any  `json:"specs"`
	Images            []ProductImage  `json:"images"`
	SKUs              []StorefrontSKU `json:"skus"`
	Availability      string          `json:"availability"`
}

// StorefrontService answers customer catalog queries.
type StorefrontService struct {
	store      StorefrontStore
	categories CategoryLister
	prices     PriceLookup
}

func NewStorefrontService(store StorefrontStore, categories CategoryLister, prices PriceLookup) *StorefrontService {
	return &StorefrontService{store: store, categories: categories, prices: prices}
}

func (q *StorefrontQuery) validate() error {
	fields := map[string][]string{}
	add := func(f, m string) { fields[f] = append(fields[f], m) }
	switch q.Sort {
	case "":
		q.Sort = SortNewest
	case SortNewest, SortPriceAsc, SortPriceDesc:
	default:
		add("sort", "sort must be newest, price_asc or price_desc")
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.Page < 1 {
		add("page", "page must be 1 or more")
	}
	if q.Limit == 0 {
		q.Limit = defaultStorefrontLimit
	}
	if q.Limit < 1 || q.Limit > maxStorefrontLimit {
		add("limit", fmt.Sprintf("limit must be between 1 and %d", maxStorefrontLimit))
	}
	if q.MinPrice != nil && *q.MinPrice < 0 {
		add("minPrice", "minPrice cannot be negative")
	}
	if q.MaxPrice != nil && *q.MaxPrice < 0 {
		add("maxPrice", "maxPrice cannot be negative")
	}
	if q.MinPrice != nil && q.MaxPrice != nil && *q.MinPrice > *q.MaxPrice {
		add("maxPrice", "maxPrice must not be below minPrice")
	}
	q.Q = strings.TrimSpace(q.Q)
	if len(q.Q) > maxSearchTextLen {
		add("q", fmt.Sprintf("q must be at most %d characters", maxSearchTextLen))
	}
	for axis, value := range q.Attrs {
		if !axisPattern.MatchString(axis) || strings.TrimSpace(value) == "" || len(value) > maxAttributeLen {
			add("attr."+axis, "attribute filters need a variant axis name and a value of up to 64 characters")
		}
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func collectCategoryIDs(n CategoryNode, into *[]string) {
	*into = append(*into, n.ID)
	for _, c := range n.Children {
		collectCategoryIDs(c, into)
	}
}

func (s *StorefrontService) categoryIDs(ctx context.Context, path string) ([]string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil, nil
	}
	cats, err := s.categories.List(ctx)
	if err != nil {
		return nil, err
	}
	node, ok := FindCategoryNode(BuildCategoryTree(cats), "/"+path)
	if !ok {
		return nil, ErrCategoryNotFound
	}
	var ids []string
	collectCategoryIDs(node, &ids)
	return ids, nil
}

type candidate struct {
	ref      PublishedRef
	min, max int64
	options  map[string]map[string]string // axis -> lower-cased value -> display value
}

// List returns one page of published products. A product matches when at least one of its active, orderable
// SKUs satisfies the attribute and price filters together; the card shows the price range of those SKUs.
func (s *StorefrontService) List(ctx context.Context, q StorefrontQuery) (StorefrontPage, error) {
	if err := q.validate(); err != nil {
		return StorefrontPage{}, err
	}
	categoryIDs, err := s.categoryIDs(ctx, q.CategoryPath)
	if err != nil {
		return StorefrontPage{}, err
	}
	refs, err := s.store.FindPublished(ctx, q.Q, categoryIDs)
	if err != nil {
		return StorefrontPage{}, err
	}
	if len(refs) > maxStorefrontCandidates {
		refs = refs[:maxStorefrontCandidates]
	}
	byID := make(map[string]*candidate, len(refs))
	productIDs := make([]string, len(refs))
	for i, r := range refs {
		productIDs[i] = r.ID
		byID[r.ID] = &candidate{ref: r, options: map[string]map[string]string{}}
	}
	skus, err := s.store.ActiveSKUs(ctx, productIDs, q.Attrs)
	if err != nil {
		return StorefrontPage{}, err
	}
	skuIDs := make([]string, len(skus))
	for i, k := range skus {
		skuIDs[i] = k.SkuID
	}
	prices, err := s.prices.OrderablePrices(ctx, skuIDs)
	if err != nil {
		return StorefrontPage{}, err
	}
	matched := map[string]*candidate{}
	for _, k := range skus {
		price, ok := prices[k.SkuID]
		c := byID[k.ProductID]
		if !ok || c == nil || (q.MinPrice != nil && price.UnitPriceVND < *q.MinPrice) || (q.MaxPrice != nil && price.UnitPriceVND > *q.MaxPrice) {
			continue
		}
		if matched[c.ref.ID] == nil {
			c.min, c.max = math.MaxInt64, 0
			matched[c.ref.ID] = c
		}
		c.min, c.max = min(c.min, price.UnitPriceVND), max(c.max, price.UnitPriceVND)
		for axis, value := range k.Attributes {
			if c.options[axis] == nil {
				c.options[axis] = map[string]string{}
			}
			c.options[axis][strings.ToLower(strings.TrimSpace(value))] = strings.TrimSpace(value)
		}
	}
	ordered := make([]*candidate, 0, len(matched))
	for _, c := range matched {
		ordered = append(ordered, c)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		switch {
		case q.Sort == SortPriceAsc && a.min != b.min:
			return a.min < b.min
		case q.Sort == SortPriceDesc && a.min != b.min:
			return a.min > b.min
		case !a.ref.PublishedAt.Equal(b.ref.PublishedAt):
			return a.ref.PublishedAt.After(b.ref.PublishedAt)
		}
		return a.ref.ID < b.ref.ID
	})

	total := len(ordered)
	page := StorefrontPage{Items: []StorefrontItem{}, Total: int64(total), Page: q.Page, Limit: q.Limit, PageCount: (total + q.Limit - 1) / q.Limit}
	start := min((q.Page-1)*q.Limit, total)
	window := ordered[start:min(start+q.Limit, total)]
	if len(window) == 0 {
		return page, nil
	}
	ids := make([]string, len(window))
	for i, c := range window {
		ids[i] = c.ref.ID
	}
	products, err := s.store.PublishedByIDs(ctx, ids)
	if err != nil {
		return StorefrontPage{}, err
	}
	for _, p := range products {
		c := matched[p.ID]
		item := StorefrontItem{
			ID: p.ID, Slug: p.Slug, Name: p.Name, Brand: p.Brand, PrimaryCategoryID: p.PrimaryCategoryID,
			MinPriceVND: c.min, MaxPriceVND: c.max, Options: map[string][]string{},
		}
		if len(p.Images) > 0 {
			item.ImageKey = p.Images[0].Key
		}
		for axis, values := range c.options {
			for _, v := range values {
				item.Options[axis] = append(item.Options[axis], v)
			}
			sort.Strings(item.Options[axis])
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// Detail returns the product page of a published product by slug.
func (s *StorefrontService) Detail(ctx context.Context, slug string) (StorefrontDetail, error) {
	p, err := s.store.PublishedBySlug(ctx, slug)
	if err != nil {
		return StorefrontDetail{}, err
	}
	skus, err := s.store.ActiveSKUs(ctx, []string{p.ID}, nil)
	if err != nil {
		return StorefrontDetail{}, err
	}
	ids := make([]string, len(skus))
	for i, k := range skus {
		ids[i] = k.SkuID
	}
	prices, err := s.prices.OrderablePrices(ctx, ids)
	if err != nil {
		return StorefrontDetail{}, err
	}
	sort.Slice(skus, func(i, j int) bool { return skus[i].SkuID < skus[j].SkuID })
	out := make([]StorefrontSKU, 0, len(skus))
	for _, k := range skus {
		item := StorefrontSKU{SkuID: k.SkuID, Attributes: k.Attributes, Availability: AvailabilityUnknown, VatRatePct: 10}
		if price, ok := prices[k.SkuID]; ok {
			amount := price.UnitPriceVND
			item.UnitPriceVND, item.VatRatePct = &amount, price.VatRatePct
		}
		out = append(out, item)
	}
	images := p.Images
	if images == nil {
		images = []ProductImage{}
	}
	return StorefrontDetail{
		ID: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description, Brand: p.Brand,
		PrimaryCategoryID: p.PrimaryCategoryID, CategoryIDs: p.CategoryIDs, VariantAxes: p.VariantAxes, Specs: p.Specs,
		Images: images, SKUs: out, Availability: AvailabilityUnknown,
	}, nil
}
