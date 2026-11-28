package main

import (
	"context"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

type rowScanner interface {
	Scan(dest ...any) error
}

type sloDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) rowScanner
}

type pgxSLODB struct {
	pool *pgxpool.Pool
}

func newPGXSLODB(pool *pgxpool.Pool) sloDB {
	if pool == nil {
		return nil
	}
	return pgxSLODB{pool: pool}
}

func (p pgxSLODB) QueryRow(ctx context.Context, sql string, args ...any) rowScanner {
	return p.pool.QueryRow(ctx, sql, args...)
}

const (
	queryUnknownPaymentAttempts  = `SELECT COUNT(*) FROM payments.payment_attempts WHERE status = 'UNKNOWN'`
	queryPendingRefundObligation = `SELECT COUNT(*) FROM payments.refund_obligations WHERE status = 'PENDING'`
	queryPaymentOutboxBacklog    = `SELECT COUNT(*), COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(created_at))), 0) FROM payments.outbox_events WHERE published_at IS NULL`
)

func registerPaymentSLOMetrics(reg *telemetry.Registry, db sloDB) {
	if reg == nil {
		return
	}
	reg.RegisterCollector(func(ctx context.Context) []telemetry.GaugeSample {
		if db == nil {
			return []telemetry.GaugeSample{
				{
					Name:  "commercelab_unknown_payment_attempts",
					Help:  "Payment attempts currently in UNKNOWN outcome state.",
					Value: 0,
				},
				{
					Name:  "commercelab_pending_refund_obligations",
					Help:  "Pending refund obligations awaiting disbursement.",
					Value: 0,
				},
				{
					Name:   "commercelab_outbox_pending_rows",
					Help:   "Unpublished outbox rows awaiting relay.",
					Labels: map[string]string{"service": "payment-service"},
					Value:  0,
				},
				{
					Name:   "commercelab_outbox_oldest_age_seconds",
					Help:   "Age in seconds of the oldest unpublished outbox row.",
					Labels: map[string]string{"service": "payment-service"},
					Value:  0,
				},
			}
		}

		var samples []telemetry.GaugeSample

		var unknownCount float64
		if err := db.QueryRow(ctx, queryUnknownPaymentAttempts).Scan(&unknownCount); err == nil {
			samples = append(samples, telemetry.GaugeSample{
				Name:  "commercelab_unknown_payment_attempts",
				Help:  "Payment attempts currently in UNKNOWN outcome state.",
				Value: unknownCount,
			})
		}

		var pendingRefunds float64
		if err := db.QueryRow(ctx, queryPendingRefundObligation).Scan(&pendingRefunds); err == nil {
			samples = append(samples, telemetry.GaugeSample{
				Name:  "commercelab_pending_refund_obligations",
				Help:  "Pending refund obligations awaiting disbursement.",
				Value: pendingRefunds,
			})
		}

		var pendingRows float64
		var oldestAgeSeconds float64
		if err := db.QueryRow(ctx, queryPaymentOutboxBacklog).Scan(&pendingRows, &oldestAgeSeconds); err == nil {
			samples = append(samples,
				telemetry.GaugeSample{
					Name:   "commercelab_outbox_pending_rows",
					Help:   "Unpublished outbox rows awaiting relay.",
					Labels: map[string]string{"service": "payment-service"},
					Value:  pendingRows,
				},
				telemetry.GaugeSample{
					Name:   "commercelab_outbox_oldest_age_seconds",
					Help:   "Age in seconds of the oldest unpublished outbox row.",
					Labels: map[string]string{"service": "payment-service"},
					Value:  oldestAgeSeconds,
				},
			)
		}

		return samples
	})
}
