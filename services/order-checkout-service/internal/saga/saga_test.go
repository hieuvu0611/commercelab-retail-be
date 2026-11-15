package saga

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

func effects(r Result) []string {
	out := make([]string, len(r.Effects))
	for i, e := range r.Effects {
		out[i] = string(e)
	}
	sort.Strings(out)
	return out
}

func mustNext(t *testing.T, s State, e Event) Result {
	t.Helper()
	r, err := Next(s, e)
	if err != nil {
		t.Fatalf("Next(%s, %T) = %v", s, e, err)
	}
	return r
}

func TestHappyPath_SC01(t *testing.T) {
	r := mustNext(t, StatePending, Reserved{})
	if r.State != StateReserved || r.PaymentStatus != PayProcessing || len(r.Effects) != 0 {
		t.Fatalf("after Reserved = %+v", r)
	}
	r = mustNext(t, r.State, PaymentCaptured{CapturedVND: 660000, ExpectedVND: 660000})
	if r.State != StateConfirmed || r.PaymentStatus != PayCaptured {
		t.Fatalf("after capture = %+v", r)
	}
	if want := []string{"COMMIT_STOCK", "CONSUME_VOUCHER"}; !reflect.DeepEqual(effects(r), want) {
		t.Fatalf("effects = %v, want %v", effects(r), want)
	}
}

func TestOutOfStock_SC03(t *testing.T) {
	r := mustNext(t, StatePending, ReserveFailed{})
	if r.State != StateFailedClosed || r.PaymentStatus != PayUnpaid {
		t.Fatalf("after ReserveFailed = %+v", r)
	}
	// Only the voucher may have been taken; no stock is held and no payment was created.
	if want := []string{"RELEASE_VOUCHER"}; !reflect.DeepEqual(effects(r), want) {
		t.Fatalf("effects = %v, want %v", effects(r), want)
	}
}

func TestDefinitePaymentFailureReleasesEverything(t *testing.T) {
	for _, from := range []State{StateReserved, StateUnknownHold} {
		r := mustNext(t, from, PaymentFailed{})
		if r.State != StateFailedClosed || r.PaymentStatus != PayFailed {
			t.Fatalf("from %s: %+v", from, r)
		}
		if want := []string{"RELEASE_STOCK", "RELEASE_VOUCHER"}; !reflect.DeepEqual(effects(r), want) {
			t.Fatalf("from %s: effects = %v", from, effects(r))
		}
	}
}

func TestUnknownPaymentHoldsStockAndNeverShips_SC05(t *testing.T) {
	r := mustNext(t, StateReserved, PaymentUnknown{})
	if r.State != StateUnknownHold || r.PaymentStatus != PayUnknown30m {
		t.Fatalf("after unknown = %+v", r)
	}
	if want := []string{"HOLD_STOCK_EXCEPTION"}; !reflect.DeepEqual(effects(r), want) {
		t.Fatalf("effects = %v, want %v", effects(r), want)
	}
	// Paid inside the window: confirmed.
	r2 := mustNext(t, StateUnknownHold, PaymentCaptured{CapturedVND: 660000, ExpectedVND: 660000})
	if r2.State != StateConfirmed {
		t.Fatalf("capture inside window = %+v", r2)
	}
	// Window ends without payment: closed, stock and voucher released, exception case opened.
	r3 := mustNext(t, StateUnknownHold, HoldExpired{})
	if r3.State != StateFailedClosed {
		t.Fatalf("hold expiry = %+v", r3)
	}
	if want := []string{"OPEN_EXCEPTION_CASE", "RELEASE_STOCK", "RELEASE_VOUCHER"}; !reflect.DeepEqual(effects(r3), want) {
		t.Fatalf("expiry effects = %v, want %v", effects(r3), want)
	}
}

func TestLateCaptureAfterClosureDoesNotReopenTheOrder_SC05(t *testing.T) {
	r := mustNext(t, StateFailedClosed, PaymentCaptured{CapturedVND: 660000, ExpectedVND: 660000})
	if r.State != StateFailedClosed {
		t.Fatalf("late capture reopened the order: %+v", r)
	}
	if want := []string{"RECORD_REFUND_OBLIGATION"}; !reflect.DeepEqual(effects(r), want) {
		t.Fatalf("effects = %v, want %v", effects(r), want)
	}
}

func TestCapturedAmountMustMatchExpected(t *testing.T) {
	for _, from := range []State{StateReserved, StateUnknownHold} {
		r := mustNext(t, from, PaymentCaptured{CapturedVND: 650000, ExpectedVND: 660000})
		if r.State == StateConfirmed {
			t.Fatalf("from %s: a short capture confirmed the order", from)
		}
		if r.State != StateUnknownHold || r.PaymentStatus != PayUnknown30m {
			t.Fatalf("from %s: mismatch result = %+v", from, r)
		}
		if want := []string{"HOLD_STOCK_EXCEPTION", "OPEN_EXCEPTION_CASE"}; !reflect.DeepEqual(effects(r), want) {
			t.Fatalf("from %s: effects = %v, want %v", from, effects(r), want)
		}
	}
	// An overpayment is just as wrong as a short one.
	if r := mustNext(t, StateReserved, PaymentCaptured{CapturedVND: 670000, ExpectedVND: 660000}); r.State == StateConfirmed {
		t.Fatal("an over capture confirmed the order")
	}
	// A zero or negative expected amount never confirms.
	if r := mustNext(t, StateReserved, PaymentCaptured{CapturedVND: 0, ExpectedVND: 0}); r.State == StateConfirmed {
		t.Fatal("a zero capture confirmed the order")
	}
}

func TestDuplicateCaptureOnConfirmedOrderIsANoOp(t *testing.T) {
	r := mustNext(t, StateConfirmed, PaymentCaptured{CapturedVND: 660000, ExpectedVND: 660000})
	if r.State != StateConfirmed || len(r.Effects) != 0 {
		t.Fatalf("duplicate capture = %+v", r)
	}
}

func TestReservationExpiryClosesAnUnpaidOrder(t *testing.T) {
	r := mustNext(t, StateReserved, HoldExpired{})
	if r.State != StateFailedClosed {
		t.Fatalf("expiry = %+v", r)
	}
	if want := []string{"RELEASE_STOCK", "RELEASE_VOUCHER"}; !reflect.DeepEqual(effects(r), want) {
		t.Fatalf("effects = %v", effects(r))
	}
}

func TestEveryOtherStateEventPairIsInvalid(t *testing.T) {
	valid := map[State]map[string]bool{
		StatePending:      {"Reserved": true, "ReserveFailed": true},
		StateReserved:     {"PaymentCaptured": true, "PaymentFailed": true, "PaymentUnknown": true, "HoldExpired": true},
		StateUnknownHold:  {"PaymentCaptured": true, "PaymentFailed": true, "PaymentUnknown": true, "HoldExpired": true},
		StateConfirmed:    {"PaymentCaptured": true},
		StateFailedClosed: {"PaymentCaptured": true, "PaymentFailed": true},
	}
	events := map[string]Event{
		"Reserved": Reserved{}, "ReserveFailed": ReserveFailed{}, "PaymentCaptured": PaymentCaptured{CapturedVND: 1, ExpectedVND: 1},
		"PaymentFailed": PaymentFailed{}, "PaymentUnknown": PaymentUnknown{}, "HoldExpired": HoldExpired{},
	}
	for _, state := range []State{StatePending, StateReserved, StateUnknownHold, StateConfirmed, StateFailedClosed} {
		for name, ev := range events {
			_, err := Next(state, ev)
			if valid[state][name] {
				if err != nil {
					t.Errorf("Next(%s, %s) should be allowed: %v", state, name, err)
				}
				continue
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("Next(%s, %s) = %v, want ErrInvalidTransition", state, name, err)
			}
		}
	}
	if _, err := Next(State("BOGUS"), Reserved{}); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("unknown state = %v", err)
	}
}

func TestPaymentUnknownAgainInHoldKeepsTheHold(t *testing.T) {
	r := mustNext(t, StateUnknownHold, PaymentUnknown{})
	if r.State != StateUnknownHold || len(r.Effects) != 0 {
		t.Fatalf("repeated unknown = %+v", r)
	}
}

func TestLateFailureAfterClosureIsANoOp(t *testing.T) {
	r := mustNext(t, StateFailedClosed, PaymentFailed{})
	if r.State != StateFailedClosed || len(r.Effects) != 0 {
		t.Fatalf("late failure = %+v", r)
	}
}

func TestDuplicateCaptureAfterFulfillmentStartedIsANoOp(t *testing.T) {
	for _, from := range []State{StateInFulfillment, StateDelivered} {
		r := mustNext(t, from, PaymentCaptured{CapturedVND: 660000, ExpectedVND: 660000})
		if r.State != from || len(r.Effects) != 0 {
			t.Fatalf("from %s: duplicate capture = %+v", from, r)
		}
	}
}
