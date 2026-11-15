//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
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

// Two orders that list the same SKUs in opposite order must both finish: rows are locked in sorted SKU order.
func TestOppositeLineOrdersDoNotDeadlockAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)

	a, b := seedStock(t, pool, 1000), seedStock(t, pool, 1000)
	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lines := []domain.ReserveLine{{SkuID: a, Quantity: 1}, {SkuID: b, Quantity: 1}}
			if i%2 == 1 {
				lines = []domain.ReserveLine{{SkuID: b, Quantity: 1}, {SkuID: a, Quantity: 1}}
			}
			_, err := repo.ReserveForOrder(context.Background(), fmt.Sprintf("IT-DL-%d-%d", time.Now().UnixNano(), i), testWarehouse, lines)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reservation failed: %v", err)
		}
	}
	if got := stockOf(t, repo, a); got.Reserved != workers {
		t.Fatalf("reserved = %d, want %d", got.Reserved, workers)
	}
}

func movementTypes(t *testing.T, repo *InventoryRepository, sku string) []string {
	t.Helper()
	rows, total, err := repo.ListMovements(context.Background(), MovementFilter{WarehouseID: testWarehouse, SkuID: sku, Page: 1, Limit: 50})
	if err != nil || int(total) != len(rows) {
		t.Fatalf("ListMovements(%s): total %d, rows %d, err %v", sku, total, len(rows), err)
	}
	var types []string
	for i := len(rows) - 1; i >= 0; i-- { // oldest first
		types = append(types, rows[i].Type)
	}
	return types
}

func TestOpeningStockIsOnceAndLedgerFollowsTheOrderLifecycleAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()
	sku := fmt.Sprintf("IT-OPEN-%d", time.Now().UnixNano())
	other := sku + "-B"

	if err := repo.RecordOpeningStock(ctx, testWarehouse, "mgr-1", "initial count", []domain.ReserveLine{{SkuID: sku, Quantity: 10}}); err != nil {
		t.Fatalf("opening stock: %v", err)
	}
	var exists *OpeningStockExistsError
	err := repo.RecordOpeningStock(ctx, testWarehouse, "mgr-1", "again", []domain.ReserveLine{{SkuID: other, Quantity: 1}, {SkuID: sku, Quantity: 1}})
	if !errors.As(err, &exists) || exists.SkuID != sku {
		t.Fatalf("second opening = %v, want OpeningStockExistsError for %s", err, sku)
	}
	if _, ok, _ := repo.GetStock(ctx, testWarehouse, other); ok {
		t.Fatal("a rejected opening call must apply nothing")
	}

	orderID := "IT-ORDER-" + sku
	steps := []func() error{
		func() error {
			_, e := repo.ReserveForOrder(ctx, orderID, testWarehouse, []domain.ReserveLine{{SkuID: sku, Quantity: 2}})
			return e
		},
		func() error { _, e := repo.CommitOrderReservation(ctx, orderID); return e },
		func() error { _, e := repo.HandoverOrderShipment(ctx, orderID); return e },
		func() error { _, e := repo.HandoverOrderShipment(ctx, orderID); return e }, // repeated: decrements nothing more
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if got := stockOf(t, repo, sku); got.OnHand != 8 || got.Available() != 8 {
		t.Fatalf("after handover: %+v, want on hand 8 and available 8", got)
	}
	got := movementTypes(t, repo, sku)
	want := []string{"OPENING", "RESERVE", "COMMIT", "HANDOVER"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ledger = %v, want %v", got, want)
	}

	// A failed (all-or-nothing) reservation leaves no movement behind.
	if _, err := repo.ReserveForOrder(ctx, orderID+"-X", testWarehouse, []domain.ReserveLine{{SkuID: sku, Quantity: 99}}); !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("over-reserve = %v", err)
	}
	if n := len(movementTypes(t, repo, sku)); n != 4 {
		t.Fatalf("ledger rows = %d, want 4", n)
	}
}

func TestAvailableBySkuUsesTheBucketFormulaAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()
	sku := seedStock(t, pool, 100)
	if _, err := pool.Exec(ctx, `UPDATE stock_items SET unsellable = 5, reserved = 10, committed = 15 WHERE sku_id = $1`, sku); err != nil {
		t.Fatal(err)
	}
	got, err := repo.AvailableBySku(ctx, testWarehouse, []string{sku, "IT-NO-SUCH-SKU"})
	if err != nil {
		t.Fatal(err)
	}
	if got[sku] != 70 { // I01 example: 100 - 5 - 10 - 15
		t.Fatalf("available = %d, want 70", got[sku])
	}
	if _, ok := got["IT-NO-SUCH-SKU"]; ok {
		t.Fatal("an unknown SKU must be absent (reported OUT by the handler)")
	}
}

// SC05 on real Postgres: a plain reservation expires at 10:10, a held one survives until the controlled release.
func TestExpirySweepAndExceptionHoldAgainstPostgres(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()
	clock := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	repo.SetClock(clock.Now)
	start := clock.Now()

	sku := seedStock(t, pool, 10)
	suffix := fmt.Sprint(time.Now().UnixNano())
	plain, held := "IT-EXP-PLAIN-"+suffix, "IT-EXP-HELD-"+suffix
	for _, id := range []string{plain, held} {
		if _, err := repo.ReserveForOrder(ctx, id, testWarehouse, []domain.ReserveLine{{SkuID: sku, Quantity: 2}}); err != nil {
			t.Fatalf("reserve %s: %v", id, err)
		}
	}
	clock.Set(start.Add(9 * time.Minute))
	rec, err := repo.PlaceExceptionHold(ctx, held)
	if err != nil || rec.Status != "EXCEPTION_HOLD_30M" || !rec.ExpiresAt.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("hold = %+v, err %v; want EXCEPTION_HOLD_30M until start+30m", rec, err)
	}

	clock.Set(start.Add(11 * time.Minute))
	if _, err := repo.ExpireDueReservations(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// Other tests' reservations may also expire in this sweep; only ours are asserted.
	if got := stockOf(t, repo, sku); got.Reserved != 2 || got.Available() != 8 {
		t.Fatalf("after the sweep %+v; want only the held 2 units reserved", got)
	}
	if r, _ := repo.GetReservation(ctx, plain); r.Status != "RELEASED" {
		t.Fatalf("plain reservation = %s, want RELEASED", r.Status)
	}
	if r, _ := repo.GetReservation(ctx, held); r.Status != "EXCEPTION_HOLD_30M" {
		t.Fatalf("held reservation = %s, want EXCEPTION_HOLD_30M", r.Status)
	}
	if _, err := repo.PlaceExceptionHold(ctx, plain); !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("hold on a released reservation = %v", err)
	}
	if got := fmt.Sprint(movementTypes(t, repo, sku)); got != "[RESERVE RESERVE RELEASE]" {
		t.Fatalf("ledger = %s", got)
	}
	clock.Set(start.Add(31 * time.Minute))
	if _, err := repo.ExpireDueReservations(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := repo.GetReservation(ctx, held); r.Status != "EXCEPTION_HOLD_30M" {
		t.Fatalf("past 30 minutes the hold must wait for the controlled release, got %s", r.Status)
	}
}

func TestConcurrentCommittedReleasesGiveStockBackOnceAgainstPostgres_B05(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()
	sku := seedStock(t, pool, 10)
	orderID := "IT-RC-" + sku
	if _, err := repo.ReserveForOrder(ctx, orderID, testWarehouse, []domain.ReserveLine{{SkuID: sku, Quantity: 3}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitOrderReservation(ctx, orderID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := repo.ReleaseCommittedReservation(ctx, orderID, domain.SystemActor); err != nil {
				t.Errorf("release: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := stockOf(t, repo, sku); got.OnHand != 10 || got.Committed != 0 || got.Available() != 10 {
		t.Fatalf("stock = %+v, want everything available again", got)
	}
	if got := movementTypes(t, repo, sku); fmt.Sprint(got) != fmt.Sprint([]string{"RESERVE", "COMMIT", "RELEASE"}) {
		t.Fatalf("ledger = %v, want exactly one RELEASE", got)
	}
	if _, err := repo.HandoverOrderShipment(ctx, orderID); !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("handover after release = %v, want ErrInvalidReservationState", err)
	}
}

func TestHandoverAndCommittedReleaseRaceHasOneWinnerAgainstPostgres_SC06(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInventoryRepository(pool)
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		sku := seedStock(t, pool, 10)
		orderID := fmt.Sprintf("IT-RACE-%d-%s", round, sku)
		if _, err := repo.ReserveForOrder(ctx, orderID, testWarehouse, []domain.ReserveLine{{SkuID: sku, Quantity: 2}}); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CommitOrderReservation(ctx, orderID); err != nil {
			t.Fatal(err)
		}
		var handErr, relErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, handErr = repo.HandoverOrderShipment(ctx, orderID) }()
		go func() {
			defer wg.Done()
			_, relErr = repo.ReleaseCommittedReservation(ctx, orderID, domain.SystemActor)
		}()
		wg.Wait()
		if (handErr == nil) == (relErr == nil) {
			t.Fatalf("round %d: handover err=%v release err=%v, want exactly one winner", round, handErr, relErr)
		}
		got := stockOf(t, repo, sku)
		if handErr == nil && (got.OnHand != 8 || got.Committed != 0) || relErr == nil && (got.OnHand != 10 || got.Committed != 0) {
			t.Fatalf("round %d: stock = %+v after handover err=%v release err=%v", round, got, handErr, relErr)
		}
	}
}
