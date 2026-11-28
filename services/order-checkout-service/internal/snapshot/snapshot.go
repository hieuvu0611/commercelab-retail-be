// Package snapshot keeps the frozen commercial snapshot of an order (I04): what the customer saw and paid at
// purchase time, whatever the catalog, prices or address book say afterwards (SC07: a backpack bought at a
// 270,000 VND allocated price stays 270,000 VND in the order). It holds personal data (the delivery address):
// never log it.
package snapshot

import (
	"context"
	"sync"
	"time"
)

// Line is one purchased line at purchase time (whole VND).
type Line struct {
	SkuID                string `json:"skuId" bson:"sku_id"`
	SkuName              string `json:"skuName" bson:"sku_name"`
	ImageURL             string `json:"imageUrl,omitempty" bson:"image_url,omitempty"`
	Quantity             int64  `json:"quantity" bson:"quantity"`
	UnitPriceVND         int64  `json:"unitPriceVnd" bson:"unit_price_vnd"`
	LineDiscountVND      int64  `json:"lineDiscountVnd" bson:"line_discount_vnd"`
	AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd" bson:"allocated_unit_paid_vnd"`
}

const RedactedField = "[REDACTED]"

// Address is the delivery address copied at purchase time.
type Address struct {
	Recipient string `json:"recipient" bson:"recipient"`
	Phone     string `json:"phone" bson:"phone"`
	Line1     string `json:"line1" bson:"line1"`
	Ward      string `json:"ward" bson:"ward"`
	District  string `json:"district" bson:"district"`
	Province  string `json:"province" bson:"province"`
}

func RedactedAddress() Address {
	return Address{
		Recipient: RedactedField,
		Phone:     RedactedField,
		Line1:     RedactedField,
		Ward:      RedactedField,
		District:  RedactedField,
		Province:  RedactedField,
	}
}

// Snapshot is the frozen purchase-time contract of one order.
type Snapshot struct {
	OrderID         string     `json:"orderId" bson:"order_id"`
	UserID          string     `json:"-" bson:"user_id"`
	Channel         string     `json:"channel,omitempty" bson:"channel,omitempty"`
	WarehouseID     string     `json:"warehouseId,omitempty" bson:"warehouse_id,omitempty"`
	ReceiptNumber   string     `json:"receiptNumber,omitempty" bson:"receipt_number,omitempty"`
	QuoteID         string     `json:"quoteId" bson:"quote_id"`
	VoucherCode     string     `json:"voucherCode,omitempty" bson:"voucher_code,omitempty"`
	Lines           []Line     `json:"lines" bson:"lines"`
	SubtotalVND     int64      `json:"subtotalVnd" bson:"subtotal_vnd"`
	DiscountVND     int64      `json:"discountVnd" bson:"discount_vnd"`
	ShippingFeeVND  int64      `json:"shippingFeeVnd" bson:"shipping_fee_vnd"`
	TotalPayableVND int64      `json:"totalPayableVnd" bson:"total_payable_vnd"`
	ShippingAddress Address    `json:"shippingAddress" bson:"shipping_address"`
	PIIRedacted     bool       `json:"piiRedacted,omitempty" bson:"pii_redacted,omitempty"`
	PIIRedactedAt   *time.Time `json:"piiRedactedAt,omitempty" bson:"pii_redacted_at,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" bson:"created_at"`
}

type Store interface {
	// Save stores s unless the order already has a snapshot (then nothing changes and nil is returned), so
	// retrying after a crash is safe and the first write stays frozen.
	Save(ctx context.Context, s Snapshot) error
	// Get returns the snapshot of an order (found=false when there is none).
	Get(ctx context.Context, orderID string) (s Snapshot, found bool, err error)
	RedactUserPII(ctx context.Context, userID string, erasedAt time.Time) (int64, error)
}

// MemoryStore is the dev/test store used when MongoDB is absent (ALLOW_MEMORY_FALLBACK=true).
type MemoryStore struct {
	mu   sync.Mutex
	byID map[string]Snapshot
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{byID: map[string]Snapshot{}} }

func cloneSnapshot(s Snapshot) Snapshot {
	s.Lines = append([]Line(nil), s.Lines...)
	if s.PIIRedactedAt != nil {
		t := s.PIIRedactedAt.UTC()
		s.PIIRedactedAt = &t
	}
	return s
}

func (m *MemoryStore) Save(_ context.Context, s Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byID[s.OrderID]; !exists {
		m.byID[s.OrderID] = cloneSnapshot(s)
	}
	return nil
}

func (m *MemoryStore) Get(_ context.Context, orderID string) (Snapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[orderID]
	if !ok {
		return Snapshot{}, false, nil
	}
	return cloneSnapshot(s), true, nil
}

func (m *MemoryStore) RedactUserPII(_ context.Context, userID string, erasedAt time.Time) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	ts := erasedAt.UTC()
	if erasedAt.IsZero() {
		ts = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var updated int64
	for id, s := range m.byID {
		if s.UserID != userID {
			continue
		}
		t := ts
		s.ShippingAddress = RedactedAddress()
		s.PIIRedacted = true
		s.PIIRedactedAt = &t
		m.byID[id] = s
		updated++
	}
	return updated, nil
}
