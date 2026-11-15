package attempt

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func fresh() Attempt {
	return Attempt{AttemptID: "PAY-O-1", OrderID: "O-1", UserID: "user-lan", IdempotencyKey: "pay-O-1", AmountVND: 660000, Status: StatusProcessing, CreatedAt: t0}
}

func apply(t *testing.T, a Attempt, o Outcome, amount int64, at time.Time) (Attempt, Change) {
	t.Helper()
	next, ch, err := Apply(a, o, amount, at)
	if err != nil {
		t.Fatalf("Apply(%s, %s) = %v", a.Status, o, err)
	}
	return next, ch
}

func TestCapturedFromProcessing(t *testing.T) {
	a, ch := apply(t, fresh(), OutcomeCaptured, 660000, t0.Add(time.Minute))
	if a.Status != StatusCaptured || a.CapturedAmountVND != 660000 || a.OutcomeAt == nil || ch.Replay || ch.LateRefundVND != 0 || ch.CapturedVND != 660000 {
		t.Fatalf("attempt = %+v change = %+v", a, ch)
	}
}

func TestACapturedAmountThatDiffersIsRecordedAsIs(t *testing.T) {
	// The payment service reports facts; the order decides that 650,000 is not 660,000 (P04).
	a, ch := apply(t, fresh(), OutcomeCaptured, 650000, t0)
	if a.Status != StatusCaptured || a.CapturedAmountVND != 650000 || ch.CapturedVND != 650000 {
		t.Fatalf("attempt = %+v change = %+v", a, ch)
	}
}

func TestFailedFromProcessing(t *testing.T) {
	a, _ := apply(t, fresh(), OutcomeFailed, 0, t0)
	if a.Status != StatusFailed || a.CapturedAmountVND != 0 {
		t.Fatalf("attempt = %+v", a)
	}
}

func TestUnknownStartsAThirtyMinuteWindow_SC05(t *testing.T) {
	a, _ := apply(t, fresh(), OutcomeUnknown, 0, t0.Add(time.Minute))
	want := t0.Add(31 * time.Minute) // 10:01 + 30 minutes
	if a.Status != StatusUnknown || a.UnknownDeadline == nil || !a.UnknownDeadline.Equal(want) {
		t.Fatalf("attempt = %+v, want deadline %v", a, want)
	}
	again, ch := apply(t, a, OutcomeUnknown, 0, t0.Add(5*time.Minute))
	if !ch.Replay || !again.UnknownDeadline.Equal(want) {
		t.Fatalf("a repeated UNKNOWN must not extend the window: %+v %+v", again, ch)
	}
}

func TestCaptureInsideTheWindowConfirmsNormally(t *testing.T) {
	u, _ := apply(t, fresh(), OutcomeUnknown, 0, t0.Add(time.Minute))
	a, ch := apply(t, u, OutcomeCaptured, 660000, t0.Add(31*time.Minute)) // exactly at the deadline is still inside
	if a.Status != StatusCaptured || ch.LateRefundVND != 0 {
		t.Fatalf("attempt = %+v change = %+v", a, ch)
	}
}

func TestCaptureAfterTheWindowIsLateAndOwedBack_SC05(t *testing.T) {
	u, _ := apply(t, fresh(), OutcomeUnknown, 0, t0.Add(time.Minute))
	// 10:40: past the 10:31 deadline, even if the sweeper has not marked the attempt expired yet.
	a, ch := apply(t, u, OutcomeCaptured, 660000, t0.Add(40*time.Minute))
	if a.Status != StatusCapturedLate || a.CapturedAmountVND != 660000 || ch.LateRefundVND != 660000 || ch.CapturedVND != 660000 {
		t.Fatalf("attempt = %+v change = %+v", a, ch)
	}
	// And after an explicit expiry.
	e, expired := Expire(u, t0.Add(31*time.Minute+time.Second))
	if !expired || e.Status != StatusExpired30m {
		t.Fatalf("Expire = %+v %v", e, expired)
	}
	late, ch := apply(t, e, OutcomeCaptured, 660000, t0.Add(40*time.Minute))
	if late.Status != StatusCapturedLate || ch.LateRefundVND != 660000 {
		t.Fatalf("late after expiry = %+v %+v", late, ch)
	}
}

func TestExpireOnlyTouchesAnOverdueUnknownAttempt(t *testing.T) {
	u, _ := apply(t, fresh(), OutcomeUnknown, 0, t0)
	if _, expired := Expire(u, t0.Add(29*time.Minute)); expired {
		t.Fatal("expired inside the window")
	}
	if _, expired := Expire(u, t0.Add(30*time.Minute)); expired {
		t.Fatal("expired exactly at the deadline (the window is inclusive)")
	}
	if _, expired := Expire(fresh(), t0.Add(time.Hour)); expired {
		t.Fatal("a PROCESSING attempt must not expire here")
	}
	c, _ := apply(t, fresh(), OutcomeCaptured, 660000, t0)
	if _, expired := Expire(c, t0.Add(time.Hour)); expired {
		t.Fatal("a captured attempt must not expire")
	}
}

func TestFinalOutcomesCannotFlip(t *testing.T) {
	captured, _ := apply(t, fresh(), OutcomeCaptured, 660000, t0)
	failed, _ := apply(t, fresh(), OutcomeFailed, 0, t0)
	for name, tc := range map[string]struct {
		a      Attempt
		o      Outcome
		amount int64
	}{
		"captured then failed":       {captured, OutcomeFailed, 0},
		"captured then unknown":      {captured, OutcomeUnknown, 0},
		"captured then other amount": {captured, OutcomeCaptured, 1},
		"failed then captured":       {failed, OutcomeCaptured, 660000},
		"failed then unknown":        {failed, OutcomeUnknown, 0},
	} {
		if _, _, err := Apply(tc.a, tc.o, tc.amount, t0); !errors.Is(err, ErrFinalOutcome) {
			t.Errorf("%s: err = %v, want ErrFinalOutcome", name, err)
		}
	}
}

func TestRepeatingAFinalOutcomeIsAReplay(t *testing.T) {
	captured, _ := apply(t, fresh(), OutcomeCaptured, 660000, t0)
	again, ch := apply(t, captured, OutcomeCaptured, 660000, t0.Add(time.Hour))
	if !ch.Replay || again.Status != StatusCaptured || ch.CapturedVND != 0 {
		t.Fatalf("replay = %+v %+v", again, ch)
	}
	failed, _ := apply(t, fresh(), OutcomeFailed, 0, t0)
	if _, ch := apply(t, failed, OutcomeFailed, 0, t0); !ch.Replay {
		t.Fatal("repeated FAILED is not a replay")
	}
	u, _ := apply(t, fresh(), OutcomeUnknown, 0, t0)
	late, _ := apply(t, u, OutcomeCaptured, 660000, t0.Add(time.Hour))
	if _, ch := apply(t, late, OutcomeCaptured, 660000, t0.Add(2*time.Hour)); !ch.Replay || ch.LateRefundVND != 0 {
		t.Fatalf("repeated late capture = %+v (must not owe the refund twice)", ch)
	}
}

func TestAFailureAfterTheWindowChangesNothing(t *testing.T) {
	u, _ := apply(t, fresh(), OutcomeUnknown, 0, t0)
	e, _ := Expire(u, t0.Add(time.Hour))
	same, ch := apply(t, e, OutcomeFailed, 0, t0.Add(2*time.Hour))
	if same.Status != StatusExpired30m || !ch.Replay {
		t.Fatalf("failure after expiry = %+v %+v", same, ch)
	}
	if _, ch := apply(t, e, OutcomeUnknown, 0, t0.Add(2*time.Hour)); !ch.Replay {
		t.Fatal("UNKNOWN after expiry is not a replay")
	}
}

func TestFailureInsideTheWindowIsDefinite(t *testing.T) {
	u, _ := apply(t, fresh(), OutcomeUnknown, 0, t0)
	a, _ := apply(t, u, OutcomeFailed, 0, t0.Add(10*time.Minute))
	if a.Status != StatusFailed {
		t.Fatalf("attempt = %+v", a)
	}
}

func TestUnknownOutcomeIsRejected(t *testing.T) {
	if _, _, err := Apply(fresh(), Outcome("MAYBE"), 0, t0); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := Apply(fresh(), OutcomeCaptured, -5, t0); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("negative amount err = %v", err)
	}
	if _, _, err := Apply(fresh(), OutcomeCaptured, 0, t0); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("zero capture err = %v", err)
	}
}

func TestCloseEndsAnAttemptWithoutMoney(t *testing.T) {
	for _, from := range []string{StatusProcessing, StatusUnknown} {
		a := fresh()
		a.Status = from
		closed, ch, err := Close(a, t0.Add(time.Hour))
		if err != nil || closed.Status != StatusExpired30m || ch.Replay || ch.LateRefundVND != 0 || ch.CapturedVND != 0 {
			t.Fatalf("close %s = %+v %+v %v", from, closed, ch, err)
		}
		// Money arriving after the order was closed is late: it is refunded, never confirmed.
		late, lch := apply(t, closed, OutcomeCaptured, 660000, t0.Add(2*time.Hour))
		if late.Status != StatusCapturedLate || lch.LateRefundVND != 660000 {
			t.Fatalf("capture after close = %+v %+v", late, lch)
		}
	}
}

func TestCloseOfACapturedAttemptOwesTheMoneyBack(t *testing.T) {
	captured, _ := apply(t, fresh(), OutcomeCaptured, 660000, t0)
	closed, ch, err := Close(captured, t0.Add(time.Hour))
	if err != nil || closed.Status != StatusCapturedLate || ch.LateRefundVND != 660000 || ch.CapturedVND != 0 || ch.Replay {
		t.Fatalf("close captured = %+v %+v %v", closed, ch, err)
	}
	again, ch, err := Close(closed, t0.Add(2*time.Hour))
	if err != nil || !ch.Replay || again.Status != StatusCapturedLate {
		t.Fatalf("second close = %+v %+v %v", again, ch, err)
	}
}

func TestCloseOfFinishedAttemptsChangesNothing(t *testing.T) {
	failed, _ := apply(t, fresh(), OutcomeFailed, 0, t0)
	if got, ch, err := Close(failed, t0); err != nil || !ch.Replay || got.Status != StatusFailed {
		t.Fatalf("close failed = %+v %+v %v", got, ch, err)
	}
	expired := fresh()
	expired.Status = StatusExpired30m
	if got, ch, err := Close(expired, t0); err != nil || !ch.Replay || got.Status != StatusExpired30m {
		t.Fatalf("close expired = %+v %+v %v", got, ch, err)
	}
}
