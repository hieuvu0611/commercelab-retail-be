package attempt

import (
	"context"
	"time"
)

// Summary is what an order owes and has paid (feeds the refund ceiling, I03): refunded plus pending never
// exceeds captured.
type Summary struct {
	OrderID       string `json:"orderId"`
	ExpectedVND   int64  `json:"expectedVnd"`
	CapturedVND   int64  `json:"capturedVnd"`
	RefundedVND   int64  `json:"refundedVnd"`
	RefundPending int64  `json:"refundPendingVnd"`
}

// Obligation statuses.
const (
	ObligationPending = "PENDING"
	ObligationDone    = "DONE"
)

// Obligation is money to give back to a customer. The attempt id is its identity (one per attempt) and the
// idempotency key of the payout.
type Obligation struct {
	OrderID     string
	AttemptID   string
	AmountVND   int64
	Reason      string
	Status      string
	ProviderRef string
	RefundedAt  *time.Time
}

// Store persists attempts. Implementations must be safe for concurrent use and apply an outcome atomically.
type Store interface {
	// Create stores a new PROCESSING attempt keyed by a.IdempotencyKey. The same key with the same order, user
	// and amount returns the stored attempt and replayed=true; the same key with other content is
	// ErrIdempotencyConflict; another attempt for the order is ErrActivePayment or ErrAttemptExists.
	Create(ctx context.Context, a Attempt) (stored Attempt, replayed bool, err error)
	// Get returns the attempt or ErrNotFound.
	Get(ctx context.Context, attemptID string) (Attempt, error)
	// Apply applies a reported outcome in one atomic step. A non-empty eventID is remembered: delivering the same
	// event again changes nothing and returns the stored attempt with Change.Replay. A refund obligation is
	// recorded exactly once per attempt, and the order's captured and refund-pending totals follow the Change.
	Apply(ctx context.Context, attemptID string, o Outcome, amountVND int64, eventID string) (Attempt, Change, error)
	// ApplyCODParcel records a terminal shipment outcome and closes the attempt when all parcels settle.
	ApplyCODParcel(ctx context.Context, attemptID string, parcel CODParcel) (Attempt, Change, error)
	// GetByOrder returns the order's attempt or ErrNotFound.
	GetByOrder(ctx context.Context, orderID string) (Attempt, error)
	// Close ends the attempt because its order is closed (see the package-level Close). It is idempotent.
	Close(ctx context.Context, attemptID string) (Attempt, Change, error)
	// CloseFor is Close with the reason captured money is owed back (ReasonOrderCancelled for a cancellation).
	CloseFor(ctx context.Context, attemptID, reason string) (Attempt, Change, error)
	// PendingRefunds lists obligations that still have to be paid out, oldest first.
	PendingRefunds(ctx context.Context, limit int) ([]Obligation, error)
	// CompleteRefund records that the provider paid the obligation of attemptID (providerRef identifies the
	// payout): the amount moves from refund-pending to refunded and one refunded event is written, all in one
	// step. done=false means it was already completed and nothing changed; a payout above the captured ceiling
	// is ErrRefundCeiling and changes nothing. ErrNotFound when the attempt has no obligation.
	CompleteRefund(ctx context.Context, attemptID, providerRef string) (o Obligation, sum Summary, done bool, err error)
	// Summary returns the order's payment totals (ErrNotFound when no attempt exists).
	Summary(ctx context.Context, orderID string) (Summary, error)
	// Obligations lists the order's refund obligations.
	Obligations(ctx context.Context, orderID string) ([]Obligation, error)
}

// ReservationStore persists RMA refund ceiling reservations and payouts (I03, SC08).
type ReservationStore interface {
	ReserveRefund(ctx context.Context, orderID, reservationID string, amountVND int64) (Summary, error)
	CompleteRefundReservation(ctx context.Context, orderID, reservationID string, amountVND int64, providerRef string) (Summary, error)
}

// MetricsStore aggregates authoritative payment collections and completed refunds.
type MetricsStore interface {
	Metrics(ctx context.Context, since *time.Time) (grossCollectedVND, totalRefundedVND int64, err error)
}
