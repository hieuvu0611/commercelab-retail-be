package checkout

import (
	"context"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

func TestCancellationClosesTheAttemptForACancellationReason(t *testing.T) {
	f := newFixture()
	o := f.paid(t)
	if _, err := f.cancel(t, o); err != nil {
		t.Fatal(err)
	}
	if f.payments.cancelCloses != 1 {
		t.Fatalf("cancellation closes = %d, want 1 (so the refund carries ORDER_CANCELLED)", f.payments.cancelCloses)
	}
	g := newFixture()
	u := placed(t, g)
	if _, err := g.cancel(t, u); err != nil || g.payments.cancelCloses != 1 {
		t.Fatalf("unpaid cancel: %v closes=%d", err, g.payments.cancelCloses)
	}
}

func TestAPayoutMovesTheOrderFromRefundPendingToRefunded(t *testing.T) {
	f := newFixture()
	cancelled, _ := f.cancel(t, f.paid(t))
	for i := 0; i < 3; i++ { // the payout event is delivered more than once
		if err := f.svc.ApplyRefund(context.Background(), cancelled.OrderID, RefundReport{RefundedVND: 660000, PendingVND: 0, CapturedVND: 660000}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := f.orders.Get(context.Background(), cancelled.OrderID)
	if got.PaymentStatus != "REFUNDED" || got.RefundedVND != 660000 || got.RefundPendingVND != 0 || got.CapturedVND != 660000 || got.Status != "CANCELLED" {
		t.Fatalf("order = %+v", got)
	}
}

func TestAPartialPayoutIsPartiallyRefunded(t *testing.T) {
	f := newFixture()
	cancelled, _ := f.cancel(t, f.paid(t))
	if err := f.svc.ApplyRefund(context.Background(), cancelled.OrderID, RefundReport{RefundedVND: 200000, PendingVND: 460000, CapturedVND: 660000}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.orders.Get(context.Background(), cancelled.OrderID)
	if got.PaymentStatus != "PARTIALLY_REFUNDED" || got.RefundedVND != 200000 || got.RefundPendingVND != 460000 {
		t.Fatalf("order = %+v", got)
	}
}

func TestARefundAboveTheCapturedAmountIsNeverStoredAndUnknownOrdersAreSkipped(t *testing.T) {
	f := newFixture()
	cancelled, _ := f.cancel(t, f.paid(t))
	for _, r := range []RefundReport{{RefundedVND: 700000, CapturedVND: 660000}, {RefundedVND: 400000, PendingVND: 400000, CapturedVND: 660000}, {RefundedVND: -1, CapturedVND: 660000}} {
		if err := f.svc.ApplyRefund(context.Background(), cancelled.OrderID, r); err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	got, _ := f.orders.Get(context.Background(), cancelled.OrderID)
	if got.RefundedVND != 0 || got.PaymentStatus != "REFUND_PENDING" || got.RefundPendingVND != 660000 {
		t.Fatalf("order = %+v, want the invalid reports ignored", got)
	}
	if err := f.svc.ApplyRefund(context.Background(), "O-nope", RefundReport{RefundedVND: 1, CapturedVND: 1}); err != nil {
		t.Fatalf("unknown order = %v", err)
	}
}

func TestPaymentReplaysNeverTurnARefundedOrderBackIntoRefundPending(t *testing.T) {
	// A closed order whose late money was refunded: the late-capture report arrives again.
	f := newFixture()
	o := placed(t, f)
	closed := f.apply(t, o, "CAPTURED_LATE", 660000)
	if closed.PaymentStatus != PayRefundPending {
		t.Fatalf("closed = %+v", closed)
	}
	if err := f.svc.ApplyRefund(context.Background(), o.OrderID, RefundReport{RefundedVND: 660000, CapturedVND: 660000}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"CAPTURED_LATE", "CAPTURED"} {
		got := f.apply(t, closed, status, 660000)
		if got.PaymentStatus != "REFUNDED" || got.RefundedVND != 660000 || got.RefundPendingVND != 0 {
			t.Fatalf("%s replay = %+v", status, got)
		}
	}
	// The same holds for a cancelled order.
	g := newFixture()
	cancelled, _ := g.cancel(t, g.paid(t))
	_ = g.svc.ApplyRefund(context.Background(), cancelled.OrderID, RefundReport{RefundedVND: 660000, CapturedVND: 660000})
	if got := g.apply(t, cancelled, "CAPTURED_LATE", 660000); got.PaymentStatus != "REFUNDED" || got.Status != string(saga.StateCancelled) {
		t.Fatalf("cancelled replay = %+v", got)
	}
}
