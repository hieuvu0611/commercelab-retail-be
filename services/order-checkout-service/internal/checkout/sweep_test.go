package checkout

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

// clockFixture is a fixture whose clock the test moves, so the SC05 timeline needs no sleeping.
type clockFixture struct {
	*fixture
	at *time.Time
}

func at(h, m int) time.Time { return time.Date(2026, 11, 15, h, m, 0, 0, time.UTC) }

func newClockFixture(start time.Time) *clockFixture {
	f := newFixture()
	clock := start
	now := func() time.Time { return clock }
	f.svc.WithClock(now)
	f.orders.WithClock(now)
	f.payments.states = map[string]PaymentState{}
	return &clockFixture{fixture: f, at: &clock}
}

func (f *clockFixture) sweep(t *testing.T, h, m int) SweepStats {
	t.Helper()
	*f.at = at(h, m)
	return f.svc.Sweep(context.Background())
}

func (f *clockFixture) order(t *testing.T, id string) orders.Order {
	t.Helper()
	o, err := f.orders.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestSC05TimelineWithAFakeClock(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture) // 10:00 reserve (voucher, stock, payment attempt)

	*f.at = at(10, 1) // 10:01 the payment outcome is unknown: the customer is told not to pay again
	held := f.apply(t, o, "UNKNOWN", 0)
	if held.Status != "UNKNOWN_HOLD" || f.stock.holds != 1 {
		t.Fatalf("10:01 order = %+v holds=%d", held, f.stock.holds)
	}

	// 10:09 and 10:11: the 10-minute reservation is long due, but an unknown payment is never released early.
	f.payments.states[o.OrderID] = PaymentState{AttemptID: "PAY-" + o.OrderID, Status: "UNKNOWN"}
	for _, m := range [][2]int{{10, 9}, {10, 11}, {10, 30}} {
		f.sweep(t, m[0], m[1])
		if got := f.order(t, o.OrderID); got.Status != "UNKNOWN_HOLD" || f.stock.releases != 0 {
			t.Fatalf("%02d:%02d order = %s releases=%d: must stay held", m[0], m[1], got.Status, f.stock.releases)
		}
	}

	// 10:31 the 30-minute window ends: the order closes, stock and voucher go back, the attempt is closed.
	stats := f.sweep(t, 10, 31)
	closed := f.order(t, o.OrderID)
	if closed.Status != "FAILED_CLOSED" || closed.FailureCode != FailureUnknownExpired || stats.Expired != 1 {
		t.Fatalf("10:31 order = %+v stats=%+v", closed, stats)
	}
	if f.stock.releases != 1 || len(f.payments.closes) == 0 {
		t.Fatalf("10:31 releases=%d closes=%v", f.stock.releases, f.payments.closes)
	}
	if reserved, consumed := f.voucherBuckets(t); reserved != 0 || consumed != 0 {
		t.Fatalf("10:31 voucher reserved=%d consumed=%d", reserved, consumed)
	}

	// 10:40 the customer's money arrives late: 660,000 is owed back and the order is not restored.
	*f.at = at(10, 40)
	late := f.apply(t, o, "CAPTURED_LATE", 660000)
	if late.Status != "FAILED_CLOSED" || late.PaymentStatus != PayRefundPending || late.RefundPendingVND != 660000 || f.stock.commits != 0 {
		t.Fatalf("10:40 order = %+v commits=%d", late, f.stock.commits)
	}
	if again := f.sweep(t, 10, 45); again.Expired != 0 {
		t.Fatalf("a closed order must not be swept again: %+v", again)
	}
}

func TestAnUnpaidReservationIsReleasedAfterTenMinutes(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	f.payments.states[o.OrderID] = PaymentState{AttemptID: "PAY-" + o.OrderID, Status: "PROCESSING"}
	if stats := f.sweep(t, 10, 9); stats.Expired != 0 || f.order(t, o.OrderID).Status != "RESERVED" {
		t.Fatalf("10:09 = %+v: still inside the ten minutes", stats)
	}
	if stats := f.sweep(t, 10, 10); stats.Expired != 1 {
		t.Fatalf("10:10 = %+v", stats)
	}
	got := f.order(t, o.OrderID)
	if got.Status != "FAILED_CLOSED" || got.FailureCode != FailureReservationExpired || got.PaymentStatus != saga.PayUnpaid {
		t.Fatalf("order = %+v", got)
	}
	if f.stock.releases != 1 || len(f.payments.closes) == 0 {
		t.Fatalf("releases=%d closes=%v", f.stock.releases, f.payments.closes)
	}
	if reserved, _ := f.voucherBuckets(t); reserved != 0 {
		t.Fatalf("voucher still reserved: %d", reserved)
	}
}

func TestALostCallbackIsRepairedFromThePaymentServiceBeforeAnythingIsReleased(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	f.payments.states[o.OrderID] = PaymentState{AttemptID: "PAY-" + o.OrderID, Status: "CAPTURED", CapturedVND: 660000}
	stats := f.sweep(t, 10, 12) // the reservation is due, but the money was captured at 10:03
	got := f.order(t, o.OrderID)
	if got.Status != "CONFIRMED" || stats.Reconciled != 1 || stats.Expired != 0 || f.stock.releases != 0 || f.stock.commits != 1 {
		t.Fatalf("order = %+v stats=%+v releases=%d commits=%d", got, stats, f.stock.releases, f.stock.commits)
	}
}

func TestAnUnknownOutcomeThatWasNeverReportedMovesToTheHoldInsteadOfReleasing(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	f.payments.states[o.OrderID] = PaymentState{AttemptID: "PAY-" + o.OrderID, Status: "UNKNOWN"}
	f.sweep(t, 10, 12)
	if got := f.order(t, o.OrderID); got.Status != "UNKNOWN_HOLD" || f.stock.releases != 0 || f.stock.holds != 1 {
		t.Fatalf("order = %+v releases=%d holds=%d", got, f.stock.releases, f.stock.holds)
	}
}

func TestNothingIsReleasedWhileThePaymentServiceCannotBeAsked(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	f.payments.stateErr = dependency.Wrap(errors.New("payment down"))
	f.sweep(t, 10, 20)
	if got := f.order(t, o.OrderID); got.Status != "RESERVED" || f.stock.releases != 0 {
		t.Fatalf("order = %s releases=%d: a capture may have happened", got.Status, f.stock.releases)
	}
	f.payments.stateErr = nil
	f.sweep(t, 10, 21)
	if got := f.order(t, o.OrderID); got.Status != "FAILED_CLOSED" {
		t.Fatalf("after recovery = %s", got.Status)
	}
}

func TestMoneyCapturedWithTheWrongAmountIsRefundedWhenTheHoldEnds(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	f.apply(t, o, "CAPTURED", 650000) // 650,000 instead of 660,000: never confirmed, held for a person
	f.payments.states[o.OrderID] = PaymentState{AttemptID: "PAY-" + o.OrderID, Status: "CAPTURED", CapturedVND: 650000}
	f.sweep(t, 10, 45)
	got := f.order(t, o.OrderID)
	if got.Status != "FAILED_CLOSED" || got.PaymentStatus != PayRefundPending || got.RefundPendingVND != 650000 || f.stock.commits != 0 {
		t.Fatalf("order = %+v", got)
	}
}

func TestAnOrderInterruptedDuringPlacementIsClosedAndEverythingReturned(t *testing.T) {
	f := newClockFixture(at(10, 0))
	// A crash after the voucher was reserved: the order is still PENDING and nothing says what was taken.
	pending := orders.Order{OrderID: "O-crash", UserID: "user-lan", QuoteID: "Q-1", AddressID: "a", WarehouseID: "WH-HCM-01",
		VoucherCode: "WELCOME10", Status: orders.StatusPending, PaymentStatus: orders.PaymentUnpaid, TotalPayableVND: 660000,
		Items: []orders.Item{{SkuID: "SKU-A", Quantity: 1}}}
	if _, _, err := f.orders.Create(context.Background(), "k-crash", orders.HashIntent("user-lan", "Q-1"), pending); err != nil {
		t.Fatal(err)
	}
	if err := f.vouchers.Reserve(context.Background(), "WELCOME10", "user-lan", "O-crash", at(10, 0)); err != nil {
		t.Fatal(err)
	}
	if stats := f.sweep(t, 10, 1); stats.Interrupted != 0 {
		t.Fatalf("a fresh PENDING order is still being placed: %+v", stats)
	}
	stats := f.sweep(t, 10, 5)
	got := f.order(t, "O-crash")
	if got.Status != "FAILED_CLOSED" || got.FailureCode != FailurePlacementInterrupted || stats.Interrupted != 1 || f.stock.releases != 1 {
		t.Fatalf("order = %+v stats=%+v releases=%d", got, stats, f.stock.releases)
	}
	if reserved, _ := f.voucherBuckets(t); reserved != 0 {
		t.Fatalf("voucher still reserved: %d", reserved)
	}
}

func TestAVoucherLeftReservedByAClosedOrderIsGivenBack(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	f.apply(t, o, "FAILED", 0)
	// The release at close time failed (simulated by reserving again behind the order's back).
	_ = f.vouchers.Reserve(context.Background(), "WELCOME10", "user-lan", o.OrderID, at(10, 0))
	if reserved, _ := f.voucherBuckets(t); reserved != 1 {
		t.Fatalf("setup: reserved = %d", reserved)
	}
	if stats := f.sweep(t, 10, 5); stats.VouchersReleased != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if reserved, _ := f.voucherBuckets(t); reserved != 0 {
		t.Fatalf("voucher still reserved: %d", reserved)
	}
	// A use held by an order that is still alive is left alone.
	f.quotes.quote.ExpiresAt = at(11, 0)
	live, _, err := f.svc.Place(context.Background(), Principal{UserID: "user-lan", Bearer: "t"}, "key-2", "Q-2")
	if err != nil {
		t.Fatal(err)
	}
	f.sweep(t, 10, 6)
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ReservedQuota != 1 || f.order(t, live.OrderID).Status != "RESERVED" {
		t.Fatalf("a live order lost its voucher: %+v", c)
	}
}

func TestAnAttemptWhoseCreationResponseWasLostIsClosedForTheClosedOrder(t *testing.T) {
	f := newClockFixture(at(10, 0))
	f.payments.failure = dependency.Wrap(errors.New("response lost"))
	f.payments.closeErr = dependency.Wrap(errors.New("payment still down"))
	o, _, _ := place(t, f.fixture, lan, "key-1", "Q-1")
	if o.Status != "FAILED_CLOSED" || o.FailureCode != FailureDependencyUnavailable {
		t.Fatalf("order = %+v", o)
	}
	before := len(f.payments.closes)
	f.payments.closeErr = nil // the payment service is back
	stats := f.sweep(t, 10, 2)
	if stats.AttemptsClosed != 1 || len(f.payments.closes) != before+1 {
		t.Fatalf("stats=%+v closes=%v", stats, f.payments.closes)
	}
}

func TestRunSweeperSweepsUntilCancelled(t *testing.T) {
	f := newClockFixture(at(10, 0))
	o := placed(t, f.fixture)
	*f.at = at(10, 20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.svc.RunSweeper(ctx, 5*time.Millisecond); close(done) }()
	deadline := time.After(2 * time.Second)
	for f.order(t, o.OrderID).Status != "FAILED_CLOSED" {
		select {
		case <-deadline:
			t.Fatal("the background sweeper did not close the expired order")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunSweeper did not stop after cancel")
	}
}
