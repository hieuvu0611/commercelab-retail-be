package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5"
)

// OpeningStockExistsError reports the first SKU that already has opening stock in the warehouse.
type OpeningStockExistsError struct{ SkuID string }

func (e *OpeningStockExistsError) Error() string {
	return fmt.Sprintf("opening stock was already recorded for %s", e.SkuID)
}

// MovementFilter selects ledger records; Page starts at 1.
type MovementFilter struct {
	WarehouseID string
	SkuID       string
	Page, Limit int
}

func lineMovement(movementType, warehouseID, orderID string, line domain.ReserveLine, reason string) domain.Movement {
	return domain.Movement{
		SkuID: line.SkuID, WarehouseID: warehouseID, Type: movementType, Qty: line.Quantity,
		RefType: "ORDER", RefID: orderID, ActorID: domain.SystemActor, Reason: reason,
	}
}

// insertMovement writes one ledger record inside the transaction that changes the bucket.
func insertMovement(ctx context.Context, tx pgx.Tx, m domain.Movement) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO stock_movements (sku_id, warehouse_id, type, qty, ref_type, ref_id, actor_id, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, m.SkuID, m.WarehouseID, m.Type, m.Qty, m.RefType, m.RefID, m.ActorID, m.Reason)
	if err != nil {
		return dependency.Wrap(err)
	}
	return nil
}

// logLines records one movement per line in memory mode. The caller holds r.mu.
func (r *InventoryRepository) logLines(movementType, warehouseID, orderID string, lines []domain.ReserveLine, reason string) {
	for _, line := range lines {
		r.appendMovement(lineMovement(movementType, warehouseID, orderID, line, reason))
	}
}

func (r *InventoryRepository) appendMovement(m domain.Movement) {
	m.ID = int64(len(r.movements) + 1)
	m.At = time.Now().UTC()
	r.movements = append(r.movements, m)
}

// RecordOpeningStock adds opening stock for every line in one transaction, once per SKU and warehouse.
// If any SKU already has opening stock nothing is applied.
func (r *InventoryRepository) RecordOpeningStock(ctx context.Context, warehouseID, actorID, reason string, lines []domain.ReserveLine) error {
	if warehouseID == "" {
		return ErrWarehouseRequired
	}
	lines, err := domain.NormalizeLines(lines)
	if err != nil {
		return err
	}
	if r.pool == nil {
		return r.recordOpeningInMemory(warehouseID, actorID, reason, lines)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, line := range lines {
		tag, err := tx.Exec(ctx, `
			INSERT INTO stock_movements (sku_id, warehouse_id, type, qty, ref_type, actor_id, reason)
			VALUES ($1, $2, 'OPENING', $3, 'OPENING_STOCK', $4, $5)
			ON CONFLICT (sku_id, warehouse_id) WHERE type = 'OPENING' DO NOTHING
		`, line.SkuID, warehouseID, line.Quantity, actorID, reason)
		if err != nil {
			return dependency.Wrap(err)
		}
		if tag.RowsAffected() == 0 {
			return &OpeningStockExistsError{SkuID: line.SkuID}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stock_items (sku_id, warehouse_id, on_hand) VALUES ($1, $2, $3)
			ON CONFLICT (sku_id, warehouse_id)
			DO UPDATE SET on_hand = stock_items.on_hand + EXCLUDED.on_hand, version = stock_items.version + 1, updated_at = NOW()
		`, line.SkuID, warehouseID, line.Quantity); err != nil {
			return dependency.Wrap(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dependency.Wrap(err)
	}
	return nil
}

func (r *InventoryRepository) recordOpeningInMemory(warehouseID, actorID, reason string, lines []domain.ReserveLine) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range lines {
		for _, m := range r.movements {
			if m.Type == domain.MovementOpening && m.SkuID == line.SkuID && m.WarehouseID == warehouseID {
				return &OpeningStockExistsError{SkuID: line.SkuID}
			}
		}
	}
	for _, line := range lines {
		if err := r.engine.AddOpening(warehouseID, line.SkuID, line.Quantity); err != nil {
			return err
		}
		r.appendMovement(domain.Movement{
			SkuID: line.SkuID, WarehouseID: warehouseID, Type: domain.MovementOpening, Qty: line.Quantity,
			RefType: "OPENING_STOCK", ActorID: actorID, Reason: reason,
		})
	}
	return nil
}

// ListStock returns the buckets of every SKU in one warehouse, sorted by SKU id.
func (r *InventoryRepository) ListStock(ctx context.Context, warehouseID string) ([]domain.StockItem, error) {
	if r.pool == nil {
		return r.engine.List(warehouseID), nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit, version
		FROM stock_items WHERE warehouse_id = $1 ORDER BY sku_id
	`, warehouseID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	items := make([]domain.StockItem, 0)
	for rows.Next() {
		var st domain.StockItem
		if err := rows.Scan(&st.SkuID, &st.WarehouseID, &st.OnHand, &st.Unsellable, &st.Reserved, &st.Committed, &st.InTransit, &st.Version); err != nil {
			return nil, dependency.Wrap(err)
		}
		items = append(items, st)
	}
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	return items, nil
}

// ListMovements returns ledger records of a warehouse (optionally one SKU), newest first, with the total count.
func (r *InventoryRepository) ListMovements(ctx context.Context, f MovementFilter) ([]domain.Movement, int64, error) {
	if f.Page < 1 || f.Limit < 1 {
		return nil, 0, errors.New("page and limit must be positive")
	}
	offset := (f.Page - 1) * f.Limit
	if r.pool == nil {
		return r.listMovementsInMemory(f, offset)
	}
	var total int64
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM stock_movements WHERE warehouse_id = $1 AND ($2 = '' OR sku_id = $2)
	`, f.WarehouseID, f.SkuID).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, sku_id, warehouse_id, type, qty, ref_type, ref_id, actor_id, reason, occurred_at
		FROM stock_movements WHERE warehouse_id = $1 AND ($2 = '' OR sku_id = $2)
		ORDER BY occurred_at DESC, id DESC LIMIT $3 OFFSET $4
	`, f.WarehouseID, f.SkuID, f.Limit, offset)
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	defer rows.Close()
	out := make([]domain.Movement, 0)
	for rows.Next() {
		var m domain.Movement
		if err := rows.Scan(&m.ID, &m.SkuID, &m.WarehouseID, &m.Type, &m.Qty, &m.RefType, &m.RefID, &m.ActorID, &m.Reason, &m.At); err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	return out, total, nil
}

func (r *InventoryRepository) listMovementsInMemory(f MovementFilter, offset int) ([]domain.Movement, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	matched := make([]domain.Movement, 0)
	for i := len(r.movements) - 1; i >= 0; i-- { // newest first
		m := r.movements[i]
		if m.WarehouseID == f.WarehouseID && (f.SkuID == "" || m.SkuID == f.SkuID) {
			matched = append(matched, m)
		}
	}
	total := int64(len(matched))
	if offset >= len(matched) {
		return []domain.Movement{}, total, nil
	}
	end := min(offset+f.Limit, len(matched))
	return matched[offset:end], total, nil
}
