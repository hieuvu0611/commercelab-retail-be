package orders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func sampleOrder(id, user, quote string) Order {
	return Order{
		OrderID: id, UserID: user, QuoteID: quote, AddressID: "addr-1", WarehouseID: "WH-HCM-01",
		SubtotalVND: 700000, DiscountVND: 70000, ShippingFeeVND: 30000, TotalPayableVND: 660000,
		Items: []Item{
			{SkuID: "SKU-A", SkuName: "Backpack", Quantity: 2, UnitPriceVND: 300000, LineDiscountVND: 60000, AllocatedUnitPaidVND: 270000},
			{SkuID: "SKU-B", SkuName: "Bottle", Quantity: 1, UnitPriceVND: 100000, LineDiscountVND: 10000, AllocatedUnitPaidVND: 90000},
		},
	}
}

// runStoreContract is the behaviour every Store must have; the Postgres integration test runs it too.
func runStoreContract(t *testing.T, newStore func(t *testing.T) Store, uniq func(string) string) {
	ctx := context.Background()

	t.Run("creates a pending unpaid order with its items", func(t *testing.T) {
		s := newStore(t)
		user, key := uniq("u"), uniq("k")
		got, replayed, err := s.Create(ctx, key, HashIntent(user, "Q-1"), sampleOrder(uniq("O"), user, "Q-1"))
		if err != nil || replayed {
			t.Fatalf("Create = %v replayed=%v", err, replayed)
		}
		if got.Status != StatusPending || got.PaymentStatus != PaymentUnpaid || got.Version != 1 || len(got.Items) != 2 || got.TotalPayableVND != 660000 {
			t.Fatalf("created order = %+v", got)
		}
		read, err := s.Get(ctx, got.OrderID)
		if err != nil || read.UserID != user || read.Items[0].AllocatedUnitPaidVND != 270000 {
			t.Fatalf("Get = %+v, %v", read, err)
		}
	})

	t.Run("same key and same content replays the same order (SC04)", func(t *testing.T) {
		s := newStore(t)
		user, key := uniq("u"), uniq("k")
		first, _, _ := s.Create(ctx, key, HashIntent(user, "Q-1"), sampleOrder(uniq("O"), user, "Q-1"))
		second, replayed, err := s.Create(ctx, key, HashIntent(user, "Q-1"), sampleOrder(uniq("O"), user, "Q-1"))
		if err != nil || !replayed || second.OrderID != first.OrderID {
			t.Fatalf("replay = %+v replayed=%v err=%v, want order %s", second, replayed, err, first.OrderID)
		}
		found, ok, err := s.Lookup(ctx, user, key, HashIntent(user, "Q-1"))
		if err != nil || !ok || found.OrderID != first.OrderID {
			t.Fatalf("Lookup = %+v ok=%v err=%v", found, ok, err)
		}
	})

	t.Run("same key with different content conflicts (SC04)", func(t *testing.T) {
		s := newStore(t)
		user, key := uniq("u"), uniq("k")
		if _, _, err := s.Create(ctx, key, HashIntent(user, "Q-1"), sampleOrder(uniq("O"), user, "Q-1")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Create(ctx, key, HashIntent(user, "Q-2"), sampleOrder(uniq("O"), user, "Q-2")); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("Create with other quote = %v, want conflict", err)
		}
		if _, _, err := s.Lookup(ctx, user, key, HashIntent(user, "Q-2")); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("Lookup with other quote = %v, want conflict", err)
		}
	})

	t.Run("an unknown key is not found and another customer's key is independent", func(t *testing.T) {
		s := newStore(t)
		a, b, key := uniq("ua"), uniq("ub"), uniq("k")
		if _, ok, err := s.Lookup(ctx, a, key, HashIntent(a, "Q-1")); ok || err != nil {
			t.Fatalf("Lookup of new key = %v, %v", ok, err)
		}
		first, _, _ := s.Create(ctx, key, HashIntent(a, "Q-1"), sampleOrder(uniq("O"), a, "Q-1"))
		other, replayed, err := s.Create(ctx, key, HashIntent(b, "Q-1"), sampleOrder(uniq("O"), b, "Q-1"))
		if err != nil || replayed || other.OrderID == first.OrderID {
			t.Fatalf("other customer's create = %+v replayed=%v err=%v", other, replayed, err)
		}
	})

	t.Run("20 concurrent duplicates produce one order", func(t *testing.T) {
		s := newStore(t)
		user, key := uniq("u"), uniq("k")
		var wg sync.WaitGroup
		ids := make(chan string, 20)
		errs := make(chan error, 20)
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				o, _, err := s.Create(ctx, key, HashIntent(user, "Q-1"), sampleOrder(fmt.Sprintf("%s-%d", uniq("O"), i), user, "Q-1"))
				if err != nil {
					errs <- err
					return
				}
				ids <- o.OrderID
			}(i)
		}
		wg.Wait()
		close(ids)
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent Create: %v", err)
		}
		distinct := map[string]bool{}
		for id := range ids {
			distinct[id] = true
		}
		if len(distinct) != 1 {
			t.Fatalf("got %d distinct orders, want 1", len(distinct))
		}
	})

	t.Run("transition is compare-and-set", func(t *testing.T) {
		s := newStore(t)
		user := uniq("u")
		o, _, _ := s.Create(ctx, uniq("k"), HashIntent(user, "Q-1"), sampleOrder(uniq("O"), user, "Q-1"))
		moved, err := s.Transition(ctx, o.OrderID, StatusPending, "RESERVED", "PROCESSING", "")
		if err != nil || moved.Status != "RESERVED" || moved.PaymentStatus != "PROCESSING" || moved.Version != 2 {
			t.Fatalf("Transition = %+v, %v", moved, err)
		}
		if _, err := s.Transition(ctx, o.OrderID, StatusPending, "FAILED_CLOSED", "FAILED", "X"); !errors.Is(err, ErrStaleState) {
			t.Fatalf("stale Transition = %v, want ErrStaleState", err)
		}
		if _, err := s.Transition(ctx, "O-missing", StatusPending, "RESERVED", "PROCESSING", ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Transition of missing order = %v, want ErrNotFound", err)
		}
		if _, err := s.Get(ctx, "O-missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get of missing order = %v, want ErrNotFound", err)
		}
	})
}

func runCaptureContract(t *testing.T, newStore func(t *testing.T) Store, uniq func(string) string) {
	ctx := context.Background()
	s := newStore(t)
	user := uniq("u")
	o, _, _ := s.Create(ctx, uniq("k"), HashIntent(user, "Q-1"), sampleOrder(uniq("O"), user, "Q-1"))
	if o.CapturedVND != 0 || o.RefundPendingVND != 0 {
		t.Fatalf("new order = %+v", o)
	}
	for i := 0; i < 2; i++ { // setting the same amounts again changes nothing
		if err := s.RecordCapture(ctx, o.OrderID, 660000, 660000); err != nil {
			t.Fatalf("RecordCapture: %v", err)
		}
	}
	got, _ := s.Get(ctx, o.OrderID)
	if got.CapturedVND != 660000 || got.RefundPendingVND != 660000 {
		t.Fatalf("order = %+v", got)
	}
	if err := s.RecordCapture(ctx, "O-missing", 1, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing order = %v", err)
	}
}

func TestMemoryStoreCapture(t *testing.T) {
	var n int
	runCaptureContract(t, func(*testing.T) Store { return NewMemoryStore() }, func(p string) string { n++; return fmt.Sprintf("%s-%d", p, n) })
}

func TestMemoryStoreContract(t *testing.T) {
	var n int
	var mu sync.Mutex
	runStoreContract(t, func(*testing.T) Store { return NewMemoryStore() }, func(p string) string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("%s-%d", p, n)
	})
}

func TestHashIntentDependsOnUserAndQuote(t *testing.T) {
	if HashIntent("u1", "Q-1") == HashIntent("u1", "Q-2") || HashIntent("u1", "Q-1") == HashIntent("u2", "Q-1") {
		t.Fatal("hash must change with the quote and the user")
	}
}
