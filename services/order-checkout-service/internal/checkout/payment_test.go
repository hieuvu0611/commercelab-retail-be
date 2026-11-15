package checkout

import (
	"context"
	"errors"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

func placed(t *testing.T, f *fixture) orders.Order {
	t.Helper()
	o, _, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil || o.Status != string(saga.StateReserved) {
		t.Fatalf("place = %+v %v", o, err)
	}
	return o
}

func report(o orders.Order, status string, captured int64) PaymentReport {
	return PaymentReport{AttemptID: "PAY-" + o.OrderID, Status: status, CapturedVND: captured}
}

func (f *fixture) apply(t *testing.T, o orders.Order, status string, captured int64) orders.Order {
	t.Helper()
	got, err := f.svc.ApplyPayment(context.Background(), o.OrderID, report(o, status, captured))
	if err != nil {
		t.Fatalf("ApplyPayment(%s) = %v", status, err)
	}
	return got
}

func (f *fixture) voucherBuckets(t *testing.T) (int64, int64) {
	t.Helper()
	c, err := f.vouchers.Get(context.Background(), "WELCOME10")
	if err != nil {
		t.Fatal(err)
	}
	return c.ReservedQuota, c.ConsumedQuota
}

func TestCapturedAmountEqualToExpectedConfirmsOnce_SC01(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	for i := 0; i < 3; i++ { // the callback is delivered more than once
		got := f.apply(t, o, "CAPTURED", 660000)
		if got.Status != "CONFIRMED" || got.PaymentStatus != saga.PayCaptured || got.CapturedVND != 660000 || got.RefundPendingVND != 0 {
			t.Fatalf("order = %+v", got)
		}
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 1 {
		t.Fatalf("voucher reserved=%d consumed=%d, want 0 and 1", reserved, consumed)
	}
	if f.stock.releases != 0 {
		t.Fatalf("a confirmed order released stock %d times", f.stock.releases)
	}
	// One order version bump for CONFIRMED, not one per delivery.
	if got, _ := f.orders.Get(context.Background(), o.OrderID); got.Version != 3 { // PENDING(1) -> RESERVED(2) -> CONFIRMED(3)
		t.Fatalf("version = %d, want 3", got.Version)
	}
	if len(f.carts.removed) == 0 || f.carts.removed[0][0] != "user-lan" || len(f.carts.removed[0]) != 3 {
		t.Fatalf("cart cleanup = %v, want the two ordered SKUs of user-lan", f.carts.removed)
	}
}

func TestAmountMismatchNeverConfirms_P04(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	got := f.apply(t, o, "CAPTURED", 650000)
	if got.Status != "UNKNOWN_HOLD" || got.PaymentStatus != saga.PayUnknown30m || f.stock.commits != 0 || f.stock.holds != 1 {
		t.Fatalf("order = %+v commits=%d holds=%d", got, f.stock.commits, f.stock.holds)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 1 || consumed != 0 {
		t.Fatalf("voucher reserved=%d consumed=%d, want it still reserved", reserved, consumed)
	}
	if len(f.carts.removed) != 0 {
		t.Fatalf("cart emptied for an unconfirmed order: %v", f.carts.removed)
	}
}

func TestDefinitePaymentFailureClosesAndReleasesEverything(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	got := f.apply(t, o, "FAILED", 0)
	if got.Status != "FAILED_CLOSED" || got.PaymentStatus != saga.PayFailed || got.FailureCode != FailurePaymentFailed {
		t.Fatalf("order = %+v", got)
	}
	if f.stock.releases != 1 {
		t.Fatalf("stock releases = %d", f.stock.releases)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 0 {
		t.Fatalf("voucher reserved=%d consumed=%d", reserved, consumed)
	}
	if again := f.apply(t, o, "FAILED", 0); again.Version != got.Version {
		t.Fatalf("a repeated failure changed the order: %+v", again)
	}
}

func TestUnknownPaymentHoldsStockAndRepeatedUnknownChangesNothing_SC05(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	got := f.apply(t, o, "UNKNOWN", 0)
	if got.Status != "UNKNOWN_HOLD" || got.PaymentStatus != saga.PayUnknown30m || f.stock.holds != 1 {
		t.Fatalf("order = %+v holds=%d", got, f.stock.holds)
	}
	again := f.apply(t, o, "UNKNOWN", 0)
	if again.Version != got.Version {
		t.Fatalf("a repeated UNKNOWN changed the order: %+v", again)
	}
	// Money that arrives inside the window confirms the order from the hold.
	done := f.apply(t, o, "CAPTURED", 660000)
	if done.Status != "CONFIRMED" || f.stock.commits != 1 {
		t.Fatalf("order = %+v commits=%d", done, f.stock.commits)
	}
}

func TestLateCaptureAfterTheOrderClosedOwesARefundAndDoesNotRestoreIt_SC05(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.apply(t, o, "UNKNOWN", 0)
	got := f.apply(t, o, "CAPTURED_LATE", 660000)
	if got.Status != "FAILED_CLOSED" || got.PaymentStatus != PayRefundPending || got.FailureCode != FailureUnknownExpired ||
		got.CapturedVND != 660000 || got.RefundPendingVND != 660000 {
		t.Fatalf("order = %+v", got)
	}
	if f.stock.commits != 0 || f.stock.releases != 1 {
		t.Fatalf("commits=%d releases=%d: a late capture must not restore the order", f.stock.commits, f.stock.releases)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 0 {
		t.Fatalf("voucher reserved=%d consumed=%d", reserved, consumed)
	}
	if len(f.payments.closes) == 0 || f.payments.closes[0] != "PAY-"+o.OrderID {
		t.Fatalf("payment attempt was not closed: %v", f.payments.closes)
	}
	if again := f.apply(t, o, "CAPTURED_LATE", 660000); again.Version != got.Version {
		t.Fatalf("a repeated late capture changed the order: %+v", again)
	}
}

func TestCaptureArrivingForAnAlreadyClosedOrderIsRefunded(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.apply(t, o, "FAILED", 0) // closed first (for example by the sweeper)
	// A capture that still arrives afterwards (a race the provider can cause) is money to give back.
	got := f.apply(t, o, "CAPTURED", 660000)
	if got.Status != "FAILED_CLOSED" || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 660000 || f.stock.commits != 0 {
		t.Fatalf("order = %+v commits=%d", got, f.stock.commits)
	}
}

func TestCommitThatCannotFindTheReservationClosesAndRefunds(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.stock.commitErr = ErrReservationLost
	got := f.apply(t, o, "CAPTURED", 660000)
	if got.Status != "FAILED_CLOSED" || got.FailureCode != FailureStockLost || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 660000 {
		t.Fatalf("order = %+v", got)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 0 {
		t.Fatalf("voucher reserved=%d consumed=%d", reserved, consumed)
	}
	if len(f.payments.closes) == 0 {
		t.Fatal("the captured money was not handed to the refund path")
	}
}

func TestADependencyOutageLeavesTheOrderUnchangedForARetry(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.stock.commitErr = dependency.Wrap(errors.New("inventory down"))
	if _, err := f.svc.ApplyPayment(context.Background(), o.OrderID, report(o, "CAPTURED", 660000)); !dependency.IsUnavailable(err) {
		t.Fatalf("err = %v, want a dependency error", err)
	}
	if got, _ := f.orders.Get(context.Background(), o.OrderID); got.Status != "RESERVED" {
		t.Fatalf("status = %s, want RESERVED", got.Status)
	}
	f.stock.commitErr = nil // the retry succeeds
	if got := f.apply(t, o, "CAPTURED", 660000); got.Status != "CONFIRMED" {
		t.Fatalf("after retry = %+v", got)
	}
}

func TestPaymentReportValidation(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	if _, err := f.svc.ApplyPayment(context.Background(), o.OrderID, PaymentReport{AttemptID: "PAY-other", Status: "CAPTURED", CapturedVND: 660000}); !errors.Is(err, ErrAttemptMismatch) {
		t.Fatalf("other attempt = %v", err)
	}
	if _, err := f.svc.ApplyPayment(context.Background(), "O-missing", PaymentReport{AttemptID: "PAY-O-missing", Status: "FAILED"}); !errors.Is(err, orders.ErrNotFound) {
		t.Fatalf("missing order = %v", err)
	}
	if _, err := f.svc.ApplyPayment(context.Background(), o.OrderID, report(o, "BOGUS", 0)); !errors.Is(err, ErrUnknownPaymentStatus) {
		t.Fatalf("bogus status = %v", err)
	}
	if got := f.apply(t, o, "PROCESSING", 0); got.Status != "RESERVED" {
		t.Fatalf("PROCESSING changed the order: %+v", got)
	}
}

func TestFailureAfterConfirmationIsRejected(t *testing.T) {
	f := newFixture()
	o := placed(t, f)
	f.apply(t, o, "CAPTURED", 660000)
	if _, err := f.svc.ApplyPayment(context.Background(), o.OrderID, report(o, "FAILED", 0)); !errors.Is(err, saga.ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
}

func TestPaymentAttemptIsClosedWhenPlacementFailsAfterTheAttemptCall(t *testing.T) {
	f := newFixture()
	f.payments.failure = dependency.Wrap(errors.New("the response was lost"))
	o, _, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil || o.Status != "FAILED_CLOSED" {
		t.Fatalf("order = %+v %v", o, err)
	}
	if len(f.payments.closes) != 1 || f.payments.closes[0] != "PAY-"+o.OrderID {
		t.Fatalf("closes = %v: an attempt whose response was lost must be closed", f.payments.closes)
	}
}
