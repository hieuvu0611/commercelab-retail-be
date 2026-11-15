// Package orders stores customer orders and the idempotency keys that make placing one repeatable (B03, I02).
// It knows nothing about stock, vouchers or payments: the checkout saga drives those and records the result here.
package orders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// Initial states of a new order. Later states belong to the saga (P5-T02).
const (
	StatusPending = "PENDING"
	PaymentUnpaid = "UNPAID"
)

var (
	// ErrIdempotencyConflict means the key was already used for different content (I02, SC04).
	ErrIdempotencyConflict = errors.New("idempotency key was already used with different content")
	// ErrNotFound means no order has the id.
	ErrNotFound = errors.New("order not found")
	// ErrStaleState means the order was not in the state the caller expected, so nothing changed.
	ErrStaleState = errors.New("order is not in the expected state")
)

// Item is one order line, frozen at the price the customer accepted (all amounts are whole VND).
type Item struct {
	SkuID                string `json:"skuId"`
	SkuName              string `json:"skuName"`
	Quantity             int64  `json:"quantity"`
	UnitPriceVND         int64  `json:"unitPriceVnd"`
	LineDiscountVND      int64  `json:"lineDiscountVnd"`
	AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd"`
}

// Order is a placed order. Status is the saga state; PaymentStatus is tracked separately.
type Order struct {
	OrderID          string     `json:"orderId"`
	UserID           string     `json:"-"`
	QuoteID          string     `json:"quoteId"`
	AddressID        string     `json:"addressId"`
	WarehouseID      string     `json:"warehouseId,omitempty"`
	VoucherCode      string     `json:"voucherCode,omitempty"`
	Status           string     `json:"status"`
	PaymentStatus    string     `json:"paymentStatus"`
	FailureCode      string     `json:"failureCode,omitempty"`
	SubtotalVND      int64      `json:"subtotalVnd"`
	DiscountVND      int64      `json:"discountVnd"`
	ShippingFeeVND   int64      `json:"shippingFeeVnd"`
	TotalPayableVND  int64      `json:"totalPayableVnd"`
	CapturedVND      int64      `json:"capturedAmountVnd"`
	RefundPendingVND int64      `json:"refundPendingVnd"`
	RefundedVND      int64      `json:"refundedAmountVnd"`
	ItemCount        int64      `json:"itemCount,omitempty"` // set by Query, which does not load items
	Items            []Item     `json:"items"`
	Version          int64      `json:"version"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// Filter selects orders for Query. Zero values mean "any".
type Filter struct {
	UserID        string
	Statuses      []string
	FailureCode   string
	WarehouseIDs  []string  // only orders of these warehouses (a warehouse manager's scope)
	UpdatedBefore time.Time // updated at or before
	UpdatedAfter  time.Time // updated at or after
	Offset, Limit int       // Limit 0 means 50
}

// DefaultLimit is the page size of Query when Filter.Limit is 0; MaxLimit caps it.
const (
	DefaultLimit = 50
	MaxLimit     = 100
)

// PageLimit applies the default and the cap to a requested page size.
func PageLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

// Store persists orders. Implementations must be safe for concurrent use.
type Store interface {
	// Create claims (user, key) and stores o in one step. When the key already exists with the same hash it
	// returns the stored order and replayed=true; with a different hash it returns ErrIdempotencyConflict.
	// A concurrent duplicate waits for the first call and then replays it, so one key never yields two orders.
	// The events (see pkg/outbox) are written in the same transaction as the order; a replay writes none.
	Create(ctx context.Context, key, hash string, o Order, ev ...events.DomainEvent) (stored Order, replayed bool, err error)
	// Lookup finds the order a key already produced (found=false when the key is new).
	// A key stored with a different hash is ErrIdempotencyConflict.
	Lookup(ctx context.Context, userID, key, hash string) (o Order, found bool, err error)
	// Get returns the order or ErrNotFound.
	Get(ctx context.Context, orderID string) (Order, error)
	// Query lists orders newest first (without their items) and the total number of matches ignoring paging.
	Query(ctx context.Context, f Filter) (list []Order, total int, err error)
	// RecordCapture stores how much money the payment captured and how much of it is owed back (absolute values,
	// so repeating it changes nothing); ErrNotFound when the order does not exist.
	RecordCapture(ctx context.Context, orderID string, capturedVND, refundPendingVND int64) error
	// RecordRefund stores how much was paid back and how much is still owed, with the resulting payment status
	// (absolute values, so repeating it changes nothing); ErrNotFound when the order does not exist.
	RecordRefund(ctx context.Context, orderID string, refundedVND, refundPendingVND int64, paymentStatus string) error
	// Transition moves the order from one status to another (compare-and-set); ErrStaleState when the order
	// is not in from, ErrNotFound when it does not exist.
	// The events are written in the same transaction as the state change, and not at all when it is refused.
	Transition(ctx context.Context, orderID, from, to, paymentStatus, failureCode string, ev ...events.DomainEvent) (Order, error)
}

// HashIntent fingerprints what a place-order call asks for. The payload (the quote) is part of it, so reusing
// a key for another quote is a conflict instead of a silent replay (I02, SC04).
func HashIntent(userID, quoteID string) string {
	sum := sha256.Sum256([]byte(userID + "|" + quoteID))
	return hex.EncodeToString(sum[:])
}
