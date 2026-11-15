//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
)

// runConcurrently starts n goroutines at the same moment and collects their errors.
func runConcurrently(n int, fn func(i int) error) []error {
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func uniqueID(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

// SC03: fifty buyers race for the last unit; exactly one reservation succeeds and the bucket never goes negative.
func TestFiftyBuyersRaceForTheLastUnit_SC03(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	sku := seedStock(t, pool, 1)
	run := uniqueID("IT-RACE")

	errs := runConcurrently(50, func(i int) error {
		_, err := repo.ReserveForOrder(context.Background(), fmt.Sprintf("%s-%d", run, i), testWarehouse,
			[]domain.ReserveLine{{SkuID: sku, Quantity: 1}})
		return err
	})
	var wins, soldOut int
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, domain.ErrInsufficientStock):
			soldOut++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 || soldOut != 49 {
		t.Fatalf("successes = %d, sold out = %d; want exactly 1 and 49", wins, soldOut)
	}
	if got := stockOf(t, repo, sku); got.Reserved != 1 || got.Available() != 0 {
		t.Fatalf("stock = %+v; want reserved 1, available 0", got)
	}
	if got := fmt.Sprint(movementTypes(t, repo, sku)); got != "[RESERVE]" {
		t.Fatalf("ledger = %s; only the winner may leave a movement", got)
	}
}

// Two buyers want two different multi-line baskets that share a SKU; no deadlock, stock stays consistent.
func TestOverlappingBasketsKeepStockConsistent(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	shared, onlyA, onlyB := seedStock(t, pool, 5), seedStock(t, pool, 100), seedStock(t, pool, 100)
	run := uniqueID("IT-BASKET")

	errs := runConcurrently(30, func(i int) error {
		lines := []domain.ReserveLine{{SkuID: onlyA, Quantity: 1}, {SkuID: shared, Quantity: 1}}
		if i%2 == 1 {
			lines = []domain.ReserveLine{{SkuID: shared, Quantity: 1}, {SkuID: onlyB, Quantity: 1}}
		}
		_, err := repo.ReserveForOrder(context.Background(), fmt.Sprintf("%s-%d", run, i), testWarehouse, lines)
		return err
	})
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, domain.ErrInsufficientStock) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 5 {
		t.Fatalf("successes = %d; the shared SKU has 5 units, want exactly 5", wins)
	}
	if got := stockOf(t, repo, shared); got.Reserved != 5 || got.Available() != 0 {
		t.Fatalf("shared = %+v", got)
	}
	// A rejected basket must not leave a line of itself behind: reserved quantities equal the winners' lines.
	a, b := stockOf(t, repo, onlyA).Reserved, stockOf(t, repo, onlyB).Reserved
	if a+b != 5 {
		t.Fatalf("reserved on the single-use SKUs = %d + %d, want 5 in total", a, b)
	}
}

// The same order id sent many times at once (a double click, a retry) holds the stock once and never errors.
func TestConcurrentRepeatsOfOneOrderHoldTheStockOnce(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	sku := seedStock(t, pool, 10)
	orderID := uniqueID("IT-SAME")

	errs := runConcurrently(10, func(int) error {
		_, err := repo.ReserveForOrder(context.Background(), orderID, testWarehouse, []domain.ReserveLine{{SkuID: sku, Quantity: 3}})
		return err
	})
	for _, err := range errs {
		if err != nil {
			t.Fatalf("a repeated reservation must succeed idempotently, got %v", err)
		}
	}
	if got := stockOf(t, repo, sku); got.Reserved != 3 || got.Available() != 7 {
		t.Fatalf("stock = %+v; want 3 reserved once", got)
	}
}

// SC02 on Postgres: 10 on hand, reserve 2 (available 8), commit (8), handover (on hand 8, available 8), with a ledger.
func TestOrderLifecycleNumbers_SC02(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()
	sku := seedStock(t, pool, 10)
	orderID := uniqueID("IT-SC02")
	check := func(step string, onHand, reserved, committed, available int64) {
		t.Helper()
		got := stockOf(t, repo, sku)
		if got.OnHand != onHand || got.Reserved != reserved || got.Committed != committed || got.Available() != available {
			t.Fatalf("%s: %+v; want on hand %d, reserved %d, committed %d, available %d", step, got, onHand, reserved, committed, available)
		}
	}
	check("start", 10, 0, 0, 10)
	if _, err := repo.ReserveForOrder(ctx, orderID, testWarehouse, []domain.ReserveLine{{SkuID: sku, Quantity: 2}}); err != nil {
		t.Fatal(err)
	}
	check("reserved", 10, 2, 0, 8)
	if _, err := repo.CommitOrderReservation(ctx, orderID); err != nil {
		t.Fatal(err)
	}
	check("committed", 10, 0, 2, 8)
	if _, err := repo.HandoverOrderShipment(ctx, orderID); err != nil {
		t.Fatal(err)
	}
	check("handed over", 8, 0, 0, 8)
	if got := fmt.Sprint(movementTypes(t, repo, sku)); got != "[RESERVE COMMIT HANDOVER]" {
		t.Fatalf("ledger = %s", got)
	}
	// Repeating a finished step changes nothing.
	if _, err := repo.CommitOrderReservation(ctx, orderID); err == nil {
		t.Fatal("committing a handed-over order must be refused")
	}
	check("after a refused repeat", 8, 0, 0, 8)
}

// Opening stock is once per SKU and warehouse even when managers submit it at the same moment.
func TestConcurrentOpeningStockIsRecordedOnce(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	sku := uniqueID("IT-OPEN-RACE")

	errs := runConcurrently(10, func(i int) error {
		return repo.RecordOpeningStock(context.Background(), testWarehouse, fmt.Sprintf("mgr-%d", i), "initial count",
			[]domain.ReserveLine{{SkuID: sku, Quantity: 7}})
	})
	wins := 0
	for _, err := range errs {
		var exists *OpeningStockExistsError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &exists):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("successes = %d, want exactly 1", wins)
	}
	if got := stockOf(t, repo, sku); got.OnHand != 7 {
		t.Fatalf("on hand = %d, want 7 (applied once)", got.OnHand)
	}
	if got := fmt.Sprint(movementTypes(t, repo, sku)); got != "[OPENING]" {
		t.Fatalf("ledger = %s", got)
	}
}
