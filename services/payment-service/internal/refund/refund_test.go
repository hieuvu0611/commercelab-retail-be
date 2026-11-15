package refund

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
)

func cancelledOrder(t *testing.T, store *attempt.MemoryStore, order string, captured int64) attempt.Attempt {
	t.Helper()
	ctx := context.Background()
	a := attempt.Attempt{AttemptID: "PAY-" + order, OrderID: order, UserID: "user-lan", IdempotencyKey: "pay-" + order, AmountVND: captured}
	if _, _, err := store.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(ctx, a.AttemptID, attempt.OutcomeCaptured, captured, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CloseFor(ctx, a.AttemptID, attempt.ReasonOrderCancelled); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSimulatorPaysEachRefundOnceWhateverTheNumberOfCalls(t *testing.T) {
	sim := NewSimulator()
	var refs []string
	for i := 0; i < 5; i++ {
		ref, err := sim.Refund(context.Background(), "PAY-O-1", 660000)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	if sim.Payouts() != 1 || sim.PaidVND() != 660000 || refs[0] == "" || refs[0] != refs[4] {
		t.Fatalf("payouts=%d paid=%d refs=%v, want one payout and one reference", sim.Payouts(), sim.PaidVND(), refs)
	}
	if _, err := sim.Refund(context.Background(), "PAY-O-1", 1); err == nil {
		t.Fatal("the same refund id was accepted for another amount")
	}
	if _, err := sim.Refund(context.Background(), "PAY-O-2", 0); err == nil {
		t.Fatal("a refund of zero was accepted")
	}
}

func TestWorkerPaysACancelledOrdersFullAmountOnce_SC06A(t *testing.T) {
	store, sim := attempt.NewMemoryStore(), NewSimulator()
	a := cancelledOrder(t, store, "O-1", 660000)
	w := &Worker{Store: store, Provider: sim}
	for i := 0; i < 3; i++ {
		if _, err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	sum, _ := store.Summary(context.Background(), a.OrderID)
	obs, _ := store.Obligations(context.Background(), a.OrderID)
	if sim.Payouts() != 1 || sim.PaidVND() != 660000 || sum.RefundedVND != 660000 || sum.RefundPending != 0 || obs[0].Status != attempt.ObligationDone {
		t.Fatalf("payouts=%d paid=%d summary=%+v obligations=%+v", sim.Payouts(), sim.PaidVND(), sum, obs)
	}
}

func TestConcurrentWorkersNeverPayTwice(t *testing.T) {
	store, sim := attempt.NewMemoryStore(), NewSimulator()
	for i := 0; i < 5; i++ {
		cancelledOrder(t, store, fmt.Sprintf("O-%d", i), 660000)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := (&Worker{Store: store, Provider: sim}).RunOnce(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if sim.Payouts() != 5 || sim.PaidVND() != 5*660000 {
		t.Fatalf("payouts=%d paid=%d, want five refunds of 660,000", sim.Payouts(), sim.PaidVND())
	}
}

type flaky struct {
	inner *Simulator
	fail  atomic.Bool
}

func (f *flaky) Refund(ctx context.Context, id string, amountVND int64) (string, error) {
	if f.fail.Load() {
		return "", errors.New("provider timeout")
	}
	return f.inner.Refund(ctx, id, amountVND)
}

func TestAProviderOutageKeepsTheRefundPendingAndALaterRunPaysIt(t *testing.T) {
	store, sim := attempt.NewMemoryStore(), NewSimulator()
	a := cancelledOrder(t, store, "O-1", 660000)
	p := &flaky{inner: sim}
	p.fail.Store(true)
	w := &Worker{Store: store, Provider: p}
	if done, err := w.RunOnce(context.Background()); err != nil || done != 0 {
		t.Fatalf("outage run = %d %v", done, err)
	}
	if sum, _ := store.Summary(context.Background(), a.OrderID); sum.RefundPending != 660000 || sum.RefundedVND != 0 {
		t.Fatalf("summary = %+v, want it still pending", sum)
	}
	p.fail.Store(false)
	if done, err := w.RunOnce(context.Background()); err != nil || done != 1 {
		t.Fatalf("recovery run = %d %v", done, err)
	}
	if sim.Payouts() != 1 {
		t.Fatalf("payouts = %d", sim.Payouts())
	}
}

// inflated lists an obligation above what is pending, as a corrupted row would.
type inflated struct {
	attempt.Store
	amount int64
}

func (s inflated) PendingRefunds(ctx context.Context, limit int) ([]attempt.Obligation, error) {
	list, err := s.Store.PendingRefunds(ctx, limit)
	for i := range list {
		list[i].AmountVND = s.amount
	}
	return list, err
}

func TestARefundAboveTheCapturedAmountIsNeverPaid_I03(t *testing.T) {
	store, sim := attempt.NewMemoryStore(), NewSimulator()
	a := cancelledOrder(t, store, "O-1", 660000)
	w := &Worker{Store: inflated{Store: store, amount: 700000}, Provider: sim}
	if done, err := w.RunOnce(context.Background()); err != nil || done != 0 {
		t.Fatalf("run = %d %v", done, err)
	}
	if sim.Payouts() != 0 {
		t.Fatalf("the provider paid %d VND above the captured amount", sim.PaidVND())
	}
	if sum, _ := store.Summary(context.Background(), a.OrderID); sum.RefundedVND != 0 || sum.RefundPending != 660000 {
		t.Fatalf("summary = %+v", sum)
	}
}
