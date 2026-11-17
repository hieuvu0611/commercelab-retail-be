package repository

import (
	"context"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
)

// ReleaseCommittedReservation releases all committed stock of a paid order back to available (B05).
func (r *InventoryRepository) ReleaseCommittedReservation(ctx context.Context, orderID, actor string) (*ReservationRecord, error) {
	return r.ReleaseCommittedForWarehouse(ctx, orderID, "", actor)
}

// ReleaseCommittedForWarehouse releases committed stock of one warehouse (when warehouseID is set) or all
// warehouses of the order (when warehouseID == "").
func (r *InventoryRepository) ReleaseCommittedForWarehouse(ctx context.Context, orderID, warehouseID, actor string) (*ReservationRecord, error) {
	if r.pool == nil {
		return r.releaseCommittedInMemory(orderID, warehouseID, actor)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recs, err := loadReservationsTx(ctx, tx, orderID, warehouseID)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrReservationNotFound
	}
	allReleased := true
	for _, rec := range recs {
		if rec.Status != "RELEASED" {
			allReleased = false
			break
		}
	}
	if allReleased {
		return &recs[0], nil
	}
	for i := range recs {
		rec := &recs[i]
		if rec.Status == "RELEASED" {
			continue
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
		if _, err := tx.Exec(ctx, `UPDATE reservations SET status = 'RELEASED', version = version + 1 WHERE reservation_id = $1`, rec.ReservationID); err != nil {
			return nil, dependency.Wrap(err)
		}
		rec.Status = "RELEASED"
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dependency.Wrap(err)
	}
	return &recs[0], nil
}

func (r *InventoryRepository) releaseCommittedInMemory(orderID, warehouseID, actor string) (*ReservationRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	recs, ok := r.reservations[orderID]
	if !ok || len(recs) == 0 {
		return nil, ErrReservationNotFound
	}
	var matched []*ReservationRecord
	for _, rec := range recs {
		if warehouseID == "" || rec.WarehouseID == warehouseID {
			matched = append(matched, rec)
		}
	}
	if len(matched) == 0 {
		return nil, ErrReservationNotFound
	}
	allReleased := true
	for _, rec := range matched {
		if rec.Status != "RELEASED" {
			allReleased = false
			break
		}
	}
	if allReleased {
		cp := *matched[0]
		return &cp, nil
	}
	for _, rec := range matched {
		if rec.Status == "RELEASED" {
			continue
		}
		if rec.Status != "COMMITTED" {
			return nil, domain.ErrInvalidReservationState
		}
		if err := r.engine.ReleaseCommitted(rec.WarehouseID, rec.Lines); err != nil {
			return nil, err
		}
		r.logLines(domain.MovementRelease, rec.WarehouseID, orderID, rec.Lines, "paid order cancelled before handover")
		rec.Status = "RELEASED"
	}
	cp := *matched[0]
	return &cp, nil
}
