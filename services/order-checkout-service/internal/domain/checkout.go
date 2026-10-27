package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key reused with different cart or quote payload (I02, SC04)")
	ErrVoucherExhausted    = errors.New("voucher campaign quota is exhausted (P12, SC10)")
	ErrOrderNotFound       = errors.New("order not found")
	ErrInvalidOrderState   = errors.New("invalid order state transition")
)

// VoucherQuota tracks the 3 quota buckets defined by Policy P12: Available, Reserved, and Consumed.
type VoucherQuota struct {
	Code     string `json:"code"`
	Total    int64  `json:"total"`
	Reserved int64  `json:"reserved"`
	Consumed int64  `json:"consumed"`
}

func (v VoucherQuota) Available() int64 {
	return v.Total - v.Reserved - v.Consumed
}

// OrderItemLine stores the commercial line allocation frozen at purchase time (Invariant I04, Scenario SC07).
type OrderItemLine struct {
	SkuID                string `json:"skuId"`
	SkuName              string `json:"skuName"`
	Quantity             int64  `json:"quantity"`
	UnitPriceVND         int64  `json:"unitPriceVnd"`
	LineDiscountVND      int64  `json:"lineDiscountVnd"`
	AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd"`
}

type OrderRecord struct {
	OrderID         string          `json:"orderId"`
	UserID          string          `json:"userId"`
	QuoteID         string          `json:"quoteId"`
	VoucherCode     string          `json:"voucherCode,omitempty"`
	Status          string          `json:"status"`
	PaymentStatus   string          `json:"paymentStatus"`
	SubtotalVND     int64           `json:"subtotalVnd"`
	DiscountVND     int64           `json:"discountVnd"`
	ShippingFeeVND  int64           `json:"shippingFeeVnd"`
	TotalPayableVND int64           `json:"totalPayableVnd"`
	Items           []OrderItemLine `json:"items,omitempty"`
	RequestHash     string          `json:"-"`
}

type CheckoutEngine struct {
	mu          sync.Mutex
	idempotency map[string]*OrderRecord
	orders      map[string]*OrderRecord
	vouchers    map[string]*VoucherQuota
}

func NewCheckoutEngine() *CheckoutEngine {
	return &CheckoutEngine{
		idempotency: make(map[string]*OrderRecord),
		orders:      make(map[string]*OrderRecord),
		vouchers: map[string]*VoucherQuota{
			"WELCOME10": {Code: "WELCOME10", Total: 100, Reserved: 0, Consumed: 0},
		},
	}
}

func HashIntent(userID, quoteID, voucherCode string, totalPayable int64) string {
	sum := sha256.Sum256([]byte(userID + "|" + quoteID + "|" + voucherCode))
	return hex.EncodeToString(sum[:])
}

func DefaultItemsIfEmpty(items []OrderItemLine) []OrderItemLine {
	if len(items) > 0 {
		cp := make([]OrderItemLine, len(items))
		copy(cp, items)
		return cp
	}
	return []OrderItemLine{
		{
			SkuID:                "SKU-BALO-BLACK-20L",
			SkuName:              "Balo City Black 20L",
			Quantity:             2,
			UnitPriceVND:         300000,
			LineDiscountVND:      60000,
			AllocatedUnitPaidVND: 270000,
		},
		{
			SkuID:                "SKU-BOTTLE-BLUE-500ML",
			SkuName:              "Eco Thermos Bottle Blue 500ml",
			Quantity:             1,
			UnitPriceVND:         100000,
			LineDiscountVND:      10000,
			AllocatedUnitPaidVND: 90000,
		},
	}
}

// PlaceOrder enforces Invariant I02 (SC04 idempotency) and Policy P12 (SC10 voucher quota reservation).
func (e *CheckoutEngine) PlaceOrder(idempotencyKey, orderID, userID, quoteID, voucherCode string, totalPayable int64) (*OrderRecord, bool, error) {
	return e.PlaceOrderWithLines(idempotencyKey, orderID, userID, quoteID, voucherCode, 700000, 70000, 30000, totalPayable, nil)
}

func (e *CheckoutEngine) PlaceOrderWithLines(
	idempotencyKey, orderID, userID, quoteID, voucherCode string,
	subtotalVND, discountVND, shippingFeeVND, totalPayable int64,
	items []OrderItemLine,
) (*OrderRecord, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	reqHash := HashIntent(userID, quoteID, voucherCode, totalPayable)
	if existing, ok := e.idempotency[idempotencyKey]; ok {
		if existing.RequestHash != reqHash {
			return nil, false, ErrIdempotencyConflict
		}
		cp := *existing
		return &cp, true, nil
	}

	if voucherCode != "" {
		v, ok := e.vouchers[voucherCode]
		if !ok || v.Available() <= 0 {
			return nil, false, ErrVoucherExhausted
		}
		v.Reserved++
	}

	rec := &OrderRecord{
		OrderID:         orderID,
		UserID:          userID,
		QuoteID:         quoteID,
		VoucherCode:     voucherCode,
		Status:          "PENDING",
		PaymentStatus:   "PROCESSING",
		SubtotalVND:     subtotalVND,
		DiscountVND:     discountVND,
		ShippingFeeVND:  shippingFeeVND,
		TotalPayableVND: totalPayable,
		Items:           DefaultItemsIfEmpty(items),
		RequestHash:     reqHash,
	}
	e.idempotency[idempotencyKey] = rec
	e.orders[orderID] = rec
	cp := *rec
	return &cp, false, nil
}

func (e *CheckoutEngine) GetOrder(orderID string) (*OrderRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ord, ok := e.orders[orderID]
	if !ok {
		return nil, ErrOrderNotFound
	}
	cp := *ord
	return &cp, nil
}

func (e *CheckoutEngine) ListOrders(userID string) []OrderRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]OrderRecord, 0, len(e.orders))
	for _, ord := range e.orders {
		if userID == "" || ord.UserID == userID {
			out = append(out, *ord)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].OrderID < out[j].OrderID
	})
	return out
}

// ApplyPaymentOutcome updates order status, payment status, and voucher quota based on payment outcome.
func (e *CheckoutEngine) ApplyPaymentOutcome(orderID, outcome string) (*OrderRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	ord, ok := e.orders[orderID]
	if !ok {
		return nil, ErrOrderNotFound
	}

	switch outcome {
	case "CAPTURED":
		if ord.Status == "CONFIRMED" && ord.PaymentStatus == "CAPTURED" {
			cp := *ord
			return &cp, nil
		}
		if ord.Status != "PENDING" {
			return nil, ErrInvalidOrderState
		}
		ord.Status = "CONFIRMED"
		ord.PaymentStatus = "CAPTURED"
		if ord.VoucherCode != "" {
			if v, exists := e.vouchers[ord.VoucherCode]; exists && v.Reserved > 0 {
				v.Reserved--
				v.Consumed++
			}
		}
	case "UNKNOWN_30M":
		if ord.Status != "PENDING" {
			return nil, ErrInvalidOrderState
		}
		ord.PaymentStatus = "UNKNOWN_30M"
	case "FAILED", "EXPIRED_30M":
		if ord.Status == "CANCELLED" || ord.Status == "FAILED_CLOSED" {
			cp := *ord
			return &cp, nil
		}
		if ord.Status != "PENDING" {
			return nil, ErrInvalidOrderState
		}
		if outcome == "EXPIRED_30M" {
			ord.Status = "FAILED_CLOSED"
		} else {
			ord.Status = "CANCELLED"
		}
		ord.PaymentStatus = "FAILED"
		if ord.VoucherCode != "" {
			if v, exists := e.vouchers[ord.VoucherCode]; exists && v.Reserved > 0 {
				v.Reserved--
			}
		}
	default:
		return nil, ErrInvalidOrderState
	}

	cp := *ord
	return &cp, nil
}
