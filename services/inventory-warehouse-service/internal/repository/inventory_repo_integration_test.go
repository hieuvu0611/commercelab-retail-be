//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testWarehouse = "WH-HCM-01"

// integrationPool connects to the real inventory schema started by scripts/test-integration.sh.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL_INVENTORY")
	if dsn == "" {
		t.Skip("DATABASE_URL_INVENTORY is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedStock inserts a uniquely named SKU so tests never depend on each other or on the seed data.
func seedStock(t *testing.T, pool *pgxpool.Pool, onHand int64) string {
	t.Helper()
	sku := fmt.Sprintf("IT-SKU-%d", time.Now().UnixNano())
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO stock_items (sku_id, warehouse_id, on_hand) VALUES ($1, $2, $3)`, sku, testWarehouse, onHand); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	return sku
}

func stockOf(t *testing.T, repo *InventoryRepository, sku string) domain.StockItem {
	t.Helper()
	st, ok, err := repo.GetStock(context.Background(), testWarehouse, sku)
	if err != nil || !ok {
		t.Fatalf("GetStock(%s) = ok %v, err %v", sku, ok, err)
	}
	return st
}

func TestReserveIsAllOrNothingAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()

	plentiful := seedStock(t, pool, 10)
	scarce := seedStock(t, pool, 1)
	orderID := fmt.Sprintf("IT-ORDER-%d", time.Now().UnixNano())

	// The second line cannot be satisfied, so the first line must not stay reserved either.
	_, err := repo.ReserveForOrder(ctx, orderID, testWarehouse, []domain.ReserveLine{
		{SkuID: plentiful, Quantity: 3},
		{SkuID: scarce, Quantity: 2},
	})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("err = %v, want ErrInsufficientStock", err)
	}
	if got := stockOf(t, repo, plentiful); got.Reserved != 0 || got.Available() != 10 {
		t.Fatalf("plentiful SKU changed after a failed reservation: %+v", got)
	}
	if got := stockOf(t, repo, scarce); got.Reserved != 0 || got.Available() != 1 {
		t.Fatalf("scarce SKU changed after a failed reservation: %+v", got)
	}
	if _, err := repo.CommitOrderReservation(ctx, orderID); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("a failed reservation must leave no record, got err %v", err)
	}
}

func TestReserveHoldsEveryLineAndIsIdempotentAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()

	first := seedStock(t, pool, 10)
	second := seedStock(t, pool, 5)
	orderID := fmt.Sprintf("IT-ORDER-%d", time.Now().UnixNano())
	lines := []domain.ReserveLine{{SkuID: first, Quantity: 3}, {SkuID: second, Quantity: 2}}

	rec, err := repo.ReserveForOrder(ctx, orderID, testWarehouse, lines)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if rec.Status != "ACTIVE_10M" {
		t.Fatalf("status = %s, want ACTIVE_10M", rec.Status)
	}
	if got := stockOf(t, repo, first); got.Reserved != 3 || got.Available() != 7 {
		t.Fatalf("first SKU = %+v, want reserved 3 / available 7", got)
	}
	if got := stockOf(t, repo, second); got.Reserved != 2 || got.Available() != 3 {
		t.Fatalf("second SKU = %+v, want reserved 2 / available 3", got)
	}

	// Repeating the same order must not hold the stock twice.
	if _, err := repo.ReserveForOrder(ctx, orderID, testWarehouse, lines); err != nil {
		t.Fatalf("repeat reserve: %v", err)
	}
	if got := stockOf(t, repo, first); got.Reserved != 3 {
		t.Fatalf("repeat reservation double-held stock: %+v", got)
	}

	if _, err := repo.ReleaseOrderReservation(ctx, orderID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := stockOf(t, repo, first); got.Reserved != 0 || got.Available() != 10 {
		t.Fatalf("release did not return stock: %+v", got)
	}
}
