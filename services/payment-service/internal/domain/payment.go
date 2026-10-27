package domain

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrRefundCeilingExceeded   = errors.New("refund ceiling exceeded: refunded + pending refund cannot exceed captured amount (I03, SC08)")
	ErrInvalidPendingRefund    = errors.New("invalid pending refund amount for completion")
	ErrActivePaymentInProgress = errors.New("an active payment attempt (PROCESSING or UNKNOWN) is already in progress for this order (SC05)")
	ErrPaymentNotFound         = errors.New("payment record not found for order")
)

// PaymentSummary enforces invariant I03 and scenario SC08.
type PaymentSummary struct {
	OrderID        string `json:"orderId"`
	ExpectedAmount int64  `json:"expectedAmount"`
	CapturedAmount int64  `json:"capturedAmount"`
	RefundedAmount int64  `json:"refundedAmount"`
	RefundPending  int64  `json:"refundPending"`
}

func (p PaymentSummary) RemainingRefundable() int64 {
	return p.CapturedAmount - p.RefundedAmount - p.RefundPending
}

// PaymentAttempt tracks an individual payment gateway transaction (B03, P05, SC05).
type PaymentAttempt struct {
	AttemptID       string     `json:"attemptId"`
	OrderID         string     `json:"orderId"`
	IdempotencyKey  string     `json:"idempotencyKey"`
	AmountVND       int64      `json:"amountVnd"`
	Status          string     `json:"status"` // PROCESSING, UNKNOWN, CAPTURED, FAILED, EXPIRED_30M, CAPTURED_LATE_REFUND
	UnknownDeadline *time.Time `json:"unknownDeadline,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type PaymentEngine struct {
	mu        sync.Mutex
	summaries map[string]*PaymentSummary
	attempts  map[string]*PaymentAttempt // keyed by orderID (latest attempt)
	byIdemKey map[string]*PaymentAttempt // keyed by idempotencyKey
}

func NewPaymentEngine() *PaymentEngine {
	return &PaymentEngine{
		summaries: map[string]*PaymentSummary{
			"O-001": {OrderID: "O-001", ExpectedAmount: 660000, CapturedAmount: 660000, RefundedAmount: 0, RefundPending: 0},
		},
		attempts:  make(map[string]*PaymentAttempt),
		byIdemKey: make(map[string]*PaymentAttempt),
	}
}

// CreateAttempt initiates a payment attempt while preventing duplicate active charges (SC05).
func (e *PaymentEngine) CreateAttempt(attemptID, orderID, idempotencyKey string, amountVND int64) (*PaymentAttempt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if orderID == "" || idempotencyKey == "" || amountVND <= 0 {
		return nil, errors.New("orderId, idempotencyKey, and positive amountVnd are required")
	}

	if existing, ok := e.byIdemKey[idempotencyKey]; ok {
		if existing.OrderID == orderID && existing.AmountVND == amountVND {
			cp := *existing
			return &cp, nil
		}
		return nil, ErrActivePaymentInProgress
	}

	if active, ok := e.attempts[orderID]; ok {
		if active.Status == "PROCESSING" || active.Status == "UNKNOWN" {
			return nil, ErrActivePaymentInProgress
		}
	}

	if attemptID == "" {
		attemptID = fmt.Sprintf("PAY-%s-%d", orderID, len(e.byIdemKey)+1)
	}

	now := time.Now().UTC()
	attempt := &PaymentAttempt{
		AttemptID:      attemptID,
		OrderID:        orderID,
		IdempotencyKey: idempotencyKey,
		AmountVND:      amountVND,
		Status:         "PROCESSING",
		CreatedAt:      now,
	}
	e.attempts[orderID] = attempt
	e.byIdemKey[idempotencyKey] = attempt

	if _, exists := e.summaries[orderID]; !exists {
		e.summaries[orderID] = &PaymentSummary{
			OrderID:        orderID,
			ExpectedAmount: amountVND,
		}
	}
	cp := *attempt
	return &cp, nil
}

// ProcessWebhook handles gateway outcomes including 30m UNKNOWN hold and minute-40 late capture (SC05).
func (e *PaymentEngine) ProcessWebhook(orderID, attemptID, outcome string, amountVND int64) (*PaymentSummary, *PaymentAttempt, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	summary, ok := e.summaries[orderID]
	if !ok {
		summary = &PaymentSummary{
			OrderID:        orderID,
			ExpectedAmount: amountVND,
		}
		e.summaries[orderID] = summary
	}

	attempt, hasAttempt := e.attempts[orderID]
	if !hasAttempt {
		if attemptID == "" {
			attemptID = "PAY-" + orderID
		}
		attempt = &PaymentAttempt{
			AttemptID:      attemptID,
			OrderID:        orderID,
			IdempotencyKey: "WEBHOOK-" + attemptID,
			AmountVND:      amountVND,
			Status:         "PROCESSING",
			CreatedAt:      time.Now().UTC(),
		}
		e.attempts[orderID] = attempt
	}

	if amountVND <= 0 {
		amountVND = attempt.AmountVND
	}
	if amountVND <= 0 {
		amountVND = summary.ExpectedAmount
	}

	autoRefundRequired := false
	switch outcome {
	case "CAPTURED":
		if attempt.Status == "EXPIRED_30M" {
			// SC05: Late payment arrived at minute 40 after the 30-minute hold expired.
			// Do not reopen the closed order; record captured funds and lock refund_pending for automatic refund.
			attempt.Status = "CAPTURED_LATE_REFUND"
			summary.CapturedAmount += amountVND
			summary.RefundPending += amountVND
			autoRefundRequired = true
		} else {
			attempt.Status = "CAPTURED"
			summary.CapturedAmount = amountVND
		}
	case "FAILED":
		attempt.Status = "FAILED"
	case "UNKNOWN":
		deadline := time.Now().UTC().Add(30 * time.Minute)
		attempt.Status = "UNKNOWN"
		attempt.UnknownDeadline = &deadline
	case "EXPIRE_30M", "EXPIRED_30M":
		attempt.Status = "EXPIRED_30M"
	default:
		return nil, nil, false, fmt.Errorf("unsupported payment webhook outcome: %s", outcome)
	}

	sumCopy := *summary
	attCopy := *attempt
	return &sumCopy, &attCopy, autoRefundRequired, nil
}

// GetOrderPayment returns the current payment summary and latest attempt for an order.
func (e *PaymentEngine) GetOrderPayment(orderID string) (*PaymentSummary, *PaymentAttempt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	summary, ok := e.summaries[orderID]
	if !ok {
		return nil, nil, ErrPaymentNotFound
	}
	sumCopy := *summary
	var attCopy *PaymentAttempt
	if att, has := e.attempts[orderID]; has {
		cp := *att
		attCopy = &cp
	}
	return &sumCopy, attCopy, nil
}

// ReserveRefundCeiling locks a refund amount against the captured ceiling before gateway payout (I03, SC08).
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

// CompleteRefund transitions a pending refund to refunded without increasing remaining refundable ceiling.
func (e *PaymentEngine) CompleteRefund(orderID string, amount int64) (*PaymentSummary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.summaries[orderID]
	if !ok || amount <= 0 || s.RefundPending < amount {
		return nil, ErrInvalidPendingRefund
	}
	s.RefundPending -= amount
	s.RefundedAmount += amount
	cp := *s
	return &cp, nil
}
