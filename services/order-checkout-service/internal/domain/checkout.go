package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

var (
	ErrIdempotencyConflict = errors.New("cùng Idempotency-Key nhưng nội dung giỏ hàng đã thay đổi (I02, SC04)")
	ErrVoucherExhausted    = errors.New("voucher đã hết lượt sử dụng (P12, SC10)")
)

// VoucherQuota quản lý 3 rổ quota theo P12: Còn dùng, Đang giữ, Đã tiêu thụ
type VoucherQuota struct {
	Code     string `json:"code"`
	Total    int64  `json:"total"`
	Reserved int64  `json:"reserved"`
	Consumed int64  `json:"consumed"`
}

func (v VoucherQuota) Available() int64 {
	return v.Total - v.Reserved - v.Consumed
}

type OrderRecord struct {
	OrderID         string `json:"orderId"`
	UserID          string `json:"userId"`
	QuoteID         string `json:"quoteId"`
	VoucherCode     string `json:"voucherCode,omitempty"`
	Status          string `json:"status"`
	PaymentStatus   string `json:"paymentStatus"`
	TotalPayableVND int64  `json:"totalPayableVnd"`
	RequestHash     string `json:"-"`
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

// PlaceOrder thực thi I02 (SC04 chống bấm đúp) và P12 (SC10 giữ quota voucher)
func (e *CheckoutEngine) PlaceOrder(idempotencyKey, orderID, userID, quoteID, voucherCode string, totalPayable int64) (*OrderRecord, bool, error) {
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
		TotalPayableVND: totalPayable,
		RequestHash:     reqHash,
	}
	e.idempotency[idempotencyKey] = rec
	e.orders[orderID] = rec
	cp := *rec
	return &cp, false, nil
}
