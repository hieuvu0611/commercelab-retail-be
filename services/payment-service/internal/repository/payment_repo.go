package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type PaymentRepository struct {
	pg       *pgxpool.Pool
	mongo    *db.MongoStore
	fallback *domain.PaymentEngine
}

func NewPaymentRepository(pg *pgxpool.Pool, mongo *db.MongoStore, fallback *domain.PaymentEngine) *PaymentRepository {
	return &PaymentRepository{
		pg:       pg,
		mongo:    mongo,
		fallback: fallback,
	}
}

func (r *PaymentRepository) CreateAttempt(ctx context.Context, attemptID, orderID, idempotencyKey string, amountVND int64) (*domain.PaymentAttempt, error) {
	if r.pg == nil {
		return r.fallback.CreateAttempt(attemptID, orderID, idempotencyKey, amountVND)
	}

	tx, err := r.pg.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return r.fallback.CreateAttempt(attemptID, orderID, idempotencyKey, amountVND)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Check idempotency key first
	var existing domain.PaymentAttempt
	err = tx.QueryRow(ctx, `
		SELECT attempt_id, order_id, idempotency_key, amount_vnd, status, unknown_deadline, created_at
		FROM payments.payment_attempts
		WHERE idempotency_key = $1
	`, idempotencyKey).Scan(
		&existing.AttemptID, &existing.OrderID, &existing.IdempotencyKey,
		&existing.AmountVND, &existing.Status, &existing.UnknownDeadline, &existing.CreatedAt,
	)
	if err == nil {
		if existing.OrderID == orderID && existing.AmountVND == amountVND {
			_ = tx.Commit(ctx)
			return &existing, nil
		}
		return nil, domain.ErrActivePaymentInProgress
	}

	// 2. Enforce at most one active PROCESSING or UNKNOWN attempt per order (SC05)
	var activeCount int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM payments.payment_attempts
		WHERE order_id = $1 AND status IN ('PROCESSING', 'UNKNOWN')
	`, orderID).Scan(&activeCount); err == nil && activeCount > 0 {
		return nil, domain.ErrActivePaymentInProgress
	}

	if attemptID == "" {
		attemptID = fmt.Sprintf("PAY-%s-%d", orderID, time.Now().UTC().UnixNano()%100000)
	}
	now := time.Now().UTC()

	_, err = tx.Exec(ctx, `
		INSERT INTO payments.payment_attempts (attempt_id, order_id, idempotency_key, amount_vnd, status, created_at)
		VALUES ($1, $2, $3, $4, 'PROCESSING', $5)
	`, attemptID, orderID, idempotencyKey, amountVND, now)
	if err != nil {
		return nil, domain.ErrActivePaymentInProgress
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO payments.order_payment_summary (order_id, expected_amount, captured_amount, refunded_amount, refund_pending, version)
		VALUES ($1, $2, 0, 0, 0, 1)
		ON CONFLICT (order_id) DO NOTHING
	`, orderID, amountVND)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &domain.PaymentAttempt{
		AttemptID:      attemptID,
		OrderID:        orderID,
		IdempotencyKey: idempotencyKey,
		AmountVND:      amountVND,
		Status:         "PROCESSING",
		CreatedAt:      now,
	}, nil
}

func (r *PaymentRepository) ProcessWebhook(ctx context.Context, orderID, attemptID, outcome string, amountVND int64, rawPayload map[string]any) (*domain.PaymentSummary, *domain.PaymentAttempt, bool, error) {
	if r.mongo != nil {
		doc := bson.M{
			"orderId":    orderID,
			"attemptId":  attemptID,
			"outcome":    outcome,
			"amountVnd":  amountVND,
			"rawPayload": rawPayload,
			"receivedAt": time.Now().UTC(),
		}
		_ = r.mongo.InsertOne(ctx, "payment_raw_webhooks", doc)
	}

	if r.pg == nil {
		return r.fallback.ProcessWebhook(orderID, attemptID, outcome, amountVND)
	}

	tx, err := r.pg.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return r.fallback.ProcessWebhook(orderID, attemptID, outcome, amountVND)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var att domain.PaymentAttempt
	err = tx.QueryRow(ctx, `
		SELECT attempt_id, order_id, idempotency_key, amount_vnd, status, unknown_deadline, created_at
		FROM payments.payment_attempts
		WHERE order_id = $1
		ORDER BY created_at DESC LIMIT 1
		FOR UPDATE
	`, orderID).Scan(
		&att.AttemptID, &att.OrderID, &att.IdempotencyKey, &att.AmountVND, &att.Status, &att.UnknownDeadline, &att.CreatedAt,
	)
	if err != nil {
		return r.fallback.ProcessWebhook(orderID, attemptID, outcome, amountVND)
	}

	if amountVND <= 0 {
		amountVND = att.AmountVND
	}

	autoRefundRequired := false
	switch outcome {
	case "CAPTURED":
		if att.Status == "EXPIRED_30M" {
			att.Status = "CAPTURED_LATE_REFUND"
			autoRefundRequired = true
			_, err = tx.Exec(ctx, `
				UPDATE payments.order_payment_summary
				SET captured_amount = captured_amount + $2,
				    refund_pending = refund_pending + $2,
				    version = version + 1
				WHERE order_id = $1
			`, orderID, amountVND)
		} else {
			att.Status = "CAPTURED"
			_, err = tx.Exec(ctx, `
				UPDATE payments.order_payment_summary
				SET captured_amount = $2,
				    version = version + 1
				WHERE order_id = $1
			`, orderID, amountVND)
		}
	case "FAILED":
		att.Status = "FAILED"
	case "UNKNOWN":
		deadline := time.Now().UTC().Add(30 * time.Minute)
		att.Status = "UNKNOWN"
		att.UnknownDeadline = &deadline
	case "EXPIRE_30M", "EXPIRED_30M":
		att.Status = "EXPIRED_30M"
	default:
		return nil, nil, false, fmt.Errorf("unsupported payment webhook outcome: %s", outcome)
	}
	if err != nil {
		return nil, nil, false, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE payments.payment_attempts
		SET status = $2, unknown_deadline = $3
		WHERE attempt_id = $1
	`, att.AttemptID, att.Status, att.UnknownDeadline)
	if err != nil {
		return nil, nil, false, err
	}

	var sum domain.PaymentSummary
	err = tx.QueryRow(ctx, `
		SELECT order_id, expected_amount, captured_amount, refunded_amount, refund_pending
		FROM payments.order_payment_summary
		WHERE order_id = $1
	`, orderID).Scan(&sum.OrderID, &sum.ExpectedAmount, &sum.CapturedAmount, &sum.RefundedAmount, &sum.RefundPending)
	if err != nil {
		return nil, nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, false, err
	}
	return &sum, &att, autoRefundRequired, nil
}

func (r *PaymentRepository) GetOrderPayment(ctx context.Context, orderID string) (*domain.PaymentSummary, *domain.PaymentAttempt, error) {
	if r.pg == nil {
		return r.fallback.GetOrderPayment(orderID)
	}

	var sum domain.PaymentSummary
	err := r.pg.QueryRow(ctx, `
		SELECT order_id, expected_amount, captured_amount, refunded_amount, refund_pending
		FROM payments.order_payment_summary
		WHERE order_id = $1
	`, orderID).Scan(&sum.OrderID, &sum.ExpectedAmount, &sum.CapturedAmount, &sum.RefundedAmount, &sum.RefundPending)
	if err != nil {
		return r.fallback.GetOrderPayment(orderID)
	}

	var att domain.PaymentAttempt
	err = r.pg.QueryRow(ctx, `
		SELECT attempt_id, order_id, idempotency_key, amount_vnd, status, unknown_deadline, created_at
		FROM payments.payment_attempts
		WHERE order_id = $1
		ORDER BY created_at DESC LIMIT 1
	`, orderID).Scan(&att.AttemptID, &att.OrderID, &att.IdempotencyKey, &att.AmountVND, &att.Status, &att.UnknownDeadline, &att.CreatedAt)
	if err != nil {
		return &sum, nil, nil
	}
	return &sum, &att, nil
}

func (r *PaymentRepository) ReserveRefundCeiling(ctx context.Context, orderID string, amount int64) (*domain.PaymentSummary, error) {
	if r.pg == nil {
		return r.fallback.ReserveRefundCeiling(orderID, amount)
	}

	var sum domain.PaymentSummary
	err := r.pg.QueryRow(ctx, `
		UPDATE payments.order_payment_summary
		SET refund_pending = refund_pending + $2,
		    version = version + 1
		WHERE order_id = $1
		  AND $2 > 0
		  AND captured_amount >= (refunded_amount + refund_pending + $2)
		RETURNING order_id, expected_amount, captured_amount, refunded_amount, refund_pending
	`, orderID, amount).Scan(&sum.OrderID, &sum.ExpectedAmount, &sum.CapturedAmount, &sum.RefundedAmount, &sum.RefundPending)
	if err != nil {
		return nil, domain.ErrRefundCeilingExceeded
	}
	return &sum, nil
}

func (r *PaymentRepository) CompleteRefund(ctx context.Context, orderID string, amount int64) (*domain.PaymentSummary, error) {
	if r.pg == nil {
		return r.fallback.CompleteRefund(orderID, amount)
	}

	var sum domain.PaymentSummary
	err := r.pg.QueryRow(ctx, `
		UPDATE payments.order_payment_summary
		SET refund_pending = refund_pending - $2,
		    refunded_amount = refunded_amount + $2,
		    version = version + 1
		WHERE order_id = $1
		  AND $2 > 0
		  AND refund_pending >= $2
		RETURNING order_id, expected_amount, captured_amount, refunded_amount, refund_pending
	`, orderID, amount).Scan(&sum.OrderID, &sum.ExpectedAmount, &sum.CapturedAmount, &sum.RefundedAmount, &sum.RefundPending)
	if err != nil {
		return nil, domain.ErrInvalidPendingRefund
	}
	return &sum, nil
}
