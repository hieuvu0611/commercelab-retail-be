package checkout

import (
	"context"
	"errors"
	"sync"
	"testing"

	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

func (f *fixture) cancel(t *testing.T, o orders.Order) (orders.Order, error) {
	t.Helper()
	return f.svc.Cancel(context.Background(), o.UserID, o.OrderID)
}

func (f *fixture) paid(t *testing.T) orders.Order {
	t.Helper()
	o := f.apply(t, placed(t, f), "CAPTURED", 660000)
	if o.Status != "CONFIRMED" {
		t.Fatalf("paid order = %+v", o)
	}
	return o
}

func (f *fixture) eventTypes() []string {
	var out []string
	for _, e := range f.orders.Outbox() {
		out = append(out, e.EventType)
	}
	return out
}

func countEvents(f *fixture, typ string) int {
	n := 0
	for _, e := range f.orders.Outbox() {
		if e.EventType == typ {
			n++
		}
	}
	return n
}

func TestCancelUnpaidOrderReleasesStockAndVoucherOnce_Case1(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	for i := 0; i < 3; i++ { // the customer clicks Cancel more than once
		got, err := f.cancel(t, o)
		if err != nil || got.Status != string(saga.StateCancelled) || got.PaymentStatus != saga.PayUnpaid || got.RefundPendingVND != 0 {
			t.Fatalf("cancel #%d = %+v %v", i, got, err)
		}
	}
	if f.stock.releases != 1 || f.stock.committedReleases != 0 {
		t.Fatalf("stock releases=%d committedReleases=%d, want 1 and 0", f.stock.releases, f.stock.committedReleases)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 0 {
		t.Fatalf("voucher reserved=%d consumed=%d, want the use released", reserved, consumed)
	}
	if len(f.payments.closes) != 1 || f.payments.closes[0] != "PAY-"+o.OrderID {
		t.Fatalf("attempt closes = %v, want one close so a late payment is refunded", f.payments.closes)
	}
	if f.ful.calls != 0 {
		t.Fatalf("an unpaid order has no shipment, fulfillment was asked %d times", f.ful.calls)
	}
	if countEvents(f, events.EventOrderCancelled) != 1 {
		t.Fatalf("events = %v, want exactly one order.cancelled", f.eventTypes())
	}
}

func TestCancelPaidOrderNotPickedReleasesCommittedStockAndKeepsTheVoucher_Case2(t *testing.T) {
	f := newFixture()
	o := f.paid(t)
	for i := 0; i < 3; i++ {
		got, err := f.cancel(t, o)
		if err != nil || got.Status != string(saga.StateCancelled) || got.PaymentStatus != PayRefundPending ||
			got.CapturedVND != 660000 || got.RefundPendingVND != 660000 {
			t.Fatalf("cancel #%d = %+v %v", i, got, err)
		}
	}
	if f.stock.committedReleases != 1 || f.stock.releases != 0 {
		t.Fatalf("committedReleases=%d releases=%d, want 1 and 0", f.stock.committedReleases, f.stock.releases)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 1 {
		t.Fatalf("voucher reserved=%d consumed=%d, a paid cancellation must not restore the use", reserved, consumed)
	}
	if f.ful.calls != 1 || f.ful.last.OrderID != o.OrderID || f.ful.last.WarehouseID != "WH-HCM-01" || f.ful.last.UserID != "user-lan" || len(f.ful.last.Lines) != 2 {
		t.Fatalf("fulfillment stop = %d calls, %+v", f.ful.calls, f.ful.last)
	}
	if len(f.payments.closes) != 1 {
		t.Fatalf("attempt closes = %v, want one (it owes the refund)", f.payments.closes)
	}
	if countEvents(f, events.EventOrderCancelled) != 1 {
		t.Fatalf("events = %v", f.eventTypes())
	}
}

func TestCancelAfterHandoverIsRefused_Case4(t *testing.T) {
	f := newFixture()
	o := f.paid(t)
	f.ful.result = StopTooLate
	got, err := f.cancel(t, o)
	if !errors.Is(err, ErrHandedOver) || got.Status != "CONFIRMED" {
		t.Fatalf("cancel = %+v %v, want ErrHandedOver and no change", got, err)
	}
	if f.stock.committedReleases != 0 || len(f.payments.closes) != 0 || countEvents(f, events.EventOrderCancelled) != 0 {
		t.Fatalf("a refused cancel had effects: released=%d closes=%v", f.stock.committedReleases, f.payments.closes)
	}
}

func TestCancelWhileWarehouseIsPackingWaitsForTheWarehouse_Case3(t *testing.T) {
	f := newFixture()
	o := f.paid(t)
	f.ful.result = StopRequested
	for i := 0; i < 3; i++ { // the customer asks again while waiting
		got, err := f.cancel(t, o)
		if err != nil || got.Status != string(saga.StateCancelRequested) || got.PaymentStatus != saga.PayCaptured || got.RefundPendingVND != 0 {
			t.Fatalf("request #%d = %+v %v, want CANCEL_REQUESTED and still paid", i, got, err)
		}
	}
	if f.stock.committedReleases != 0 || len(f.payments.closes) != 0 {
		t.Fatalf("a cancellation that is only requested released stock (%d) or closed the payment (%v)", f.stock.committedReleases, f.payments.closes)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 1 {
		t.Fatalf("voucher reserved=%d consumed=%d", reserved, consumed)
	}
	if countEvents(f, events.EventOrderCancelRequested) != 1 || countEvents(f, events.EventOrderCancelled) != 0 {
		t.Fatalf("events = %v, want one order.cancel_requested and no order.cancelled", f.eventTypes())
	}
}

func (f *fixture) requested(t *testing.T) orders.Order {
	t.Helper()
	o := f.paid(t)
	f.ful.result = StopRequested
	got, err := f.cancel(t, o)
	if err != nil || got.Status != string(saga.StateCancelRequested) {
		t.Fatalf("request = %+v %v", got, err)
	}
	return got
}

func TestWarehouseStopsBeforeHandoverTheOrderIsCancelledAndRefunded_SC06A(t *testing.T) {
	f := newFixture()
	o := f.requested(t)
	f.ful.result = StopStopped
	for i := 0; i < 3; i++ { // the stopped event is delivered more than once, and the sweeper looks too
		if err := f.svc.SettleCancel(context.Background(), o.OrderID); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := f.orders.Get(context.Background(), o.OrderID)
	if got.Status != string(saga.StateCancelled) || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 660000 || got.CapturedVND != 660000 {
		t.Fatalf("order = %+v, want CANCELLED with the whole 660,000 owed", got)
	}
	if f.stock.committedReleases != 1 || len(f.payments.closes) != 1 || countEvents(f, events.EventOrderCancelled) != 1 {
		t.Fatalf("committedReleases=%d closes=%v events=%v, want each once", f.stock.committedReleases, f.payments.closes, f.eventTypes())
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 1 {
		t.Fatalf("voucher reserved=%d consumed=%d, a paid cancellation keeps the use", reserved, consumed)
	}
	// Asking again after the fact is the stored answer, not another cancellation.
	if again, err := f.cancel(t, got); err != nil || again.Status != string(saga.StateCancelled) || f.stock.committedReleases != 1 {
		t.Fatalf("repeat = %+v %v releases=%d", again, err, f.stock.committedReleases)
	}
}

func TestHandoverWinsTheCancellationIsRejectedAndNothingIsRefunded_SC06B(t *testing.T) {
	f := newFixture()
	o := f.requested(t)
	f.ful.result = StopTooLate
	for i := 0; i < 3; i++ {
		if err := f.svc.SettleCancel(context.Background(), o.OrderID); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := f.orders.Get(context.Background(), o.OrderID)
	if got.Status != string(saga.StateInFulfillment) || got.PaymentStatus != saga.PayCaptured || got.RefundPendingVND != 0 {
		t.Fatalf("order = %+v, want back in fulfillment, still paid, nothing owed", got)
	}
	if f.stock.committedReleases != 0 || len(f.payments.closes) != 0 || countEvents(f, events.EventOrderCancelled) != 0 || countEvents(f, events.EventOrderCancelRejected) != 1 {
		t.Fatalf("released=%d closes=%v events=%v", f.stock.committedReleases, f.payments.closes, f.eventTypes())
	}
	// The customer asking again is refused as a handed over order (case 4).
	if _, err := f.cancel(t, got); !errors.Is(err, ErrHandedOver) {
		t.Fatalf("cancel after the rejection = %v, want ErrHandedOver", err)
	}
}

func TestCancelRequestIsFinishedByTheCustomerAskingAgainOnceTheStopIsConfirmed(t *testing.T) {
	f := newFixture()
	o := f.requested(t)
	f.ful.result = StopStopped
	got, err := f.cancel(t, o)
	if err != nil || got.Status != string(saga.StateCancelled) || got.RefundPendingVND != 660000 {
		t.Fatalf("cancel = %+v %v", got, err)
	}
}

func TestSettleOnlyActsOnAWaitingCancellation(t *testing.T) {
	f := newFixture()
	o := f.paid(t) // CONFIRMED: a stopped or handed over event for it changes nothing
	f.ful.result = StopStopped
	if err := f.svc.SettleCancel(context.Background(), o.OrderID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SettleCancel(context.Background(), "O-nope"); err != nil {
		t.Fatalf("unknown order = %v", err)
	}
	if got, _ := f.orders.Get(context.Background(), o.OrderID); got.Status != "CONFIRMED" || f.ful.calls != 0 || f.stock.committedReleases != 0 {
		t.Fatalf("order = %+v asked=%d released=%d", got, f.ful.calls, f.stock.committedReleases)
	}
}

func TestSettleKeepsWaitingWhileTheWarehouseHasNotAnswered(t *testing.T) {
	f := newFixture()
	o := f.requested(t)
	if err := f.svc.SettleCancel(context.Background(), o.OrderID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.orders.Get(context.Background(), o.OrderID); got.Status != string(saga.StateCancelRequested) {
		t.Fatalf("order = %+v", got)
	}
	f.ful.err = dependency.Wrap(errors.New("fulfillment down"))
	if err := f.svc.SettleCancel(context.Background(), o.OrderID); !dependency.IsUnavailable(err) {
		t.Fatalf("outage = %v, want a retryable error", err)
	}
}

func TestSweeperSettlesAWaitingCancellation(t *testing.T) {
	f := newFixture()
	o := f.requested(t)
	f.ful.result = StopStopped
	// The memory store stamps updates with the wall clock, so age the order by moving the sweeper's clock past it.
	svc := f.svc.WithClock(func() time.Time { return time.Now().UTC().Add(time.Hour) })
	if st := svc.Sweep(context.Background()); st.CancelsSettled != 1 {
		t.Fatalf("sweep = %+v, want one settled cancellation", st)
	}
	if got, _ := f.orders.Get(context.Background(), o.OrderID); got.Status != string(saga.StateCancelled) {
		t.Fatalf("order = %+v", got)
	}
}

func TestConcurrentRequestsAndSettlesCancelOnce_SC06A(t *testing.T) {
	f := newFixture()
	o := f.requested(t)
	f.ful.result = StopStopped
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				if _, err := f.cancel(t, o); err != nil {
					t.Errorf("cancel: %v", err)
				}
			} else if err := f.svc.SettleCancel(context.Background(), o.OrderID); err != nil {
				t.Errorf("settle: %v", err)
			}
		}()
	}
	wg.Wait()
	if f.stock.committedReleases != 1 || len(f.payments.closes) != 1 || countEvents(f, events.EventOrderCancelled) != 1 {
		t.Fatalf("committedReleases=%d closes=%d cancelled=%d, want 1 of each", f.stock.committedReleases, len(f.payments.closes), countEvents(f, events.EventOrderCancelled))
	}
}

func TestPaymentReportsWhileACancellationWaitsChangeNothing(t *testing.T) {
	f := newFixture()
	o := f.requested(t)
	before := len(f.eventTypes())
	for _, status := range []string{"CAPTURED", "CAPTURED_LATE", "UNKNOWN", "FAILED", "EXPIRED_30M"} {
		got := f.apply(t, o, status, 660000)
		if got.Status != string(saga.StateCancelRequested) || got.PaymentStatus != saga.PayCaptured {
			t.Fatalf("%s = %+v", status, got)
		}
	}
	if len(f.eventTypes()) != before || f.stock.commits != 1 {
		t.Fatalf("events=%v commits=%d", f.eventTypes(), f.stock.commits)
	}
}

func TestCancelWithUnknownPaymentWaitsForThePayment_Case5(t *testing.T) {
	f := newFixture()
	o := f.apply(t, placed(t, f), "UNKNOWN", 0)
	_, err := f.cancel(t, o)
	if !errors.Is(err, ErrPaymentUnresolved) {
		t.Fatalf("cancel = %v, want ErrPaymentUnresolved", err)
	}
	if f.stock.releases != 0 || len(f.payments.closes) != 0 {
		t.Fatalf("an unresolved payment must keep everything held: releases=%d closes=%v", f.stock.releases, f.payments.closes)
	}
	if reserved, _ := f.voucherBuckets(t); reserved != 1 {
		t.Fatalf("voucher reserved = %d, want it still held", reserved)
	}
}

func TestCancelFindsALostUnknownCallback_Case5(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.payments.states = map[string]PaymentState{o.OrderID: {AttemptID: "PAY-" + o.OrderID, Status: "UNKNOWN"}}
	if _, err := f.cancel(t, o); !errors.Is(err, ErrPaymentUnresolved) {
		t.Fatalf("cancel = %v, want ErrPaymentUnresolved", err)
	}
	if got, _ := f.orders.Get(context.Background(), o.OrderID); got.Status != string(saga.StateUnknownHold) || f.stock.releases != 0 {
		t.Fatalf("order = %+v releases=%d", got, f.stock.releases)
	}
}

func TestCancelFindsALostCaptureAndTreatsTheOrderAsPaid(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.payments.states = map[string]PaymentState{o.OrderID: {AttemptID: "PAY-" + o.OrderID, Status: "CAPTURED", CapturedVND: 660000}}
	got, err := f.cancel(t, o)
	if err != nil || got.Status != string(saga.StateCancelled) || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 660000 {
		t.Fatalf("cancel = %+v %v", got, err)
	}
	if f.stock.commits != 1 || f.stock.committedReleases != 1 {
		t.Fatalf("commits=%d committedReleases=%d, want the capture applied then the committed stock released", f.stock.commits, f.stock.committedReleases)
	}
}

func TestCancelUnpaidRecordsMoneyCapturedWhileClosing(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.payments.afterClose = &PaymentState{AttemptID: "PAY-" + o.OrderID, Status: "CAPTURED_LATE", CapturedVND: 660000}
	got, err := f.cancel(t, o)
	if err != nil || got.Status != string(saga.StateCancelled) || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 660000 {
		t.Fatalf("cancel = %+v %v", got, err)
	}
}

func TestCancelIsOwnerOnlyAndStateChecked(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	if _, err := f.svc.Cancel(context.Background(), "user-other", o.OrderID); !errors.Is(err, orders.ErrNotFound) {
		t.Fatalf("another customer = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Cancel(context.Background(), "user-lan", "O-nope"); !errors.Is(err, orders.ErrNotFound) {
		t.Fatalf("unknown order = %v", err)
	}
	for _, status := range []string{"PENDING", "FAILED_CLOSED", "DELIVERED"} {
		g := newFixture()
		x := placed(t, g)
		if status == "FAILED_CLOSED" {
			x = g.apply(t, x, "FAILED", 0)
		} else {
			if _, err := g.orders.Transition(context.Background(), x.OrderID, "RESERVED", status, "PROCESSING", ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := g.cancel(t, x); !errors.Is(err, ErrNotCancellable) {
			t.Fatalf("cancel of %s = %v, want ErrNotCancellable", status, err)
		}
	}
	if f.stock.releases != 0 {
		t.Fatalf("a refused cancel released stock")
	}
}

func TestConcurrentCancelsReleaseOnceAndOweOneRefund(t *testing.T) {
	f := newFixture()
	o := f.paid(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.cancel(t, o); err != nil {
				t.Errorf("cancel: %v", err)
			}
		}()
	}
	wg.Wait()
	if f.stock.committedReleases != 1 || len(f.payments.closes) != 1 || f.ful.calls != 1 || countEvents(f, events.EventOrderCancelled) != 1 {
		t.Fatalf("committedReleases=%d closes=%d stops=%d cancelledEvents=%d, want 1 of each",
			f.stock.committedReleases, len(f.payments.closes), f.ful.calls, countEvents(f, events.EventOrderCancelled))
	}
}

func TestCancelRetriesAfterAnInventoryFailureWithoutDoubleEffects(t *testing.T) {
	f := newFixture()
	o := f.paid(t)
	f.stock.committedErr = errors.New("inventory down")
	if _, err := f.cancel(t, o); err == nil {
		t.Fatal("cancel succeeded although inventory failed")
	}
	if got, _ := f.orders.Get(context.Background(), o.OrderID); got.Status != "CONFIRMED" {
		t.Fatalf("order = %+v, want it unchanged so the cancel can be retried", got)
	}
	f.stock.committedErr = nil
	got, err := f.cancel(t, o)
	if err != nil || got.Status != string(saga.StateCancelled) || got.RefundPendingVND != 660000 {
		t.Fatalf("retry = %+v %v", got, err)
	}
	if len(f.payments.closes) != 1 {
		t.Fatalf("closes = %v, the attempt must only be closed after the stock was given back", f.payments.closes)
	}
}

func TestPaymentReportsAfterACancelNeverRefundTwice(t *testing.T) {
	// Paid cancellation: replays of the capture (also the late status the close produced) change nothing.
	f := newFixture()
	o := f.paid(t)
	cancelled, _ := f.cancel(t, o)
	before := len(f.eventTypes())
	for _, status := range []string{"CAPTURED", "CAPTURED_LATE", "UNKNOWN", "PROCESSING"} {
		got := f.apply(t, cancelled, status, 660000)
		if got.Status != "CANCELLED" || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 660000 {
			t.Fatalf("%s after cancel = %+v", status, got)
		}
	}
	if len(f.eventTypes()) != before {
		t.Fatalf("replays wrote events: %v", f.eventTypes())
	}
	if f.stock.commits != 1 {
		t.Fatalf("commits = %d, a cancelled order must never commit again", f.stock.commits)
	}
}

func TestMoneyArrivingAfterAnUnpaidCancelIsRefundedOnce_SC05(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	cancelled, _ := f.cancel(t, o)
	for i := 0; i < 2; i++ {
		got := f.apply(t, cancelled, "CAPTURED_LATE", 660000)
		if got.Status != "CANCELLED" || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 660000 {
			t.Fatalf("late capture #%d = %+v", i, got)
		}
	}
	if f.stock.commits != 0 {
		t.Fatalf("commits = %d, the order is cancelled", f.stock.commits)
	}
}

func TestCancelConfirmedCODOrderClearsWithoutRefundPending(t *testing.T) {
	f := newFixture()
	o, _, err := f.svc.Place(context.Background(), Principal{UserID: "user-lan", Bearer: "tok"}, "cod-cancel-key", "Q-1", PaymentMethodCOD)
	if err != nil || o.Status != "CONFIRMED" || o.PaymentStatus != PayCODPending {
		t.Fatalf("place COD = %+v %v", o, err)
	}
	cancelled, err := f.cancel(t, o)
	if err != nil {
		t.Fatalf("cancel COD = %v", err)
	}
	if cancelled.Status != string(saga.StateCancelled) || cancelled.PaymentStatus != PayCODCancelled ||
		cancelled.RefundPendingVND != 0 || cancelled.CapturedVND != 0 {
		t.Fatalf("cancelled COD = %+v, want COD_CANCELLED with 0 refundPending", cancelled)
	}
	if f.stock.committedReleases != 1 {
		t.Fatalf("expected committed stock release for COD cancel, got %d", f.stock.committedReleases)
	}
}

func TestCODAttemptCreationFailureRollsBackConfirmation(t *testing.T) {
	f := newFixture()
	f.payments.failure = errors.New("payment db down")
	o, _, err := f.svc.Place(context.Background(), Principal{UserID: "user-lan", Bearer: "tok"}, "cod-fail-key", "Q-1", PaymentMethodCOD)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if o.Status != string(saga.StateFailedClosed) || o.PaymentStatus != saga.PayFailed {
		t.Fatalf("expected FAILED_CLOSED on CreateCODAttempt failure, got status=%s payment=%s", o.Status, o.PaymentStatus)
	}
	if f.stock.committedReleases != 1 {
		t.Fatalf("expected committed stock rollback on CreateCODAttempt failure, got %d", f.stock.committedReleases)
	}
}
