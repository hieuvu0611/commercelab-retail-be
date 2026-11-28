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
	queryStuckPendingOrders = `SELECT COUNT(*) FROM orders.orders WHERE status IN ('PENDING', 'RESERVED', 'PENDING_PAYMENT') AND created_at < NOW() - INTERVAL '10 minutes'`
	queryOrderOutboxBacklog = `SELECT COUNT(*), COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(created_at))), 0) FROM orders.outbox_events WHERE published_at IS NULL`
)

func registerOrderSLOMetrics(reg *telemetry.Registry, db sloDB) {
	if reg == nil {
		return
	}
	reg.RegisterCollector(func(ctx context.Context) []telemetry.GaugeSample {
		if db == nil {
			return []telemetry.GaugeSample{
				{
					Name:  "commercelab_stuck_pending_orders",
					Help:  "Orders stuck in pending payment state longer than 10 minutes.",
					Value: 0,
				},
				{
					Name:   "commercelab_outbox_pending_rows",
					Help:   "Unpublished outbox rows awaiting relay.",
					Labels: map[string]string{"service": "order-checkout-service"},
					Value:  0,
				},
				{
					Name:   "commercelab_outbox_oldest_age_seconds",
					Help:   "Age in seconds of the oldest unpublished outbox row.",
					Labels: map[string]string{"service": "order-checkout-service"},
					Value:  0,
				},
			}
		}

		var samples []telemetry.GaugeSample

		var stuckCount float64
		if err := db.QueryRow(ctx, queryStuckPendingOrders).Scan(&stuckCount); err == nil {
			samples = append(samples, telemetry.GaugeSample{
				Name:  "commercelab_stuck_pending_orders",
				Help:  "Orders stuck in pending payment state longer than 10 minutes.",
				Value: stuckCount,
			})
		}

		var pendingRows float64
		var oldestAgeSeconds float64
		if err := db.QueryRow(ctx, queryOrderOutboxBacklog).Scan(&pendingRows, &oldestAgeSeconds); err == nil {
			samples = append(samples,
				telemetry.GaugeSample{
					Name:   "commercelab_outbox_pending_rows",
					Help:   "Unpublished outbox rows awaiting relay.",
					Labels: map[string]string{"service": "order-checkout-service"},
					Value:  pendingRows,
				},
				telemetry.GaugeSample{
					Name:   "commercelab_outbox_oldest_age_seconds",
					Help:   "Age in seconds of the oldest unpublished outbox row.",
					Labels: map[string]string{"service": "order-checkout-service"},
					Value:  oldestAgeSeconds,
				},
			)
		}

		return samples
	})
}
