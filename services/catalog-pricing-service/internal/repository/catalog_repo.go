package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/db"
	"github.com/hieuvu0211/full-service-shopping/be/services/catalog-pricing-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	ErrQuoteNotFound = errors.New("không tìm thấy báo giá")
	ErrSkuNotFound   = errors.New("không tìm thấy mã SKU trong bảng giá")
)

type ProductCatalogDoc struct {
	ProductID    string           `json:"productId"`
	Name         string           `json:"name"`
	CategoryType string           `json:"categoryType"`
	Status       string           `json:"status"`
	Specs        map[string]any   `json:"specs"`
	Skus         []map[string]any `json:"skus"`
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
	quotes     map[string]*domain.Quote
}

func NewCatalogPricingRepository(pgPool *pgxpool.Pool, mongoStore *db.MongoStore) *CatalogPricingRepository {
	return &CatalogPricingRepository{
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
		quotes: make(map[string]*domain.Quote),
	}
}

func (r *CatalogPricingRepository) GetSkuPrice(ctx context.Context, skuID string) (SkuPrice, error) {
	if r.pgPool != nil {
		var sp SkuPrice
		err := r.pgPool.QueryRow(ctx, `
			SELECT sku_id, product_id, sku_name, unit_price_vnd, vat_rate_pct, is_published
			FROM sku_prices
			WHERE sku_id = $1 AND is_published = TRUE
		`, skuID).Scan(&sp.SkuID, &sp.ProductID, &sp.SkuName, &sp.UnitPriceVND, &sp.VatRatePct, &sp.IsPublished)
		if err == nil {
			return sp, nil
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	sp, ok := r.skuPrices[skuID]
	if !ok || !sp.IsPublished {
		return SkuPrice{}, ErrSkuNotFound
	}
	return sp, nil
}

func (r *CatalogPricingRepository) ListPublishedProducts(ctx context.Context) ([]ProductCatalogDoc, error) {
	if r.mongoStore != nil {
		docs, err := r.mongoStore.FindAll(ctx, "catalog_products", bson.D{{Key: "status", Value: "PUBLISHED"}})
		if err == nil && len(docs) > 0 {
			out := make([]ProductCatalogDoc, 0, len(docs))
			for _, d := range docs {
				prodID, _ := d["product_id"].(string)
				name, _ := d["name"].(string)
				cat, _ := d["category_type"].(string)
				status, _ := d["status"].(string)
				specs, _ := d["specs"].(bson.M)
				out = append(out, ProductCatalogDoc{
					ProductID:    prodID,
					Name:         name,
					CategoryType: cat,
					Status:       status,
					Specs:        specs,
					Skus:         r.loadProductSkus(ctx, prodID),
				})
			}
			return out, nil
		}
	}

	return []ProductCatalogDoc{
		{
			ProductID:    "PROD-BALO-CITY",
			Name:         "Balo City Chống Nước Đa Năng",
			CategoryType: "BACKPACK",
			Status:       "PUBLISHED",
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
			ProductID:    "PROD-BOTTLE-ECO",
			Name:         "Bình Nước Giữ Nhiệt Eco",
			CategoryType: "THERMOS_BOTTLE",
			Status:       "PUBLISHED",
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

func (r *CatalogPricingRepository) loadProductSkus(ctx context.Context, productID string) []map[string]any {
	if r.pgPool != nil {
		rows, err := r.pgPool.Query(ctx, `
			SELECT sku_id, sku_name, unit_price_vnd, vat_rate_pct
			FROM sku_prices
			WHERE product_id = $1 AND is_published = TRUE
			ORDER BY sku_id
		`, productID)
		if err == nil {
			defer rows.Close()
			var skus []map[string]any
			for rows.Next() {
				var skuID, skuName string
				var price int64
				var vat int
				if err := rows.Scan(&skuID, &skuName, &price, &vat); err == nil {
					skus = append(skus, map[string]any{
						"skuId":          skuID,
						"skuName":        skuName,
						"unitPriceVnd":   price,
						"vatIncludedPct": vat,
					})
				}
			}
			if len(skus) > 0 {
				return skus
			}
		}
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
	return skus
}

func (r *CatalogPricingRepository) SaveQuote(ctx context.Context, q *domain.Quote) error {
	linesJSON, err := json.Marshal(q.Lines)
	if err != nil {
		return err
	}
	if r.pgPool != nil {
		_, err := r.pgPool.Exec(ctx, `
			INSERT INTO quotes (quote_id, user_id, voucher_code, subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd, lines_json, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
			ON CONFLICT (quote_id) DO UPDATE SET
				subtotal_vnd = EXCLUDED.subtotal_vnd,
				discount_vnd = EXCLUDED.discount_vnd,
				shipping_fee_vnd = EXCLUDED.shipping_fee_vnd,
				total_payable_vnd = EXCLUDED.total_payable_vnd,
				lines_json = EXCLUDED.lines_json,
				expires_at = EXCLUDED.expires_at
		`, q.QuoteID, q.UserID, q.VoucherCode, q.SubtotalVND, q.DiscountVND, q.ShippingFeeVND, q.TotalPayableVND, string(linesJSON), q.ExpiresAt)
		if err == nil {
			return nil
		}
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
			SELECT quote_id, user_id, COALESCE(voucher_code, ''), subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd, lines_json, expires_at
			FROM quotes
			WHERE quote_id = $1
		`, quoteID).Scan(&q.QuoteID, &q.UserID, &q.VoucherCode, &q.SubtotalVND, &q.DiscountVND, &q.ShippingFeeVND, &q.TotalPayableVND, &linesRaw, &q.ExpiresAt)
		if err == nil {
			_ = json.Unmarshal(linesRaw, &q.Lines)
			if q.SubtotalVND > 0 {
				q.DiscountPct = (q.DiscountVND * 100) / q.SubtotalVND
			}
			return &q, nil
		}
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
