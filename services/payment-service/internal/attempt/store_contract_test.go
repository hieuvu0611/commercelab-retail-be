package attempt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// runStoreContract is shared with the Postgres integration test so both stores behave the same.
// advance moves the store's clock forward (the Postgres store reads the real clock, so it is a no-op there and
// the late-capture case uses an attempt whose window is already over).
func runStoreContract(t *testing.T, store Store, uniq func(string) string) {
	t.Helper()
	ctx := context.Background()
	newAttempt := func() Attempt {
		order := uniq("O")
		return Attempt{AttemptID: "PAY-" + order, OrderID: order, UserID: uniq("u"), IdempotencyKey: "pay-" + order, AmountVND: 660000}
	}

	t.Run("create stores a processing attempt and a summary", func(t *testing.T) {
		a := newAttempt()
		got, replayed, err := store.Create(ctx, a)
		if err != nil || replayed || got.Status != StatusProcessing || got.AmountVND != 660000 || got.UserID != a.UserID {
			t.Fatalf("Create = %+v replayed=%v err=%v", got, replayed, err)
		}
		sum, err := store.Summary(ctx, a.OrderID)
		if err != nil || sum.ExpectedVND != 660000 || sum.CapturedVND != 0 {
			t.Fatalf("Summary = %+v, %v", sum, err)
		}
		read, err := store.Get(ctx, a.AttemptID)
		if err != nil || read.OrderID != a.OrderID {
			t.Fatalf("Get = %+v, %v", read, err)
		}
		if _, err := store.Get(ctx, "PAY-missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get missing = %v", err)
		}
	})

	t.Run("same key and content replays, other content conflicts", func(t *testing.T) {
		a := newAttempt()
		first, _, _ := store.Create(ctx, a)
		again, replayed, err := store.Create(ctx, a)
		if err != nil || !replayed || again.AttemptID != first.AttemptID {
			t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
		}
		other := a
		other.AmountVND = 1
		if _, _, err := store.Create(ctx, other); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("other amount = %v", err)
		}
		stranger := a
		stranger.UserID = "someone-else"
		if _, _, err := store.Create(ctx, stranger); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("other user = %v", err)
		}
	})

	t.Run("an order has one attempt: active or finished", func(t *testing.T) {
		a := newAttempt()
		_, _, _ = store.Create(ctx, a)
		second := a
		second.IdempotencyKey = "other-key-" + a.OrderID
		if _, _, err := store.Create(ctx, second); !errors.Is(err, ErrActivePayment) {
			t.Fatalf("second active attempt = %v, want ErrActivePayment", err)
		}
		if _, _, err := store.Apply(ctx, a.AttemptID, OutcomeFailed, 0, ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Create(ctx, second); !errors.Is(err, ErrAttemptExists) {
			t.Fatalf("attempt after a failure = %v, want ErrAttemptExists", err)
		}
	})

	t.Run("concurrent creates for one order make one attempt", func(t *testing.T) {
		a := newAttempt()
		var wg sync.WaitGroup
		var created, replayed, rejected atomic.Int32
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				c := a
				if i%2 == 1 { // half use another key: they must be refused, not create a second attempt
					c.IdempotencyKey = fmt.Sprintf("%s-x%d", a.IdempotencyKey, i)
				}
				_, rep, err := store.Create(ctx, c)
				switch {
				case err == nil && rep:
					replayed.Add(1)
				case err == nil:
					created.Add(1)
				case errors.Is(err, ErrActivePayment):
					rejected.Add(1)
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}(i)
		}
		wg.Wait()
		if created.Load() != 1 || created.Load()+replayed.Load()+rejected.Load() != 10 {
			t.Fatalf("created=%d replayed=%d rejected=%d", created.Load(), replayed.Load(), rejected.Load())
		}
	})

	t.Run("a captured outcome updates the totals once", func(t *testing.T) {
		a := newAttempt()
		_, _, _ = store.Create(ctx, a)
		got, ch, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, "")
		if err != nil || got.Status != StatusCaptured || ch.CapturedVND != 660000 {
			t.Fatalf("Apply = %+v %+v %v", got, ch, err)
		}
		_, ch, err = store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, "")
		if err != nil || !ch.Replay {
			t.Fatalf("replay = %+v %v", ch, err)
		}
		if _, _, err := store.Apply(ctx, a.AttemptID, OutcomeFailed, 0, ""); !errors.Is(err, ErrFinalOutcome) {
			t.Fatalf("flip = %v", err)
		}
		sum, _ := store.Summary(ctx, a.OrderID)
		if sum.CapturedVND != 660000 || sum.RefundPending != 0 {
			t.Fatalf("summary = %+v", sum)
		}
		if _, _, err := store.Apply(ctx, "PAY-missing", OutcomeFailed, 0, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing attempt = %v", err)
		}
	})

	t.Run("a delivered event id is applied once", func(t *testing.T) {
		a := newAttempt()
		_, _, _ = store.Create(ctx, a)
		ev := uniq("evt")
		if _, ch, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, ev); err != nil || ch.Replay {
			t.Fatalf("first delivery = %+v %v", ch, err)
		}
		if _, ch, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, ev); err != nil || !ch.Replay {
			t.Fatalf("second delivery = %+v %v", ch, err)
		}
		if sum, _ := store.Summary(ctx, a.OrderID); sum.CapturedVND != 660000 {
			t.Fatalf("captured = %d, want 660000", sum.CapturedVND)
		}
	})

	t.Run("closing a processing attempt turns a later capture into a refund", func(t *testing.T) {
		a := newAttempt()
		_, _, _ = store.Create(ctx, a)
		closed, ch, err := store.Close(ctx, a.AttemptID)
		if err != nil || closed.Status != StatusExpired30m || ch.Replay {
			t.Fatalf("Close = %+v %+v %v", closed, ch, err)
		}
		if _, ch, err := store.Close(ctx, a.AttemptID); err != nil || !ch.Replay {
			t.Fatalf("second Close = %+v %v", ch, err)
		}
		late, ch, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, "")
		if err != nil || late.Status != StatusCapturedLate || ch.LateRefundVND != 660000 {
			t.Fatalf("capture after close = %+v %+v %v", late, ch, err)
		}
		sum, _ := store.Summary(ctx, a.OrderID)
		if sum.CapturedVND != 660000 || sum.RefundPending != 660000 {
			t.Fatalf("summary = %+v", sum)
		}
		if _, _, err := store.Close(ctx, "PAY-missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Close missing = %v", err)
		}
	})

	t.Run("closing a captured attempt records one refund obligation", func(t *testing.T) {
		a := newAttempt()
		_, _, _ = store.Create(ctx, a)
		_, _, _ = store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, "")
		for i := 0; i < 2; i++ {
			got, _, err := store.Close(ctx, a.AttemptID)
			if err != nil || got.Status != StatusCapturedLate {
				t.Fatalf("Close #%d = %+v %v", i, got, err)
			}
		}
		sum, _ := store.Summary(ctx, a.OrderID)
		obs, _ := store.Obligations(ctx, a.OrderID)
		if sum.CapturedVND != 660000 || sum.RefundPending != 660000 || len(obs) != 1 || obs[0].AmountVND != 660000 {
			t.Fatalf("summary = %+v obligations = %+v", sum, obs)
		}
	})

	t.Run("concurrent identical captures count the money once", func(t *testing.T) {
		a := newAttempt()
		_, _, _ = store.Create(ctx, a)
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, ""); err != nil {
					t.Errorf("Apply: %v", err)
				}
			}()
		}
		wg.Wait()
		if sum, _ := store.Summary(ctx, a.OrderID); sum.CapturedVND != 660000 {
			t.Fatalf("captured = %d, want 660000", sum.CapturedVND)
		}
	})
}

// runLateCaptureContract needs a store whose window can be passed: setDeadline makes the attempt's UNKNOWN window
// end in the past.
func runLateCaptureContract(t *testing.T, store Store, uniq func(string) string, expireWindow func(attemptID string)) {
	t.Helper()
	ctx := context.Background()
	order := uniq("O")
	a := Attempt{AttemptID: "PAY-" + order, OrderID: order, UserID: uniq("u"), IdempotencyKey: "pay-" + order, AmountVND: 660000}
	_, _, _ = store.Create(ctx, a)
	if got, _, err := store.Apply(ctx, a.AttemptID, OutcomeUnknown, 0, ""); err != nil || got.Status != StatusUnknown || got.UnknownDeadline == nil {
		t.Fatalf("unknown = %+v %v", got, err)
	}
	expireWindow(a.AttemptID)
	got, ch, err := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, "")
	if err != nil || got.Status != StatusCapturedLate || ch.LateRefundVND != 660000 {
		t.Fatalf("late capture = %+v %+v %v", got, ch, err)
	}
	if _, ch, _ := store.Apply(ctx, a.AttemptID, OutcomeCaptured, 660000, ""); !ch.Replay {
		t.Fatal("a repeated late capture must be a replay")
	}
	sum, _ := store.Summary(ctx, order)
	if sum.CapturedVND != 660000 || sum.RefundPending != 660000 {
		t.Fatalf("summary = %+v, want captured 660000 and refund pending 660000", sum)
	}
	obs, err := store.Obligations(ctx, order)
	if err != nil || len(obs) != 1 || obs[0].AmountVND != 660000 || obs[0].AttemptID != a.AttemptID || obs[0].Status != "PENDING" {
		t.Fatalf("obligations = %+v %v", obs, err)
	}
}

func TestMemoryStoreContract(t *testing.T) {
	var n atomic.Int64
	uniq := func(p string) string { return fmt.Sprintf("%s-%d", p, n.Add(1)) }
	runStoreContract(t, NewMemoryStore(), uniq)
}

func TestMemoryStoreLateCapture_SC05(t *testing.T) {
	var n atomic.Int64
	uniq := func(p string) string { return fmt.Sprintf("%s-%d", p, n.Add(1)) }
	clock := time.Date(2026, 11, 15, 10, 1, 0, 0, time.UTC)
	store := NewMemoryStore().WithClock(func() time.Time { return clock })
	runLateCaptureContract(t, store, uniq, func(string) { clock = time.Date(2026, 11, 15, 10, 40, 0, 0, time.UTC) })
}
