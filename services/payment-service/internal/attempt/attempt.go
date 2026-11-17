// Package attempt is the payment attempt of an order: its states, the outcomes a payment provider reports and the
// rules for applying them (P04, P05, SC05). The domain part is pure; stores persist attempts atomically.
package attempt

import (
	"errors"
	"time"
)

// Statuses of an attempt.
const (
	StatusProcessing   = "PROCESSING"
	StatusUnknown      = "UNKNOWN"
	StatusCaptured     = "CAPTURED"
	StatusFailed       = "FAILED"
	StatusExpired30m   = "EXPIRED_30M"
	StatusCapturedLate = "CAPTURED_LATE"
	StatusCODPending   = "COD_PENDING"
	StatusCOLLECTED    = "COLLECTED"
	StatusCODCancelled = "COD_CANCELLED"
)

// Reasons of a refund obligation: why money is owed back.
const (
	// ReasonLateCapture: the money arrived (or the order closed) after the order's payment window.
	ReasonLateCapture = "LATE_CAPTURE"
	// ReasonOrderCancelled: the customer cancelled a paid order before handover.
	ReasonOrderCancelled = "ORDER_CANCELLED"
)

// UnknownWindow is how long an UNKNOWN payment may still be captured normally (P05).
const UnknownWindow = 30 * time.Minute

// Outcome is what the payment provider reports.
type Outcome string

const (
	OutcomeCaptured     Outcome = "CAPTURED"
	OutcomeFailed       Outcome = "FAILED"
	OutcomeUnknown      Outcome = "UNKNOWN"
	OutcomeCODCollected Outcome = "COLLECTED"
	OutcomeCODCancelled Outcome = "COD_CANCELLED"
)

var (
	// ErrFinalOutcome: the attempt already ended differently; a final outcome never flips.
	ErrFinalOutcome = errors.New("the payment attempt already has a different final outcome")
	// ErrInvalidOutcome: the outcome or amount is not valid.
	ErrInvalidOutcome = errors.New("invalid payment outcome")
	// ErrNotFound: no attempt has the id.
	ErrNotFound = errors.New("payment attempt not found")
	// ErrActivePayment: another PROCESSING or UNKNOWN attempt exists for the order (SC05).
	ErrActivePayment = errors.New("a payment attempt is already in progress for this order")
	// ErrAttemptExists: the order already has a finished attempt; a failed payment closes the order.
	ErrAttemptExists = errors.New("the order already has a payment attempt")
	// ErrRefundCeiling: paying this out would refund more than was captured (I03).
	ErrRefundCeiling = errors.New("a refund cannot exceed the captured amount")
	// ErrIdempotencyConflict: the key was used for different content.
	ErrIdempotencyConflict = errors.New("idempotency key was already used with different content")
)

// Attempt is the single payment attempt of an order (all amounts whole VND).
type Attempt struct {
	AttemptID         string     `json:"attemptId"`
	OrderID           string     `json:"orderId"`
	UserID            string     `json:"-"`
	IdempotencyKey    string     `json:"-"`
	PaymentMethod     string     `json:"paymentMethod,omitempty"`
	AmountVND         int64      `json:"amountVnd"`
	CapturedAmountVND int64      `json:"capturedAmountVnd"`
	Status            string     `json:"status"`
	UnknownDeadline   *time.Time `json:"unknownDeadline,omitempty"`
	OutcomeAt         *time.Time `json:"outcomeAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// Change tells the store what to do besides saving the attempt.
type Change struct {
	// Replay: nothing changed; the outcome was already applied.
	Replay bool
	// CapturedVND is the money newly captured by this call (added to the order's captured total).
	CapturedVND int64
	// LateRefundVND is the amount owed back because it was captured after the order was closed (SC05) or the
	// order was cancelled after payment.
	LateRefundVND int64
	// RefundReason is why LateRefundVND is owed (empty means ReasonLateCapture).
	RefundReason string
}

// Apply applies a reported outcome. amountVND is used for CAPTURED only. It never reopens a closed attempt:
// a capture after the window is CAPTURED_LATE and creates a refund obligation, not a confirmation.
func Apply(a Attempt, o Outcome, amountVND int64, now time.Time) (Attempt, Change, error) {
	switch o {
	case OutcomeCaptured, OutcomeCODCollected:
		if amountVND <= 0 {
			if a.PaymentMethod == "COD" || a.Status == StatusCODPending {
				amountVND = a.AmountVND
			} else {
				return a, Change{}, ErrInvalidOutcome
			}
		}
	case OutcomeFailed, OutcomeUnknown, OutcomeCODCancelled:
	default:
		return a, Change{}, ErrInvalidOutcome
	}

	switch a.Status {
	case StatusCODPending:
		switch o {
		case OutcomeCaptured, OutcomeCODCollected:
			nextCap := a.CapturedAmountVND + amountVND
			if nextCap > a.AmountVND {
				nextCap = a.AmountVND
			}
			delta := nextCap - a.CapturedAmountVND
			a.CapturedAmountVND = nextCap
			if nextCap >= a.AmountVND {
				a.Status, a.OutcomeAt = StatusCOLLECTED, &now
			}
			return a, Change{CapturedVND: delta}, nil
		case OutcomeFailed, OutcomeCODCancelled:
			a.Status, a.OutcomeAt = StatusCODCancelled, &now
			return a, Change{}, nil
		default:
			return a, Change{}, ErrInvalidOutcome
		}
	case StatusCOLLECTED:
		if (o == OutcomeCaptured || o == OutcomeCODCollected) && (amountVND == a.CapturedAmountVND || amountVND <= 0 || a.PaymentMethod == "COD") {
			return a, Change{Replay: true}, nil
		}
		return a, Change{}, ErrFinalOutcome
	case StatusCODCancelled:
		if o == OutcomeFailed || o == OutcomeCODCancelled {
			return a, Change{Replay: true}, nil
		}
		return a, Change{}, ErrFinalOutcome
	case StatusProcessing:
		return fromProcessing(a, o, amountVND, now)
	case StatusUnknown:
		return fromUnknown(a, o, amountVND, now)
	case StatusExpired30m:
		return fromExpired(a, o, amountVND, now)
	case StatusCaptured, StatusCapturedLate:
		if o == OutcomeCaptured && amountVND == a.CapturedAmountVND {
			return a, Change{Replay: true}, nil
		}
		return a, Change{}, ErrFinalOutcome
	case StatusFailed:
		if o == OutcomeFailed {
			return a, Change{Replay: true}, nil
		}
		return a, Change{}, ErrFinalOutcome
	}
	return a, Change{}, ErrInvalidOutcome
}

func fromProcessing(a Attempt, o Outcome, amountVND int64, now time.Time) (Attempt, Change, error) {
	switch o {
	case OutcomeCaptured:
		return capture(a, StatusCaptured, amountVND, now), Change{CapturedVND: amountVND}, nil
	case OutcomeFailed:
		a.Status, a.OutcomeAt = StatusFailed, &now
		return a, Change{}, nil
	}
	deadline := now.Add(UnknownWindow)
	a.Status, a.UnknownDeadline = StatusUnknown, &deadline
	return a, Change{}, nil
}

func fromUnknown(a Attempt, o Outcome, amountVND int64, now time.Time) (Attempt, Change, error) {
	switch o {
	case OutcomeCaptured:
		if a.UnknownDeadline != nil && now.After(*a.UnknownDeadline) {
			return capture(a, StatusCapturedLate, amountVND, now), Change{CapturedVND: amountVND, LateRefundVND: amountVND}, nil
		}
		return capture(a, StatusCaptured, amountVND, now), Change{CapturedVND: amountVND}, nil
	case OutcomeFailed:
		a.Status, a.OutcomeAt = StatusFailed, &now
		return a, Change{}, nil
	}
	return a, Change{Replay: true}, nil // still unknown: the window is not extended
}

func fromExpired(a Attempt, o Outcome, amountVND int64, now time.Time) (Attempt, Change, error) {
	if o == OutcomeCaptured {
		return capture(a, StatusCapturedLate, amountVND, now), Change{CapturedVND: amountVND, LateRefundVND: amountVND}, nil
	}
	return a, Change{Replay: true}, nil // the order is already closed; a late failure or unknown changes nothing
}

func capture(a Attempt, status string, amountVND int64, now time.Time) Attempt {
	a.Status, a.CapturedAmountVND, a.OutcomeAt = status, amountVND, &now
	return a
}

// Close ends an attempt because its order is closed (released, timed out or never confirmed). An attempt that
// holds no money becomes EXPIRED_30M, so a capture that still arrives is CAPTURED_LATE and refunded. An attempt
// that already captured money is CAPTURED_LATE with the whole amount owed back. Finished attempts are a replay.
func Close(a Attempt, now time.Time) (Attempt, Change, error) {
	return CloseFor(a, now, ReasonLateCapture)
}

// CloseFor is Close with the reason the captured money (if any) is owed back.
func CloseFor(a Attempt, now time.Time, reason string) (Attempt, Change, error) {
	switch a.Status {
	case StatusProcessing, StatusUnknown:
		a.Status, a.OutcomeAt = StatusExpired30m, &now
		return a, Change{}, nil
	case StatusCaptured:
		a.Status = StatusCapturedLate
		return a, Change{LateRefundVND: a.CapturedAmountVND, RefundReason: reason}, nil
	case StatusCapturedLate, StatusFailed, StatusExpired30m:
		return a, Change{Replay: true}, nil
	}
	return a, Change{}, ErrInvalidOutcome
}

// Expire marks an UNKNOWN attempt whose window has ended. It reports false for any other attempt.
func Expire(a Attempt, now time.Time) (Attempt, bool) {
	if a.Status != StatusUnknown || a.UnknownDeadline == nil || !now.After(*a.UnknownDeadline) {
		return a, false
	}
	a.Status = StatusExpired30m
	return a, true
}
