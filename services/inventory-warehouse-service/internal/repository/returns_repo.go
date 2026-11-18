package repository

import (
	"context"
	"errors"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5"
)

var errReturnStepConflict = errors.New("the return stock step was already recorded with a different payload")

type returnStockChange struct {
	warehouseID, skuID           string
	onHandDelta, unsellableDelta int64
}

// ReceiveReturnedItem implements SC07: OnHand +qty, Unsellable +qty, Available unchanged.
func (r *InventoryRepository) ReceiveReturnedItem(ctx context.Context, warehouseID, skuID string, qty int64, key string) (domain.StockItem, error) {
	if qty <= 0 {
		return domain.StockItem{}, domain.ErrInvalidReservationState
	}
	return r.applyReturnStockChange(ctx, key, returnStockChange{warehouseID, skuID, qty, qty})
}

// ApproveReturnQC releases passing units. A keyed zero quantity records an all-failed QC decision.
func (r *InventoryRepository) ApproveReturnQC(ctx context.Context, warehouseID, skuID string, qty int64, key string) (domain.StockItem, error) {
	if qty < 0 || (qty == 0 && key == "") {
		return domain.StockItem{}, domain.ErrInvalidReservationState
	}
	return r.applyReturnStockChange(ctx, key, returnStockChange{warehouseID, skuID, 0, -qty})
}

func (r *InventoryRepository) applyReturnStockChange(ctx context.Context, key string, change returnStockChange) (domain.StockItem, error) {
	if change.warehouseID == "" {
		change.warehouseID = domain.DefaultWarehouseID
	}
	if r.pool == nil {
		return r.applyMemoryReturnStockChange(key, change)
	}
	if key == "" {
		return updateReturnStock(ctx, r.pool, change, false)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.StockItem{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		INSERT INTO return_stock_steps
		    (idempotency_key, warehouse_id, sku_id, on_hand_delta, unsellable_delta)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (idempotency_key) DO NOTHING
	`, key, change.warehouseID, change.skuID, change.onHandDelta, change.unsellableDelta)
	if err != nil {
		return domain.StockItem{}, dependency.Wrap(err)
	}
	replay := tag.RowsAffected() == 0
	if replay {
		var recorded returnStockChange
		err := tx.QueryRow(ctx, `
			SELECT warehouse_id, sku_id, on_hand_delta, unsellable_delta
			FROM return_stock_steps WHERE idempotency_key = $1
		`, key).Scan(&recorded.warehouseID, &recorded.skuID, &recorded.onHandDelta, &recorded.unsellableDelta)
		if err != nil {
			return domain.StockItem{}, dependency.Wrap(err)
		}
		if recorded != change {
			return domain.StockItem{}, errReturnStepConflict
		}
	}
	st, err := updateReturnStock(ctx, tx, change, replay)
	if err != nil {
		return domain.StockItem{}, err
	}
	return st, dependency.Wrap(tx.Commit(ctx))
}

func updateReturnStock(ctx context.Context, q procurementQueryer, change returnStockChange, replay bool) (domain.StockItem, error) {
	var row pgx.Row
	if replay || change.unsellableDelta == 0 {
		row = q.QueryRow(ctx, `
			SELECT sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit, version
			FROM stock_items WHERE sku_id = $1 AND warehouse_id = $2
		`, change.skuID, change.warehouseID)
	} else {
		row = q.QueryRow(ctx, `
			UPDATE stock_items
			SET on_hand = on_hand + $3, unsellable = unsellable + $4, version = version + 1
			WHERE sku_id = $1 AND warehouse_id = $2 AND unsellable + $4 >= 0
			RETURNING sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit, version
		`, change.skuID, change.warehouseID, change.onHandDelta, change.unsellableDelta)
	}
	var st domain.StockItem
	err := row.Scan(&st.SkuID, &st.WarehouseID, &st.OnHand, &st.Unsellable, &st.Reserved, &st.Committed, &st.InTransit, &st.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StockItem{}, domain.ErrInsufficientStock
	}
	return st, dependency.Wrap(err)
}

func (r *InventoryRepository) applyMemoryReturnStockChange(key string, change returnStockChange) (domain.StockItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	recorded, replay := r.returnSteps[key]
	if replay && recorded != change {
		return domain.StockItem{}, errReturnStepConflict
	}
	if !replay {
		var err error
		switch {
		case change.onHandDelta > 0:
			err = r.engine.ReceiveReturnedItem(change.warehouseID, change.skuID, change.onHandDelta)
		case change.unsellableDelta < 0:
			err = r.engine.ApproveReturnQC(change.warehouseID, change.skuID, -change.unsellableDelta)
		}
		if err != nil {
			return domain.StockItem{}, err
		}
	}
	st, ok := r.engine.GetStock(change.warehouseID, change.skuID)
	if !ok {
		return domain.StockItem{}, domain.ErrInsufficientStock
	}
	if key != "" {
		r.returnSteps[key] = change
	}
	return st, nil
}
