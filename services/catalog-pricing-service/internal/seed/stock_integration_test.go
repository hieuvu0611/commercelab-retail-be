//go:build integration

package seed

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestStockSeedIsIdempotentAndLedgered(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_INVENTORY")
	if dsn == "" {
		t.Skip("DATABASE_URL_INVENTORY is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	plan := StockPlan()

	if _, err := LoadStock(ctx, pool, plan); err != nil {
		t.Fatalf("first run: %v", err)
	}
	skus := make([]string, 0, len(plan))
	for _, s := range plan {
		skus = append(skus, s.SkuID)
	}
	// Other packages' integration tests write to the same schema in parallel, so only the seeded SKUs are counted.
	count := func(q string) (n int) {
		if err := pool.QueryRow(ctx, q, skus).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	items := count(`SELECT COUNT(*) FROM inventory.stock_items WHERE sku_id = ANY($1)`)
	moves := count(`SELECT COUNT(*) FROM inventory.stock_movements WHERE sku_id = ANY($1)`)
	created, err := LoadStock(ctx, pool, plan)
	if err != nil || created != 0 {
		t.Fatalf("second run created %d rows (err %v), want 0", created, err)
	}
	if got := count(`SELECT COUNT(*) FROM inventory.stock_items WHERE sku_id = ANY($1)`); got != items {
		t.Fatalf("stock rows %d -> %d", items, got)
	}
	if got := count(`SELECT COUNT(*) FROM inventory.stock_movements WHERE sku_id = ANY($1)`); got != moves {
		t.Fatalf("movements %d -> %d", moves, got)
	}
	// Ledger explains on hand: every stocked seeded row has an OPENING movement.
	if missing := count(`SELECT COUNT(*) FROM inventory.stock_items s WHERE s.on_hand > 0 AND s.sku_id = ANY($1) AND NOT EXISTS (
		SELECT 1 FROM inventory.stock_movements m WHERE m.sku_id = s.sku_id AND m.warehouse_id = s.warehouse_id AND m.type = 'OPENING')`); missing != 0 {
		t.Fatalf("%d stocked rows have no OPENING movement", missing)
	}
	// The SC01 SKUs keep the migration stock (10 on hand).
	if n := count(`SELECT on_hand FROM inventory.stock_items WHERE sku_id = 'SKU-BALO-BLACK-20L' AND warehouse_id = 'WH-HCM-01' AND $1::text[] IS NOT NULL`); n != 10 {
		t.Fatalf("SC01 backpack on hand = %d, want 10", n)
	}
}
