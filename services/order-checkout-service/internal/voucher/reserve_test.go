package voucher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type reserveStore interface {
	AdminStore
	Reserver
}

func refusalReason(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

// runReserveContract is shared with the Postgres integration test so both stores behave the same.
func runReserveContract(t *testing.T, store reserveStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	admin := NewAdmin(store)
	now := time.Now().UTC()
	mk := func(name string, quota int64, limit int, mutate func(*Fields)) string {
		code := fmt.Sprintf("%s-%s-%d", prefix, name, now.UnixNano()%1_000_000)
		f := Fields{Name: name, DiscountPct: 10, TotalQuota: quota, PerCustomerLimit: limit, Status: StatusActive}
		if mutate != nil {
			mutate(&f)
		}
		if _, err := admin.Create(ctx, code, "staff-marketing", f); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return code
	}
	buckets := func(code string) (int64, int64) {
		c, err := store.Get(ctx, code)
		if err != nil {
			t.Fatal(err)
		}
		return c.ReservedQuota, c.ConsumedQuota
	}

	t.Run("consume turns the reserved use into a consumed one exactly once", func(t *testing.T) {
		code := mk("CONSUME", 5, 3, nil)
		if err := store.Reserve(ctx, code, "u1", "O-C1", now); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if err := store.Consume(ctx, code, "O-C1"); err != nil {
				t.Fatalf("consume #%d: %v", i, err)
			}
		}
		if reserved, consumed := buckets(code); reserved != 0 || consumed != 1 {
			t.Fatalf("reserved=%d consumed=%d, want 0 and 1", reserved, consumed)
		}
		// A consumed use is final: releasing it changes nothing and it still counts against the customer limit.
		if err := store.Release(ctx, code, "O-C1"); err != nil {
			t.Fatal(err)
		}
		if reserved, consumed := buckets(code); reserved != 0 || consumed != 1 {
			t.Fatalf("after release reserved=%d consumed=%d", reserved, consumed)
		}
		if n, _ := store.CustomerUses(ctx, code, "u1"); n != 1 {
			t.Fatalf("uses = %d, want 1", n)
		}
		// Nothing reserved for the order: consuming is a no-op, not an error and not a free use.
		if err := store.Consume(ctx, code, "O-never"); err != nil {
			t.Fatal(err)
		}
		if _, consumed := buckets(code); consumed != 1 {
			t.Fatalf("consumed = %d, want 1", consumed)
		}
	})

	t.Run("reserve takes one use and repeating the same order takes no more", func(t *testing.T) {
		code := mk("A", 5, 3, nil)
		for i := 0; i < 3; i++ {
			if err := store.Reserve(ctx, code, "u1", "O-A1", now); err != nil {
				t.Fatalf("reserve #%d: %v", i, err)
			}
		}
		if reserved, _ := buckets(code); reserved != 1 {
			t.Fatalf("reserved = %d, want 1", reserved)
		}
		if n, _ := store.CustomerUses(ctx, code, "u1"); n != 1 {
			t.Fatalf("customer uses = %d, want 1", n)
		}
	})

	t.Run("the last use goes to one customer (SC10)", func(t *testing.T) {
		code := mk("LAST", 1, 1, nil)
		if err := store.Reserve(ctx, code, "u1", "O-L1", now); err != nil {
			t.Fatal(err)
		}
		if got := refusalReason(store.Reserve(ctx, code, "u2", "O-L2", now)); got != ReasonExhausted {
			t.Fatalf("second customer = %q, want %s", got, ReasonExhausted)
		}
		if reserved, _ := buckets(code); reserved != 1 {
			t.Fatalf("reserved = %d, want 1", reserved)
		}
	})

	t.Run("the per-customer limit counts held uses", func(t *testing.T) {
		code := mk("LIM", 5, 1, nil)
		if err := store.Reserve(ctx, code, "u1", "O-M1", now); err != nil {
			t.Fatal(err)
		}
		if got := refusalReason(store.Reserve(ctx, code, "u1", "O-M2", now)); got != ReasonLimitReached {
			t.Fatalf("same customer again = %q, want %s", got, ReasonLimitReached)
		}
	})

	t.Run("release returns the use and is safe to repeat", func(t *testing.T) {
		code := mk("REL", 1, 1, nil)
		_ = store.Reserve(ctx, code, "u1", "O-R1", now)
		for i := 0; i < 2; i++ {
			if err := store.Release(ctx, code, "O-R1"); err != nil {
				t.Fatalf("release #%d: %v", i, err)
			}
		}
		if reserved, _ := buckets(code); reserved != 0 {
			t.Fatalf("reserved after release = %d, want 0", reserved)
		}
		if err := store.Release(ctx, code, "O-NEVER"); err != nil {
			t.Fatalf("release of an order that never reserved: %v", err)
		}
		if n, _ := store.CustomerUses(ctx, code, "u1"); n != 0 {
			t.Fatalf("a released use still counts: %d", n)
		}
		if err := store.Reserve(ctx, code, "u2", "O-R2", now); err != nil {
			t.Fatalf("the freed use cannot be reserved: %v", err)
		}
	})

	t.Run("unknown, paused and out-of-window vouchers are refused with a reason", func(t *testing.T) {
		if got := refusalReason(store.Reserve(ctx, "NOPE-"+prefix, "u1", "O-X", now)); got != ReasonNotFound {
			t.Fatalf("unknown = %q", got)
		}
		paused := mk("PAU", 5, 1, func(f *Fields) { f.Status = StatusPaused })
		if got := refusalReason(store.Reserve(ctx, paused, "u1", "O-P", now)); got != ReasonNotActive {
			t.Fatalf("paused = %q", got)
		}
		ended := now.Add(-time.Hour)
		started := now.Add(-2 * time.Hour)
		expired := mk("EXP", 5, 1, func(f *Fields) { f.StartsAt, f.EndsAt = &started, &ended })
		if got := refusalReason(store.Reserve(ctx, expired, "u1", "O-E", now)); got != ReasonExpired {
			t.Fatalf("expired = %q", got)
		}
		if reserved, _ := buckets(paused); reserved != 0 {
			t.Fatal("a refused reserve changed the quota")
		}
	})

	t.Run("concurrent customers never reserve more than the quota", func(t *testing.T) {
		code := mk("RACE", 3, 1, nil)
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				err := store.Reserve(ctx, code, fmt.Sprintf("u%d", i), fmt.Sprintf("O-RACE-%d", i), now)
				if err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				} else if refusalReason(err) != ReasonExhausted {
					t.Errorf("unexpected error: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if reserved, _ := buckets(code); wins != 3 || reserved != 3 {
			t.Fatalf("wins = %d, reserved = %d, want 3 and 3", wins, reserved)
		}
	})
}

func TestMemoryStoreReserveContract(t *testing.T) {
	runReserveContract(t, NewMemoryStore(), "MEM")
}
