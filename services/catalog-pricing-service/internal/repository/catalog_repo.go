package repository

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	ErrQuoteNotFound = errors.New("không tìm thấy báo giá")
	ErrSkuNotFound   = errors.New("không tìm thấy mã SKU trong bảng giá")
	// ErrSkuNotOrderable means the SKU exists but cannot be bought now: its product is not published or it has
	// no price in effect.
	ErrSkuNotOrderable = errors.New("SKU is not orderable")
)

type ProductCatalogDoc struct {
	ProductID         string           `json:"productId"`
	Name              string           `json:"name"`
	PrimaryCategoryID string           `json:"primaryCategoryId"`
	CategoryIDs       []string         `json:"categoryIds"`
	VariantAxes       []string         `json:"variantAxes"`
	Status            string           `json:"status"`
	Specs             map[string]any   `json:"specs"`
	Skus              []map[string]any `json:"skus"`
}

// productDocFromBSON maps a stored product document (without SKUs) to the API shape. The id is the document id
// (documents written before the product model have it in "product_id"); spec keys are normalized to camelCase
// so legacy snake_case specs read the same as new ones.
func productDocFromBSON(d bson.M) ProductCatalogDoc {
	id, _ := d["_id"].(string)
	if id == "" {
		id, _ = d["product_id"].(string)
	}
	name, _ := d["name"].(string)
	status, _ := d["status"].(string)
	primary, _ := d["primaryCategoryId"].(string)
	specs, err := domain.NormalizeSpecs(asStringMap(d["specs"]))
	if err != nil {
		log.Printf("[catalog] product %s has invalid specs, serving none: %v", id, err)
		specs = map[string]any{}
	}
	return ProductCatalogDoc{
		ProductID: id, Name: name, PrimaryCategoryID: primary, Status: status, Specs: specs,
		CategoryIDs: stringList(d["categoryIds"]), VariantAxes: stringList(d["variantAxes"]),
	}
}

func asStringMap(v any) map[string]any {
	switch m := v.(type) {
	case bson.M:
		return m
	case map[string]any:
		return m
	}
	return nil
}

func stringList(v any) []string {
	out := []string{}
	switch list := v.(type) {
	case bson.A:
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, list...)
	}
	return out
}

type SkuPrice struct {
	SkuID        string `json:"skuId"`
	ProductID    string `json:"productId"`
	SkuName      string `json:"skuName"`
	UnitPriceVND int64  `json:"unitPriceVnd"`
	VatRatePct   int    `json:"vatRatePct"`
	IsPublished  bool   `json:"isPublished"`
}

type CatalogPricingRepository struct {
	pgPool     *pgxpool.Pool
	mongoStore *db.MongoStore
	mu         sync.RWMutex
	skuPrices  map[string]SkuPrice
	history    map[string][]domain.PricePeriod // memory mode only: periods per SKU, newest first
	now        func() time.Time
	quotes     map[string]*domain.Quote
}

// NewCatalogPricingRepository returns a repository backed by the given stores. A nil store selects
// seeded in-memory data for that part, which is only meant for development and tests
// (ALLOW_MEMORY_FALLBACK=true). A configured store never falls back to seeds on failure.
func NewCatalogPricingRepository(pgPool *pgxpool.Pool, mongoStore *db.MongoStore) *CatalogPricingRepository {
	r := &CatalogPricingRepository{
		pgPool:     pgPool,
		mongoStore: mongoStore,
		skuPrices: map[string]SkuPrice{
			"SKU-BALO-BLACK-20L": {
				SkuID:        "SKU-BALO-BLACK-20L",
				ProductID:    "PROD-BALO-CITY",
				SkuName:      "Balo City Đen 20L",
				UnitPriceVND: 300000,
				VatRatePct:   10,
				IsPublished:  true,
			},
			"SKU-BOTTLE-BLUE-500ML": {
				SkuID:        "SKU-BOTTLE-BLUE-500ML",
				ProductID:    "PROD-BOTTLE-ECO",
				SkuName:      "Bình Nước Xanh 500ml",
				UnitPriceVND: 100000,
				VatRatePct:   10,
				IsPublished:  true,
			},
		},
		quotes:  make(map[string]*domain.Quote),
		history: map[string][]domain.PricePeriod{},
		now:     func() time.Time { return time.Now().UTC() },
	}
	for id, sp := range r.skuPrices {
		r.history[id] = []domain.PricePeriod{{UnitPriceVND: sp.UnitPriceVND, EffectiveFrom: time.Unix(0, 0).UTC(), ChangedBy: "seed"}}
	}
	return r
}

// GetSkuPrice returns the price in effect now for an orderable SKU. It returns ErrSkuNotFound for an unknown SKU
// and ErrSkuNotOrderable when the SKU is closed (unpublished product) or has no price in effect.
func (r *CatalogPricingRepository) GetSkuPrice(ctx context.Context, skuID string) (SkuPrice, error) {
	now := r.now()
	if r.pgPool != nil {
		var sp SkuPrice
		var price *int64
		err := r.pgPool.QueryRow(ctx, `
			SELECT p.sku_id, p.product_id, p.sku_name, p.vat_rate_pct, p.is_published, h.unit_price_vnd
			FROM sku_prices p
			LEFT JOIN LATERAL (
				SELECT unit_price_vnd FROM sku_price_history
				WHERE sku_id = p.sku_id AND effective_from <= $2 AND (effective_to IS NULL OR effective_to > $2)
				ORDER BY effective_from DESC LIMIT 1
			) h ON TRUE
			WHERE p.sku_id = $1
		`, skuID, now).Scan(&sp.SkuID, &sp.ProductID, &sp.SkuName, &sp.VatRatePct, &sp.IsPublished, &price)
		if errors.Is(err, pgx.ErrNoRows) {
			return SkuPrice{}, ErrSkuNotFound
		}
		if err != nil {
			return SkuPrice{}, dependency.Wrap(err)
		}
		if !sp.IsPublished || price == nil {
			return SkuPrice{}, ErrSkuNotOrderable
		}
		sp.UnitPriceVND = *price
		return sp, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	sp, ok := r.skuPrices[skuID]
	if !ok {
		return SkuPrice{}, ErrSkuNotFound
	}
	period, active := effectivePeriod(r.history[skuID], now)
	if !sp.IsPublished || !active {
		return SkuPrice{}, ErrSkuNotOrderable
	}
	sp.UnitPriceVND = period.UnitPriceVND
	return sp, nil
}

func (r *CatalogPricingRepository) ListPublishedProducts(ctx context.Context) ([]ProductCatalogDoc, error) {
	if r.mongoStore != nil {
		docs, err := r.mongoStore.FindAll(ctx, "catalog_products", bson.D{{Key: "status", Value: "PUBLISHED"}})
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		out := make([]ProductCatalogDoc, 0, len(docs))
		for _, d := range docs {
			doc := productDocFromBSON(d)
			skus, err := r.loadProductSkus(ctx, doc.ProductID)
			if err != nil {
				return nil, err
			}
			doc.Skus = skus
			out = append(out, doc)
		}
		return out, nil
	}

	return []ProductCatalogDoc{
		{
			ProductID:   "PROD-BALO-CITY",
			Name:        "Balo City Chống Nước Đa Năng",
			VariantAxes: []string{"color", "capacity"},
			Status:      "PUBLISHED",
			Specs: map[string]any{
				"capacityLiters":        20,
				"laptopCompartmentInch": 15.6,
				"material":              "Cordura Ballistic Nylon",
				"waterResistance":       "IPX4",
			},
			Skus: []map[string]any{
				{"skuId": "SKU-BALO-BLACK-20L", "skuName": "Balo City Đen 20L", "unitPriceVnd": 300000, "vatIncludedPct": 10},
			},
		},
		{
			ProductID:   "PROD-BOTTLE-ECO",
			Name:        "Bình Nước Giữ Nhiệt Eco",
			VariantAxes: []string{"color", "capacity"},
			Status:      "PUBLISHED",
			Specs: map[string]any{
				"volumeMl":          500,
				"keepHotHours":      12,
				"keepColdHours":     24,
				"innerCoreMaterial": "Inox 316",
			},
			Skus: []map[string]any{
				{"skuId": "SKU-BOTTLE-BLUE-500ML", "skuName": "Bình Nước Xanh 500ml", "unitPriceVnd": 100000, "vatIncludedPct": 10},
			},
		},
	}, nil
}

func (r *CatalogPricingRepository) loadProductSkus(ctx context.Context, productID string) ([]map[string]any, error) {
	if r.pgPool != nil {
		rows, err := r.pgPool.Query(ctx, `
			SELECT sku_id, sku_name, unit_price_vnd, vat_rate_pct
			FROM sku_prices
			WHERE product_id = $1 AND is_published = TRUE
			ORDER BY sku_id
		`, productID)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		defer rows.Close()
		skus := []map[string]any{}
		for rows.Next() {
			var skuID, skuName string
			var price int64
			var vat int
			if err := rows.Scan(&skuID, &skuName, &price, &vat); err != nil {
				return nil, dependency.Wrap(err)
			}
			skus = append(skus, map[string]any{
				"skuId":          skuID,
				"skuName":        skuName,
				"unitPriceVnd":   price,
				"vatIncludedPct": vat,
			})
		}
		if err := rows.Err(); err != nil {
			return nil, dependency.Wrap(err)
		}
		return skus, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var skus []map[string]any
	for _, sp := range r.skuPrices {
		if sp.ProductID == productID {
			skus = append(skus, map[string]any{
				"skuId":          sp.SkuID,
				"skuName":        sp.SkuName,
				"unitPriceVnd":   sp.UnitPriceVND,
				"vatIncludedPct": sp.VatRatePct,
			})
		}
	}
	return skus, nil
}

func (r *CatalogPricingRepository) SaveQuote(ctx context.Context, q *domain.Quote) error {
	linesJSON, err := json.Marshal(q.Lines)
	if err != nil {
		return err
	}
	if r.pgPool != nil {
		_, err := r.pgPool.Exec(ctx, `
			INSERT INTO quotes (quote_id, user_id, voucher_code, subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd, lines_json, expires_at, address_id, discount_pct, version_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, NULLIF($10, ''), $11, $12)
			ON CONFLICT (quote_id) DO UPDATE SET
				subtotal_vnd = EXCLUDED.subtotal_vnd,
				discount_vnd = EXCLUDED.discount_vnd,
				shipping_fee_vnd = EXCLUDED.shipping_fee_vnd,
				total_payable_vnd = EXCLUDED.total_payable_vnd,
				lines_json = EXCLUDED.lines_json,
				expires_at = EXCLUDED.expires_at,
				address_id = EXCLUDED.address_id,
				discount_pct = EXCLUDED.discount_pct,
				version_hash = EXCLUDED.version_hash
		`, q.QuoteID, q.UserID, q.VoucherCode, q.SubtotalVND, q.DiscountVND, q.ShippingFeeVND, q.TotalPayableVND, string(linesJSON), q.ExpiresAt, q.AddressID, q.DiscountPct, q.Version)
		if err != nil {
			return dependency.Wrap(err)
		}
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *q
	r.quotes[q.QuoteID] = &cp
	return nil
}

func (r *CatalogPricingRepository) GetQuoteByID(ctx context.Context, quoteID string) (*domain.Quote, error) {
	if r.pgPool != nil {
		var q domain.Quote
		var linesRaw []byte
		err := r.pgPool.QueryRow(ctx, `
			SELECT quote_id, user_id, COALESCE(voucher_code, ''), subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd, lines_json, expires_at,
				COALESCE(address_id, ''), discount_pct, version_hash
			FROM quotes
			WHERE quote_id = $1
		`, quoteID).Scan(&q.QuoteID, &q.UserID, &q.VoucherCode, &q.SubtotalVND, &q.DiscountVND, &q.ShippingFeeVND, &q.TotalPayableVND, &linesRaw, &q.ExpiresAt,
			&q.AddressID, &q.DiscountPct, &q.Version)
		if err == nil {
			_ = json.Unmarshal(linesRaw, &q.Lines)
			return &q, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrQuoteNotFound
		}
		return nil, dependency.Wrap(err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	q, ok := r.quotes[quoteID]
	if !ok {
		return nil, ErrQuoteNotFound
	}
	cp := *q
	return &cp, nil
}

// SetOrderable makes exactly the listed SKUs of the product orderable (quotes only accept published SKU
// rows); every other SKU of the product is closed. An empty list closes the whole product.
func (r *CatalogPricingRepository) SetOrderable(ctx context.Context, productID string, orderableSKUIDs []string) error {
	if orderableSKUIDs == nil {
		orderableSKUIDs = []string{}
	}
	if r.pgPool != nil {
		_, err := r.pgPool.Exec(ctx, `
			UPDATE sku_prices SET is_published = (sku_id = ANY($2)), updated_at = NOW()
			WHERE product_id = $1`, productID, orderableSKUIDs)
		return dependency.Wrap(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, sp := range r.skuPrices {
		if sp.ProductID == productID {
			sp.IsPublished = slices.Contains(orderableSKUIDs, id)
			r.skuPrices[id] = sp
		}
	}
	return nil
}
