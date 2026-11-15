package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }

func at(hhmmss string) time.Time {
	ts, err := time.Parse("2006-01-02 15:04:05", "2026-03-01 "+hhmmss)
	if err != nil {
		panic(err)
	}
	return ts
}

const (
	wh   = "WH-HCM-01"
	balo = "SKU-BALO-BLACK-20L"
)

func newRepoAt(start string) (*InventoryRepository, *fakeClock) {
	clock := &fakeClock{t: at(start)}
	repo := NewInventoryRepository(nil)
	repo.SetClock(clock.Now)
	return repo, clock
}

func reserve(t *testing.T, repo *InventoryRepository, orderID string, qty int64) {
	t.Helper()
	if _, err := repo.ReserveForOrder(context.Background(), orderID, wh, []domain.ReserveLine{{SkuID: balo, Quantity: qty}}); err != nil {
		t.Fatalf("reserve %s: %v", orderID, err)
	}
}

func available(t *testing.T, repo *InventoryRepository) int64 {
	t.Helper()
	st, ok, err := repo.GetStock(context.Background(), wh, balo)
	if err != nil || !ok {
		t.Fatalf("GetStock: ok %v err %v", ok, err)
	}
	return st.Available()
}

func TestUnpaidReservationIsReleasedOnlyAfterTenMinutes(t *testing.T) {
	repo, clock := newRepoAt("10:00:00")
	ctx := context.Background()
	reserve(t, repo, "O-1", 2)

	clock.Set(at("10:09:59"))
	if n, _ := repo.ExpireDueReservations(ctx); n != 0 || available(t, repo) != 8 {
		t.Fatalf("at 10:09:59 released %d, available %d; want 0 and 8", n, available(t, repo))
	}
	clock.Set(at("10:10:00")) // expires_at is inclusive
	if n, err := repo.ExpireDueReservations(ctx); err != nil || n != 1 {
		t.Fatalf("at 10:10:00 released %d, err %v; want 1", n, err)
	}
	if got := available(t, repo); got != 10 {
		t.Fatalf("available = %d, want 10 after expiry", got)
	}
	if n, _ := repo.ExpireDueReservations(ctx); n != 0 {
		t.Fatalf("a second sweep released %d, want 0", n)
	}
	rec, _ := repo.GetReservation(ctx, "O-1")
	if rec.Status != "RELEASED" {
		t.Fatalf("status = %s, want RELEASED", rec.Status)
	}
	// The ledger says who and why.
	moves, _, _ := repo.ListMovements(ctx, MovementFilter{WarehouseID: wh, SkuID: balo, Page: 1, Limit: 1})
	if moves[0].Type != "RELEASE" || moves[0].ActorID != ExpirySweepActor || moves[0].Reason != "reservation expired" {
		t.Fatalf("latest movement = %+v", moves[0])
	}
}

// SC05: reserved 10:00, payment unknown at 10:09 -> held until 10:30; the worker must not release it at 10:10.
func TestExceptionHoldSurvivesTheSweepAndEndsAtThirtyMinutes_SC05(t *testing.T) {
	repo, clock := newRepoAt("10:00:00")
	ctx := context.Background()
	reserve(t, repo, "O-5", 2)

	clock.Set(at("10:09:00"))
	rec, err := repo.PlaceExceptionHold(ctx, "O-5")
	if err != nil || rec.Status != "EXCEPTION_HOLD_30M" || !rec.ExpiresAt.Equal(at("10:30:00")) {
		t.Fatalf("hold = %+v, err %v; want EXCEPTION_HOLD_30M until 10:30", rec, err)
	}
	if again, err := repo.PlaceExceptionHold(ctx, "O-5"); err != nil || !again.ExpiresAt.Equal(at("10:30:00")) {
		t.Fatalf("repeating the hold: %+v, %v; want unchanged", again, err)
	}

	clock.Set(at("10:11:00"))
	if n, _ := repo.ExpireDueReservations(ctx); n != 0 || available(t, repo) != 8 {
		t.Fatalf("10:11 released %d, available %d; the held stock must stay reserved", n, available(t, repo))
	}
	clock.Set(at("10:31:00")) // past 30 minutes: still not an automatic release, the controlled release is explicit
	if n, _ := repo.ExpireDueReservations(ctx); n != 0 || available(t, repo) != 8 {
		t.Fatalf("10:31 released %d, available %d; want held until the controlled release", n, available(t, repo))
	}
	if _, err := repo.ReleaseOrderReservation(ctx, "O-5"); err != nil {
		t.Fatalf("controlled release: %v", err)
	}
	if got := available(t, repo); got != 10 {
		t.Fatalf("available = %d, want 10 after the controlled release", got)
	}
}

func TestExceptionHoldIsRefusedWhenItIsTooLateOrNotApplicable(t *testing.T) {
	repo, clock := newRepoAt("10:00:00")
	ctx := context.Background()
	reserve(t, repo, "O-LATE", 1)
	reserve(t, repo, "O-PAID", 1)
	if _, err := repo.CommitOrderReservation(ctx, "O-PAID"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PlaceExceptionHold(ctx, "O-NONE"); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("unknown order: %v, want ErrReservationNotFound", err)
	}
	if _, err := repo.PlaceExceptionHold(ctx, "O-PAID"); !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("committed order: %v, want ErrInvalidReservationState", err)
	}
	clock.Set(at("10:10:00"))
	if _, err := repo.PlaceExceptionHold(ctx, "O-LATE"); !errors.Is(err, ErrReservationExpired) {
		t.Fatalf("hold at 10:10:00: %v, want ErrReservationExpired", err)
	}
}

func TestExpiryWorkerSweepsInTheBackgroundAndStopsWithItsContext(t *testing.T) {
	repo, clock := newRepoAt("10:00:00")
	reserve(t, repo, "O-W", 3)
	clock.Set(at("10:20:00"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { repo.RunExpiryWorker(ctx, 5*time.Millisecond); close(done) }()
	deadline := time.After(2 * time.Second)
	for available(t, repo) != 10 {
		select {
		case <-deadline:
			t.Fatal("the worker did not release the expired reservation")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the worker did not stop after cancel")
	}
}
