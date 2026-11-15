package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ReleaseCommittedReservation gives the committed stock of a paid order back to available because the order was
// cancelled before the parcel left the warehouse (B05). It is the counterpart of HandoverOrderShipment: both
// need a COMMITTED reservation and change it under the same row lock, so exactly one of them can win (SC06).
// Repeating it after it worked changes nothing; a reservation that was handed over (or never committed) is
// domain.ErrInvalidReservationState and nothing changes.
func (r *InventoryRepository) ReleaseCommittedReservation(ctx context.Context, orderID, actor string) (*ReservationRecord, error) {
	if r.pool == nil {
		return r.releaseCommittedInMemory(orderID, actor)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var rec ReservationRecord
	var rawLines []byte
	err = tx.QueryRow(ctx, `
		SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
		FROM reservations WHERE order_id = $1 FOR UPDATE
	`, orderID).Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReservationNotFound
	}
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	_ = json.Unmarshal(rawLines, &rec.Lines)
	if rec.Status == "RELEASED" {
		return &rec, nil
	}
	if rec.Status != "COMMITTED" {
		return nil, domain.ErrInvalidReservationState
	}
	for _, line := range rec.Lines {
		if _, err := tx.Exec(ctx, `
			UPDATE stock_items SET committed = committed - $1, version = version + 1, updated_at = NOW()
			WHERE sku_id = $2 AND warehouse_id = $3
		`, line.Quantity, line.SkuID, rec.WarehouseID); err != nil {
			return nil, dependency.Wrap(err)
		}
		m := lineMovement(domain.MovementRelease, rec.WarehouseID, orderID, line, "paid order cancelled before handover")
		m.ActorID = actor
		if err := insertMovement(ctx, tx, m); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE reservations SET status = 'RELEASED', version = version + 1 WHERE order_id = $1`, orderID); err != nil {
		return nil, dependency.Wrap(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dependency.Wrap(err)
	}
	rec.Status = "RELEASED"
	return &rec, nil
}

func (r *InventoryRepository) releaseCommittedInMemory(orderID, actor string) (*ReservationRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.reservations[orderID]
	if !ok {
		return nil, ErrReservationNotFound
	}
	if rec.Status == "RELEASED" {
		cp := *rec
		return &cp, nil
	}
	if rec.Status != "COMMITTED" {
		return nil, domain.ErrInvalidReservationState
	}
	if err := r.engine.ReleaseCommitted(rec.WarehouseID, rec.Lines); err != nil {
		return nil, err
	}
	r.logLines(domain.MovementRelease, rec.WarehouseID, orderID, rec.Lines, "paid order cancelled before handover")
	rec.Status = "RELEASED"
	cp := *rec
	return &cp, nil
}
