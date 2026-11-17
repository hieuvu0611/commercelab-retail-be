package domain

import (
	"context"
	"fmt"
	"log"
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

	// AvailabilityUnknown is reported when inventory cannot answer in time; the page never fails because of it.
	AvailabilityUnknown = "unknown"

	// Availability states of the inventory service (D-12).
	AvailabilityInStock = "IN_STOCK"
	AvailabilityLow     = "LOW"
	AvailabilityOut     = "OUT"

	// AvailabilityTimeout bounds the batch call to inventory so a slow inventory service cannot slow the catalog.
	AvailabilityTimeout = time.Second
)

// AvailabilityLookup returns the storefront state of each SKU (IN_STOCK, LOW or OUT).
// SKUs missing from the result are reported as unknown.
type AvailabilityLookup interface {
	States(ctx context.Context, skuIDs []string) (map[string]string, error)
}

// ProductAvailability summarises the SKUs of one product: IN_STOCK if any is, else LOW if any is,
// else unknown if any is unknown, else OUT. No SKUs means unknown.
func ProductAvailability(states []string) string {
	var low, unknown bool
	for _, st := range states {
		switch st {
		case AvailabilityInStock:
			return AvailabilityInStock
		case AvailabilityLow:
			low = true
		case AvailabilityOut:
		default:
			unknown = true
		}
	}
	switch {
	case low:
		return AvailabilityLow
	case unknown || len(states) == 0:
		return AvailabilityUnknown
	default:
		return AvailabilityOut
	}
}

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
	ActiveSKUs(ctx context.Context, productIDs []string, attrs map[string][]string) ([]SKU, error)
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
	Attrs        map[string][]string // variant axis -> accepted values (any of them matches)
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
	Availability      string              `json:"availability"`
}

// StorefrontPage is one page of results with real totals.
type StorefrontPage struct {
	Items        []StorefrontItem
	Total        int64
	Page         int
	PageCount    int
	Limit        int
	SearchEngine string
	Facets       any
}

// SearchEngine optionally resolves published product candidates and faceted counts (e.g., OpenSearch).
// Per SC12, authoritative prices and stock are still read directly from transactional stores after search.
type SearchEngine interface {
	SearchProducts(ctx context.Context, q StorefrontQuery, categoryIDs []string) (refs []PublishedRef, facets any, ok bool)
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
	stock      AvailabilityLookup
	search     SearchEngine
}

// WithAvailability connects the inventory lookup; without it every availability is unknown.
func (s *StorefrontService) WithAvailability(stock AvailabilityLookup) *StorefrontService {
	s.stock = stock
	return s
}

// WithSearchEngine connects an optional OpenSearch read-model for full-text search & facets.
func (s *StorefrontService) WithSearchEngine(engine SearchEngine) *StorefrontService {
	s.search = engine
	return s
}

// availability asks inventory once for all SKUs, within AvailabilityTimeout. Any failure yields no states,
// which callers report as unknown.
func (s *StorefrontService) availability(ctx context.Context, skuIDs []string) map[string]string {
	if s.stock == nil || len(skuIDs) == 0 {
		return map[string]string{}
	}
	ctx, cancel := context.WithTimeout(ctx, AvailabilityTimeout)
	defer cancel()
	states, err := s.stock.States(ctx, skuIDs)
	if err != nil {
		log.Printf("[catalog] availability unavailable, reporting unknown: %v", err)
		return map[string]string{}
	}
	return states
}

func stateOrUnknown(states map[string]string, skuID string) string {
	if st, ok := states[skuID]; ok {
		return st
	}
	return AvailabilityUnknown
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
	for axis, values := range q.Attrs {
		valid := axisPattern.MatchString(axis) && len(values) > 0
		for _, value := range values {
			valid = valid && strings.TrimSpace(value) != "" && len(value) <= maxAttributeLen
		}
		if !valid {
			add("attr."+axis, "attribute filters need a variant axis name and values of up to 64 characters")
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
	skuIDs   []string                     // matching SKUs, for availability
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
	var refs []PublishedRef
	var facets any
	searchEngine := "mongodb"
	if s.search != nil {
		if osRefs, osFacets, ok := s.search.SearchProducts(ctx, q, categoryIDs); ok {
			refs = osRefs
			facets = osFacets
			searchEngine = "opensearch"
		}
	}
	if refs == nil {
		refs, err = s.store.FindPublished(ctx, q.Q, categoryIDs)
		if err != nil {
			return StorefrontPage{}, err
		}
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
		c.skuIDs = append(c.skuIDs, k.SkuID)
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
	page := StorefrontPage{Items: []StorefrontItem{}, Total: int64(total), Page: q.Page, Limit: q.Limit, PageCount: (total + q.Limit - 1) / q.Limit, SearchEngine: searchEngine, Facets: facets}
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
	var pageSkus []string
	for _, c := range window {
		pageSkus = append(pageSkus, matched[c.ref.ID].skuIDs...)
	}
	states := s.availability(ctx, pageSkus)
	for _, p := range products {
		c := matched[p.ID]
		productStates := make([]string, len(c.skuIDs))
		for i, id := range c.skuIDs {
			productStates[i] = stateOrUnknown(states, id)
		}
		item := StorefrontItem{
			ID: p.ID, Slug: p.Slug, Name: p.Name, Brand: p.Brand, PrimaryCategoryID: p.PrimaryCategoryID,
			MinPriceVND: c.min, MaxPriceVND: c.max, Options: map[string][]string{}, Availability: ProductAvailability(productStates),
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
	states := s.availability(ctx, ids)
	out := make([]StorefrontSKU, 0, len(skus))
	skuStates := make([]string, 0, len(skus))
	for _, k := range skus {
		item := StorefrontSKU{SkuID: k.SkuID, Attributes: k.Attributes, Availability: stateOrUnknown(states, k.SkuID), VatRatePct: 10}
		skuStates = append(skuStates, item.Availability)
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
		Images: images, SKUs: out, Availability: ProductAvailability(skuStates),
	}, nil
}
