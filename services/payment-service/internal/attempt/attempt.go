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
)

// UnknownWindow is how long an UNKNOWN payment may still be captured normally (P05).
const UnknownWindow = 30 * time.Minute

// Outcome is what the payment provider reports.
type Outcome string

const (
	OutcomeCaptured Outcome = "CAPTURED"
	OutcomeFailed   Outcome = "FAILED"
	OutcomeUnknown  Outcome = "UNKNOWN"
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
	// ErrIdempotencyConflict: the key was used for different content.
	ErrIdempotencyConflict = errors.New("idempotency key was already used with different content")
)

// Attempt is the single payment attempt of an order (all amounts whole VND).
type Attempt struct {
	AttemptID         string     `json:"attemptId"`
	OrderID           string     `json:"orderId"`
	UserID            string     `json:"-"`
	IdempotencyKey    string     `json:"-"`
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
	// LateRefundVND is the amount owed back because it was captured after the order was closed (SC05).
	LateRefundVND int64
}

// Apply applies a reported outcome. amountVND is used for CAPTURED only. It never reopens a closed attempt:
// a capture after the window is CAPTURED_LATE and creates a refund obligation, not a confirmation.
func Apply(a Attempt, o Outcome, amountVND int64, now time.Time) (Attempt, Change, error) {
	switch o {
	case OutcomeCaptured:
		if amountVND <= 0 {
			return a, Change{}, ErrInvalidOutcome
		}
	case OutcomeFailed, OutcomeUnknown:
	default:
		return a, Change{}, ErrInvalidOutcome
	}

	switch a.Status {
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

// Expire marks an UNKNOWN attempt whose window has ended. It reports false for any other attempt.
func Expire(a Attempt, now time.Time) (Attempt, bool) {
	if a.Status != StatusUnknown || a.UnknownDeadline == nil || !now.After(*a.UnknownDeadline) {
		return a, false
	}
	a.Status = StatusExpired30m
	return a, true
}
