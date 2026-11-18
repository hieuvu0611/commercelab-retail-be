package repository

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ErrReservationExpired means the 10-minute hold ran out before the exception hold was requested.
var ErrReservationExpired = errors.New("the reservation has already expired")

const (
	// ExpirySweepActor is the actor of RELEASE movements written by the expiry worker.
	ExpirySweepActor = "system:expiry-worker"
	sweepBatchSize   = 100
)

// releaseHeldStock gives the reserved quantities of rec back to available, writes the RELEASE movements and marks
// the reservation RELEASED, all inside tx. The caller has locked the reservation row.
func releaseHeldStock(ctx context.Context, tx pgx.Tx, rec *ReservationRecord, actor, reason string) error {
	for _, line := range rec.Lines {
		if _, err := tx.Exec(ctx, `
			UPDATE stock_items
			SET reserved = reserved - $1, version = version + 1, updated_at = NOW()
			WHERE sku_id = $2 AND warehouse_id = $3
		`, line.Quantity, line.SkuID, rec.WarehouseID); err != nil {
			return dependency.Wrap(err)
		}
		m := lineMovement(domain.MovementRelease, rec.WarehouseID, rec.OrderID, line, reason)
		m.ActorID = actor
		if err := insertMovement(ctx, tx, m); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE reservations SET status = 'RELEASED', version = version + 1 WHERE reservation_id = $1`, rec.ReservationID); err != nil {
		return dependency.Wrap(err)
	}
	rec.Status = "RELEASED"
	return nil
}

// PlaceExceptionHold moves a reservation from ACTIVE_10M to EXCEPTION_HOLD_30M so the expiry worker leaves it alone
// while payment is unknown (P05, SC05). The hold lasts 30 minutes from the reservation, not from this call.
// Repeating it is harmless; an expired, released or committed reservation cannot be held.
func (r *InventoryRepository) PlaceExceptionHold(ctx context.Context, orderID string) (*ReservationRecord, error) {
	now := r.now().UTC()
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		recs, ok := r.reservations[orderID]
		if !ok || len(recs) == 0 {
			return nil, ErrReservationNotFound
		}
		allHeld := true
		for _, rec := range recs {
			if rec.Status != statusException {
				allHeld = false
				break
			}
		}
		if allHeld {
			cp := *recs[0]
			return &cp, nil
		}
		for _, rec := range recs {
			if rec.Status == statusException {
				continue
			}
			if rec.Status != statusActive {
				return nil, domain.ErrInvalidReservationState
			}
			if !now.Before(rec.ExpiresAt) {
				return nil, ErrReservationExpired
			}
		}
		for _, rec := range recs {
			if rec.Status == statusActive {
				rec.Status = statusException
				rec.ExpiresAt = rec.ExpiresAt.Add(ExceptionHoldTTL - ReservationTTL)
			}
		}
		cp := *recs[0]
		return &cp, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recs, err := loadReservationsTx(ctx, tx, orderID, "")
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrReservationNotFound
	}
	allHeld := true
	for _, rec := range recs {
		if rec.Status != statusException {
			allHeld = false
			break
		}
	}
	if allHeld {
		return &recs[0], nil
	}
	for _, rec := range recs {
		if rec.Status == statusException {
			continue
		}
		if rec.Status != statusActive {
			return nil, domain.ErrInvalidReservationState
		}
		if !now.Before(rec.ExpiresAt) {
			return nil, ErrReservationExpired
		}
	}
	for i := range recs {
		rec := &recs[i]
		if rec.Status == statusActive {
			rec.ExpiresAt = rec.ExpiresAt.Add(ExceptionHoldTTL - ReservationTTL)
			if _, err := tx.Exec(ctx, `
				UPDATE reservations SET status = 'EXCEPTION_HOLD_30M', expires_at = $2, version = version + 1 WHERE reservation_id = $1
			`, rec.ReservationID, rec.ExpiresAt); err != nil {
				return nil, dependency.Wrap(err)
			}
			rec.Status = statusException
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dependency.Wrap(err)
	}
	return &recs[0], nil
}

// ExpireDueReservations releases every ACTIVE_10M reservation whose expires_at has passed and returns how many it
// released. Exception holds are never touched: their controlled release is an explicit call (SC05).
func (r *InventoryRepository) ExpireDueReservations(ctx context.Context) (int, error) {
	now := r.now().UTC()
	if r.pool == nil {
		return r.expireInMemory(now), nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT reservation_id FROM reservations WHERE status = 'ACTIVE_10M' AND expires_at <= $1
		ORDER BY expires_at LIMIT $2
	`, now, sweepBatchSize)
	if err != nil {
		return 0, dependency.Wrap(err)
	}
	var due []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, dependency.Wrap(err)
		}
		due = append(due, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, dependency.Wrap(err)
	}
	released := 0
	for _, resID := range due {
		ok, err := r.expireOne(ctx, resID, now)
		if err != nil {
			return released, err
		}
		if ok {
			released++
		}
	}
	return released, nil
}

func (r *InventoryRepository) expireOne(ctx context.Context, reservationID string, now time.Time) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var rec ReservationRecord
	var rawLines []byte
	err = tx.QueryRow(ctx, `
		SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
		FROM reservations
		WHERE reservation_id = $1 AND status = 'ACTIVE_10M' AND expires_at <= $2
		FOR UPDATE SKIP LOCKED
	`, reservationID, now).Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dependency.Wrap(err)
	}
	_ = json.Unmarshal(rawLines, &rec.Lines)
	if err := releaseHeldStock(ctx, tx, &rec, ExpirySweepActor, "reservation expired"); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, dependency.Wrap(err)
	}
	return true, nil
}

func (r *InventoryRepository) expireInMemory(now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	released := 0
	for orderID, recs := range r.reservations {
		for _, rec := range recs {
			if rec.Status != statusActive || now.Before(rec.ExpiresAt) {
				continue
			}
			if err := r.engine.ReleaseReservation(rec.WarehouseID, rec.Lines); err != nil {
				continue
			}
			for _, line := range rec.Lines {
				m := lineMovement(domain.MovementRelease, rec.WarehouseID, orderID, line, "reservation expired")
				m.ActorID = ExpirySweepActor
				r.appendMovement(m)
			}
			rec.Status = "RELEASED"
			released++
		}
	}
	return released
}

// RunExpiryWorker sweeps expired reservations every interval until ctx is cancelled.
func (r *InventoryRepository) RunExpiryWorker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			released, err := r.ExpireDueReservations(ctx)
			if err != nil {
				log.Printf("[inventory-warehouse-service] expiry sweep failed after releasing %d: %v", released, err)
			} else if released > 0 {
				log.Printf("[inventory-warehouse-service] expiry sweep released %d reservation(s)", released)
			}
		}
	}
}
