package attempt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// runRefundContract is shared by the memory store and the Postgres integration test: a cancellation of a paid
// order owes the captured amount once, the payout is recorded once, and the ceiling is never exceeded.
func runRefundContract(t *testing.T, store Store, uniq func(string) string, eventTypes func(orderID string) []string) {
	t.Helper()
	ctx := context.Background()
	paid := func() Attempt {
		order := uniq("O")
		a := Attempt{AttemptID: "PAY-" + order, OrderID: order, UserID: uniq("u"), IdempotencyKey: "pay-" + order, AmountVND: 660000}
		if _, _, err := store.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, ""); err != nil {
			t.Fatal(err)
		}
		return a
	}
	pendingOf := func(a Attempt) []Obligation {
		list, err := store.PendingRefunds(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var mine []Obligation
		for _, o := range list {
			if o.AttemptID == a.AttemptID {
				mine = append(mine, o)
			}
		}
		return mine
	}

	t.Run("closing for a cancellation owes the captured amount once with its reason", func(t *testing.T) {
		a := paid()
		for i := 0; i < 3; i++ {
			if _, _, err := store.CloseFor(ctx, a.AttemptID, ReasonOrderCancelled); err != nil {
				t.Fatal(err)
			}
		}
		obs, _ := store.Obligations(ctx, a.OrderID)
		if len(obs) != 1 || obs[0].Reason != ReasonOrderCancelled || obs[0].AmountVND != 660000 || obs[0].Status != ObligationPending {
			t.Fatalf("obligations = %+v", obs)
		}
		if sum, _ := store.Summary(ctx, a.OrderID); sum.RefundPending != 660000 || sum.RefundedVND != 0 || sum.CapturedVND != 660000 {
			t.Fatalf("summary = %+v", sum)
		}
		if len(pendingOf(a)) != 1 {
			t.Fatalf("pending refunds = %+v", pendingOf(a))
		}
	})

	t.Run("a payout moves pending to refunded once and writes one event", func(t *testing.T) {
		a := paid()
		_, _, _ = store.CloseFor(ctx, a.AttemptID, ReasonOrderCancelled)
		for i := 0; i < 3; i++ {
			o, sum, done, err := store.CompleteRefund(ctx, a.AttemptID, "SIM-RF-1")
			if err != nil || done != (i == 0) || o.Status != ObligationDone || o.ProviderRef != "SIM-RF-1" || o.RefundedAt == nil ||
				sum.RefundedVND != 660000 || sum.RefundPending != 0 || sum.CapturedVND != 660000 {
				t.Fatalf("complete #%d = %+v %+v done=%v %v", i, o, sum, done, err)
			}
		}
		if len(pendingOf(a)) != 0 {
			t.Fatal("a paid out refund is still pending")
		}
		if got := fmt.Sprint(eventTypes(a.OrderID)); got != "[payment.captured.v1 payment.late_refund_required.v1 payment.refunded.v1]" {
			t.Fatalf("events = %s", got)
		}
		// Closing again after the payout owes nothing more.
		if _, _, err := store.CloseFor(ctx, a.AttemptID, ReasonOrderCancelled); err != nil {
			t.Fatal(err)
		}
		if sum, _ := store.Summary(ctx, a.OrderID); sum.RefundPending != 0 || sum.RefundedVND != 660000 {
			t.Fatalf("summary after a second close = %+v", sum)
		}
	})

	t.Run("concurrent completions pay out once", func(t *testing.T) {
		a := paid()
		_, _, _ = store.CloseFor(ctx, a.AttemptID, ReasonOrderCancelled)
		var wg sync.WaitGroup
		var firsts atomic.Int64
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, done, err := store.CompleteRefund(ctx, a.AttemptID, "SIM-RF-2"); err != nil {
					t.Error(err)
				} else if done {
					firsts.Add(1)
				}
			}()
		}
		wg.Wait()
		sum, _ := store.Summary(ctx, a.OrderID)
		if firsts.Load() != 1 || sum.RefundedVND != 660000 || sum.RefundPending != 0 || countType(eventTypes(a.OrderID), "payment.refunded.v1") != 1 {
			t.Fatalf("firsts=%d summary=%+v events=%v", firsts.Load(), sum, eventTypes(a.OrderID))
		}
	})

	t.Run("an attempt without an obligation has nothing to pay out", func(t *testing.T) {
		a := paid() // captured, never closed: nothing is owed
		if _, _, _, err := store.CompleteRefund(ctx, a.AttemptID, "SIM-RF-3"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("complete without obligation = %v, want ErrNotFound", err)
		}
		if _, _, _, err := store.CompleteRefund(ctx, "PAY-missing", "SIM-RF-3"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("complete missing = %v, want ErrNotFound", err)
		}
	})

	t.Run("a late capture is refunded through the same payout", func(t *testing.T) {
		order := uniq("O")
		a := Attempt{AttemptID: "PAY-" + order, OrderID: order, UserID: uniq("u"), IdempotencyKey: "pay-" + order, AmountVND: 660000}
		_, _, _ = store.Create(ctx, a)
		_, _, _ = store.Close(ctx, a.AttemptID) // the order was closed first
		if _, _, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, ""); err != nil {
			t.Fatal(err)
		}
		if p := pendingOf(a); len(p) != 1 || p[0].Reason != ReasonLateCapture || p[0].AmountVND != 660000 {
			t.Fatalf("pending = %+v", p)
		}
		if _, sum, done, err := store.CompleteRefund(ctx, a.AttemptID, "SIM-RF-4"); err != nil || !done || sum.RefundedVND != 660000 {
			t.Fatalf("complete = %+v %v %v", sum, done, err)
		}
	})
}

func countType(types []string, want string) int {
	n := 0
	for _, ty := range types {
		if ty == want {
			n++
		}
	}
	return n
}

func TestMemoryStoreRefunds(t *testing.T) {
	var n atomic.Int64
	uniq := func(p string) string { return fmt.Sprintf("%s-%d", p, n.Add(1)) }
	store := NewMemoryStore()
	runRefundContract(t, store, uniq, func(orderID string) []string {
		var out []string
		for _, e := range store.Outbox() {
			if e.AggregateID == orderID {
				out = append(out, e.EventType)
			}
		}
		return out
	})
}
