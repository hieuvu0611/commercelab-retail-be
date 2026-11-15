package seed

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedWarehouse is the warehouse that receives the demo opening stock.
const SeedWarehouse = "WH-HCM-01"

// StockSpec is the demo opening stock of one SKU.
type StockSpec struct {
	SkuID string
	Qty   int64
}

// StockPlan returns a deterministic opening stock for every seeded SKU so the storefront shows all three states
// (D-12, LOW is 1..3): of every ten SKUs six are well stocked, two are low, one sits at 4 (just above LOW), one is out.
func StockPlan() []StockSpec {
	var plan []StockSpec
	i := 0
	for _, p := range Products() {
		for _, k := range p.SKUs() {
			var qty int64
			switch i % 10 {
			case 6, 7:
				qty = int64(1 + i%3)
			case 8:
				qty = 4
			case 9:
				qty = 0
			default:
				qty = int64(15 + (i*7)%40)
			}
			plan = append(plan, StockSpec{SkuID: k.Code, Qty: qty})
			i++
		}
	}
	return plan
}

// LoadStock writes the plan into the inventory schema in one transaction. It never changes a SKU that already has
// a stock row (the SC01 SKUs keep their migration stock), so running it again changes nothing. Each new row with
// stock gets its OPENING movement (actor "seed") like opening stock recorded through the API.
func LoadStock(ctx context.Context, pool *pgxpool.Pool, plan []StockSpec) (created int, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, s := range plan {
		tag, err := tx.Exec(ctx, `
			INSERT INTO inventory.stock_items (sku_id, warehouse_id, on_hand) VALUES ($1, $2, $3)
			ON CONFLICT (sku_id, warehouse_id) DO NOTHING
		`, s.SkuID, SeedWarehouse, s.Qty)
		if err != nil {
			return 0, fmt.Errorf("stock of %s: %w", s.SkuID, err)
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		created++
		if s.Qty == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory.stock_movements (sku_id, warehouse_id, type, qty, ref_type, actor_id, reason)
			VALUES ($1, $2, 'OPENING', $3, 'SEED', 'seed', 'demo opening stock')
			ON CONFLICT (sku_id, warehouse_id) WHERE type = 'OPENING' DO NOTHING
		`, s.SkuID, SeedWarehouse, s.Qty); err != nil {
			return 0, fmt.Errorf("opening movement of %s: %w", s.SkuID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return created, nil
}
