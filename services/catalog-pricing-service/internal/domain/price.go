package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// MaxUnitPriceVND bounds a price so quote arithmetic can never overflow.
	MaxUnitPriceVND int64 = 1_000_000_000
	// priceClockSkew is how far in the past an effectiveFrom may be and still mean "now".
	priceClockSkew = time.Minute
)

// PricePeriod is a price valid from EffectiveFrom (inclusive) to EffectiveTo (exclusive, nil = open ended).
type PricePeriod struct {
	UnitPriceVND  int64      `json:"unitPriceVnd"`
	EffectiveFrom time.Time  `json:"effectiveFrom"`
	EffectiveTo   *time.Time `json:"effectiveTo,omitempty"`
	ChangedBy     string     `json:"changedBy,omitempty"`
}

func (p PricePeriod) activeAt(at time.Time) bool {
	return !p.EffectiveFrom.After(at) && (p.EffectiveTo == nil || p.EffectiveTo.After(at))
}

// SetPriceInput is one price change. EffectiveFrom is never before the time of the request. Orderable only
// matters when the SKU has no price row yet: it says whether the new row starts open for quotes.
type SetPriceInput struct {
	SkuID         string
	ProductID     string
	SkuName       string
	UnitPriceVND  int64
	EffectiveFrom time.Time
	ChangedBy     string
	Orderable     bool
}

// PriceStore persists SKU prices and their history. SetSkuPrice supersedes every price that starts at or after
// the new one and closes the period it interrupts.
type PriceStore interface {
	SetSkuPrice(ctx context.Context, in SetPriceInput) error
	// SkuPriceHistory returns every period of the SKU, newest first.
	SkuPriceHistory(ctx context.Context, skuID string) ([]PricePeriod, error)
}

// PriceView is the price of a SKU: the period in effect now (nil when there is none) and the full history.
type PriceView struct {
	SkuID   string        `json:"skuId"`
	Current *PricePeriod  `json:"current"`
	History []PricePeriod `json:"history"`
}

// PriceService applies the pricing rules (MARKETING).
type PriceService struct {
	products *ProductService
	prices   PriceStore
	now      func() time.Time
}

func NewPriceService(products *ProductService, prices PriceStore) *PriceService {
	return &PriceService{products: products, prices: prices, now: func() time.Time { return time.Now().UTC() }}
}

func skuDisplayName(p Product, s SKU) string {
	values := make([]string, 0, len(p.VariantAxes))
	for _, axis := range p.VariantAxes {
		if v := strings.TrimSpace(s.Attributes[axis]); v != "" {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return p.Name
	}
	return p.Name + " - " + strings.Join(values, " / ")
}

// SetPrice sets the price of a SKU from effectiveFrom (nil or a moment within a minute in the past means now).
// Prices are whole VND between 1 and MaxUnitPriceVND, and cannot be backdated.
func (s *PriceService) SetPrice(ctx context.Context, actor, skuID string, unitPriceVND int64, effectiveFrom *time.Time) (PriceView, error) {
	now := s.now()
	fields := map[string][]string{}
	if unitPriceVND < 1 || unitPriceVND > MaxUnitPriceVND {
		fields["unitPriceVnd"] = append(fields["unitPriceVnd"], fmt.Sprintf("price must be between 1 and %d VND", MaxUnitPriceVND))
	}
	from := now
	if effectiveFrom != nil {
		switch {
		case effectiveFrom.Before(now.Add(-priceClockSkew)):
			fields["effectiveFrom"] = append(fields["effectiveFrom"], "effectiveFrom cannot be in the past")
		case effectiveFrom.After(now):
			from = effectiveFrom.UTC()
		}
	}
	if len(fields) > 0 {
		return PriceView{}, &ValidationError{Fields: fields}
	}
	sku, product, err := s.products.SKUWithProduct(ctx, skuID)
	if err != nil {
		return PriceView{}, err
	}
	err = s.prices.SetSkuPrice(ctx, SetPriceInput{
		SkuID: sku.SkuID, ProductID: product.ID, SkuName: skuDisplayName(product, sku), UnitPriceVND: unitPriceVND,
		EffectiveFrom: from, ChangedBy: actor, Orderable: product.Status == ProductPublished && sku.IsActive,
	})
	if err != nil {
		return PriceView{}, err
	}
	return s.GetPrice(ctx, skuID)
}

// GetPrice returns the current price and the history of a SKU.
func (s *PriceService) GetPrice(ctx context.Context, skuID string) (PriceView, error) {
	if _, _, err := s.products.SKUWithProduct(ctx, skuID); err != nil {
		return PriceView{}, err
	}
	history, err := s.prices.SkuPriceHistory(ctx, skuID)
	if err != nil {
		return PriceView{}, err
	}
	if history == nil {
		history = []PricePeriod{}
	}
	view := PriceView{SkuID: skuID, History: history}
	now := s.now()
	for i := range history {
		if history[i].activeAt(now) {
			current := history[i]
			view.Current = &current
			break
		}
	}
	return view, nil
}

// PriceRow is the current price of one SKU in the price list; UnitPriceVND is nil when no price is in effect.
type PriceRow struct {
	SkuID         string            `json:"skuId"`
	ProductID     string            `json:"productId"`
	ProductName   string            `json:"productName"`
	Attributes    map[string]string `json:"attributes"`
	UnitPriceVND  *int64            `json:"unitPriceVnd"`
	EffectiveFrom *time.Time        `json:"effectiveFrom"`
}

// ListPrices returns the SKUs of one page of products (any status, matched by q on name and slug) with the price
// in effect now. Pagination counts products, so a page can hold more rows than limit.
func (s *PriceService) ListPrices(ctx context.Context, q string, page, limit int) ([]PriceRow, int64, error) {
	products, total, err := s.products.ListProducts(ctx, ProductFilter{Q: q, Page: page, Limit: limit})
	if err != nil {
		return nil, 0, err
	}
	now := s.now()
	rows := []PriceRow{}
	for _, p := range products {
		skus, err := s.products.siblings(ctx, p.ID)
		if err != nil {
			return nil, 0, err
		}
		sort.Slice(skus, func(i, j int) bool { return skus[i].SkuID < skus[j].SkuID })
		for _, k := range skus {
			row := PriceRow{SkuID: k.SkuID, ProductID: p.ID, ProductName: p.Name, Attributes: k.Attributes}
			history, err := s.prices.SkuPriceHistory(ctx, k.SkuID)
			if err != nil {
				return nil, 0, err
			}
			for _, period := range history {
				if period.activeAt(now) {
					amount, from := period.UnitPriceVND, period.EffectiveFrom
					row.UnitPriceVND, row.EffectiveFrom = &amount, &from
					break
				}
			}
			rows = append(rows, row)
		}
	}
	return rows, total, nil
}
