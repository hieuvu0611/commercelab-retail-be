package attempt

import "context"

// Summary is what an order owes and has paid (feeds the refund ceiling, I03).
type Summary struct {
	OrderID       string `json:"orderId"`
	ExpectedVND   int64  `json:"expectedVnd"`
	CapturedVND   int64  `json:"capturedVnd"`
	RefundPending int64  `json:"refundPendingVnd"`
}

// Obligation is money to give back to a customer.
type Obligation struct {
	OrderID   string
	AttemptID string
	AmountVND int64
	Reason    string
	Status    string
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
	// Close ends the attempt because its order is closed (see the package-level Close). It is idempotent.
	Close(ctx context.Context, attemptID string) (Attempt, Change, error)
	// Summary returns the order's payment totals (ErrNotFound when no attempt exists).
	Summary(ctx context.Context, orderID string) (Summary, error)
	// Obligations lists the order's refund obligations.
	Obligations(ctx context.Context, orderID string) ([]Obligation, error)
}
