// Package saga is the pure state machine of the checkout saga (B03, P03-P05). It performs no I/O and reads no
// clock: given the current state and an event it returns the next state, the payment status and the effects the
// caller must carry out. Effects are declarative and must be idempotent when executed (releasing something that
// was never held is a no-op), because a crashed saga is replayed.
package saga

import (
	"errors"
	"fmt"
)

// State is the order's checkout state, stored in orders.orders.status.
type State string

const (
	// StatePending: the order is recorded, nothing is held yet.
	StatePending State = "PENDING"
	// StateReserved: stock and voucher are held, the customer can pay (10 minutes).
	StateReserved State = "RESERVED"
	// StateUnknownHold: the payment outcome is unknown; stock stays held for 30 minutes and nothing ships.
	StateUnknownHold State = "UNKNOWN_HOLD"
	// StateConfirmed: the expected amount was captured.
	StateConfirmed State = "CONFIRMED"
	// StateInFulfillment: the warehouse has a shipment for the confirmed order (set from a fulfillment event).
	StateInFulfillment State = "IN_FULFILLMENT"
	// StateDelivered: the carrier delivered the parcel (set from a fulfillment event).
	StateDelivered State = "DELIVERED"
	// StateFailedClosed: the order is closed and everything it held was released.
	StateFailedClosed State = "FAILED_CLOSED"
)

// Payment statuses stored in orders.orders.payment_status.
const (
	PayUnpaid     = "UNPAID"
	PayProcessing = "PROCESSING"
	PayUnknown30m = "UNKNOWN_30M"
	PayCaptured   = "CAPTURED"
	PayFailed     = "FAILED"
)

// Effect is a side effect the caller performs after storing the new state.
type Effect string

const (
	CommitStock            Effect = "COMMIT_STOCK"
	ConsumeVoucher         Effect = "CONSUME_VOUCHER"
	ReleaseStock           Effect = "RELEASE_STOCK"
	ReleaseVoucher         Effect = "RELEASE_VOUCHER"
	HoldStockException     Effect = "HOLD_STOCK_EXCEPTION"
	OpenExceptionCase      Effect = "OPEN_EXCEPTION_CASE"
	RecordRefundObligation Effect = "RECORD_REFUND_OBLIGATION"
)

// ErrInvalidTransition is returned for a state and event that do not go together.
var ErrInvalidTransition = errors.New("invalid saga transition")

// Event is something that happened to the order. The set is closed.
type Event interface{ sagaEvent() }

type (
	// Reserved: stock and voucher were reserved.
	Reserved struct{}
	// ReserveFailed: stock or voucher could not be reserved (SC03); whatever was taken is released.
	ReserveFailed struct{}
	// PaymentCaptured: money was captured. Confirmation needs CapturedVND == ExpectedVND (P04).
	PaymentCaptured struct{ CapturedVND, ExpectedVND int64 }
	// PaymentFailed: the payment definitely failed.
	PaymentFailed struct{}
	// PaymentUnknown: the payment provider gave no answer (P05).
	PaymentUnknown struct{}
	// HoldExpired: the 10-minute reservation (RESERVED) or the 30-minute window (UNKNOWN_HOLD) ended.
	HoldExpired struct{}
)

func (Reserved) sagaEvent()        {}
func (ReserveFailed) sagaEvent()   {}
func (PaymentCaptured) sagaEvent() {}
func (PaymentFailed) sagaEvent()   {}
func (PaymentUnknown) sagaEvent()  {}
func (HoldExpired) sagaEvent()     {}

// Result is the outcome of a transition.
type Result struct {
	State         State
	PaymentStatus string
	Effects       []Effect
}

func invalid(s State, e Event) (Result, error) {
	return Result{}, fmt.Errorf("%w: %T in state %s", ErrInvalidTransition, e, s)
}

func result(s State, pay string, effects ...Effect) (Result, error) {
	return Result{State: s, PaymentStatus: pay, Effects: effects}, nil
}

// Next applies e to s.
func Next(s State, e Event) (Result, error) {
	switch s {
	case StatePending:
		switch e.(type) {
		case Reserved:
			return result(StateReserved, PayProcessing)
		case ReserveFailed:
			return result(StateFailedClosed, PayUnpaid, ReleaseVoucher)
		}
	case StateReserved, StateUnknownHold:
		return fromHeld(s, e)
	case StateConfirmed, StateInFulfillment, StateDelivered:
		if _, ok := e.(PaymentCaptured); ok {
			return result(s, PayCaptured) // a repeated capture notice changes nothing, however far the order got
		}
	case StateFailedClosed:
		switch e.(type) {
		case PaymentCaptured:
			// SC05: money arrived after the order was closed. The order is not restored; the customer is refunded.
			return result(StateFailedClosed, PayFailed, RecordRefundObligation)
		case PaymentFailed:
			return result(StateFailedClosed, PayFailed)
		}
	}
	return invalid(s, e)
}

// fromHeld handles the two states in which stock is held and a payment is expected.
func fromHeld(s State, e Event) (Result, error) {
	switch ev := e.(type) {
	case PaymentCaptured:
		if ev.ExpectedVND <= 0 || ev.CapturedVND != ev.ExpectedVND {
			// Wrong money never confirms an order: keep the stock held and let a person decide.
			return result(StateUnknownHold, PayUnknown30m, HoldStockException, OpenExceptionCase)
		}
		return result(StateConfirmed, PayCaptured, CommitStock, ConsumeVoucher)
	case PaymentFailed:
		return result(StateFailedClosed, PayFailed, ReleaseStock, ReleaseVoucher)
	case PaymentUnknown:
		if s == StateUnknownHold {
			return result(StateUnknownHold, PayUnknown30m)
		}
		return result(StateUnknownHold, PayUnknown30m, HoldStockException)
	case HoldExpired:
		if s == StateUnknownHold {
			return result(StateFailedClosed, PayUnknown30m, ReleaseStock, ReleaseVoucher, OpenExceptionCase)
		}
		return result(StateFailedClosed, PayUnpaid, ReleaseStock, ReleaseVoucher)
	}
	return invalid(s, e)
}

// IsPaid reports whether the order was confirmed (stock committed, voucher consumed), including the states
// after confirmation that fulfillment events set.
func (s State) IsPaid() bool {
	return s == StateConfirmed || s == StateInFulfillment || s == StateDelivered
}
