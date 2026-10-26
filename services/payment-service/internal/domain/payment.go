package domain

import (
	"errors"
	"sync"
)

var (
	ErrRefundCeilingExceeded = errors.New("vượt trần hoàn tiền cho phép: Đã hoàn + Đang hoàn không được vượt Thực thu (I03, SC08)")
)

// PaymentSummary bảo vệ bất biến I03 & kịch bản SC08
type PaymentSummary struct {
	OrderID        string `json:"orderId"`
	CapturedAmount int64  `json:"capturedAmount"` // Thực thu (VD: 660.000)
	RefundedAmount int64  `json:"refundedAmount"` // Đã hoàn thành công (VD: 270.000)
	RefundPending  int64  `json:"refundPending"`  // Đang giữ chờ hoàn (VD: 300.000)
}

func (p PaymentSummary) RemainingRefundable() int64 {
	return p.CapturedAmount - p.RefundedAmount - p.RefundPending
}

type PaymentEngine struct {
	mu        sync.Mutex
	summaries map[string]*PaymentSummary
}

func NewPaymentEngine() *PaymentEngine {
	return &PaymentEngine{
		summaries: map[string]*PaymentSummary{
			"O-001": {OrderID: "O-001", CapturedAmount: 660000, RefundedAmount: 0, RefundPending: 0},
		},
	}
}

// ReserveRefundCeiling giữ chỗ trong trần hoàn tiền trước khi gọi đối tác (I03, SC08)
func (e *PaymentEngine) ReserveRefundCeiling(orderID string, amount int64) (*PaymentSummary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.summaries[orderID]
	if !ok || amount <= 0 || s.RemainingRefundable() < amount {
		return nil, ErrRefundCeilingExceeded
	}
	s.RefundPending += amount
	cp := *s
	return &cp, nil
}

// CompleteRefund chuyển từ Đang hoàn sang Đã hoàn (không làm tăng hạn mức còn lại)
func (e *PaymentEngine) CompleteRefund(orderID string, amount int64) (*PaymentSummary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.summaries[orderID]
	if !ok || s.RefundPending < amount {
		return nil, errors.New("khoản hoàn đang chờ không hợp lệ")
	}
	s.RefundPending -= amount
	s.RefundedAmount += amount
	cp := *s
	return &cp, nil
}
