package voucher

import (
	"context"
	"time"
)

// Refusal is the reason a voucher could not be reserved for an order (one of the Reason* codes).
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return "voucher cannot be reserved: " + r.Reason }

// Reservation is a voucher use an order holds.
type Reservation struct{ Code, UserID, OrderID string }

// Reserver holds and returns the voucher use of one order (P12). Both calls are idempotent per order id:
// a repeated Reserve takes no second use and a repeated or unmatched Release changes nothing.
type Reserver interface {
	// Reserve takes one use of the campaign for the order if the rules allow it at now (status, window, quota,
	// per-customer limit); otherwise it returns a *Refusal and changes nothing.
	Reserve(ctx context.Context, code, userID, orderID string, now time.Time) error
	// ReservedBefore lists RESERVED uses taken at or before cutoff (oldest first, at most limit), so the sweeper can
	// check them against their orders and give back the ones whose order is closed.
	ReservedBefore(ctx context.Context, cutoff time.Time, limit int) ([]Reservation, error)
	// Consume turns the RESERVED use of the order into a CONSUMED one (payment captured). It is idempotent and a
	// no-op for an order that holds no reserved use.
	Consume(ctx context.Context, code, orderID string) error
	// Release gives a RESERVED use of the order back.
	Release(ctx context.Context, code, orderID string) error
}
