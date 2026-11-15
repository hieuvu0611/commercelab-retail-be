// Package orders stores customer orders and the idempotency keys that make placing one repeatable (B03, I02).
// It knows nothing about stock, vouchers or payments: the checkout saga drives those and records the result here.
package orders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
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
	Items            []Item     `json:"items"`
	Version          int64      `json:"version"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// Store persists orders. Implementations must be safe for concurrent use.
type Store interface {
	// Create claims (user, key) and stores o in one step. When the key already exists with the same hash it
	// returns the stored order and replayed=true; with a different hash it returns ErrIdempotencyConflict.
	// A concurrent duplicate waits for the first call and then replays it, so one key never yields two orders.
	Create(ctx context.Context, key, hash string, o Order) (stored Order, replayed bool, err error)
	// Lookup finds the order a key already produced (found=false when the key is new).
	// A key stored with a different hash is ErrIdempotencyConflict.
	Lookup(ctx context.Context, userID, key, hash string) (o Order, found bool, err error)
	// Get returns the order or ErrNotFound.
	Get(ctx context.Context, orderID string) (Order, error)
	// RecordCapture stores how much money the payment captured and how much of it is owed back (absolute values,
	// so repeating it changes nothing); ErrNotFound when the order does not exist.
	RecordCapture(ctx context.Context, orderID string, capturedVND, refundPendingVND int64) error
	// Transition moves the order from one status to another (compare-and-set); ErrStaleState when the order
	// is not in from, ErrNotFound when it does not exist.
	Transition(ctx context.Context, orderID, from, to, paymentStatus, failureCode string) (Order, error)
}

// HashIntent fingerprints what a place-order call asks for. The payload (the quote) is part of it, so reusing
// a key for another quote is a conflict instead of a silent replay (I02, SC04).
func HashIntent(userID, quoteID string) string {
	sum := sha256.Sum256([]byte(userID + "|" + quoteID))
	return hex.EncodeToString(sum[:])
}
