package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5"
)

func newTransferID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "TRF-" + strings.ToUpper(hex.EncodeToString(b[:]))
}

func (r *InventoryRepository) CreateTransfer(ctx context.Context, fromWH, toWH, actorID, notes string, items []domain.ReserveLine) (*domain.WarehouseTransfer, error) {
	if fromWH == "" || toWH == "" || fromWH == toWH {
		return nil, domain.ErrTransferInvalidInput
	}
	norm, err := domain.NormalizeLines(items)
	if err != nil {
		return nil, err
	}
	now := r.now().UTC()
	trf := &domain.WarehouseTransfer{
		TransferID:      newTransferID(),
		FromWarehouseID: fromWH,
		ToWarehouseID:   toWH,
		Status:          domain.TransferStatusDraft,
		CreatedBy:       actorID,
		Notes:           notes,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	for _, l := range norm {
		trf.Items = append(trf.Items, domain.TransferItem{SkuID: l.SkuID, Quantity: l.Quantity})
	}

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.transfers[trf.TransferID] = trf
		cp := *trf
		return &cp, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory.warehouse_transfers (transfer_id, from_warehouse_id, to_warehouse_id, status, created_by, notes, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
	`, trf.TransferID, trf.FromWarehouseID, trf.ToWarehouseID, trf.Status, trf.CreatedBy, trf.Notes, now); err != nil {
		return nil, dependency.Wrap(err)
	}
	for _, it := range trf.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory.warehouse_transfer_items (transfer_id, sku_id, quantity)
			VALUES ($1,$2,$3)
		`, trf.TransferID, it.SkuID, it.Quantity); err != nil {
			return nil, dependency.Wrap(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dependency.Wrap(err)
	}
	return trf, nil
}

func (r *InventoryRepository) GetTransfer(ctx context.Context, transferID string) (*domain.WarehouseTransfer, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		t, ok := r.transfers[transferID]
		if !ok {
			return nil, domain.ErrTransferNotFound
		}
		cp := *t
		cp.Items = append([]domain.TransferItem(nil), t.Items...)
		return &cp, nil
	}
	var t domain.WarehouseTransfer
	err := r.pool.QueryRow(ctx, `
		SELECT transfer_id, from_warehouse_id, to_warehouse_id, status, created_by, dispatched_by, received_by,
		       exception_case_id, notes, dispatched_at, received_at, created_at, updated_at
		FROM inventory.warehouse_transfers WHERE transfer_id = $1
	`, transferID).Scan(&t.TransferID, &t.FromWarehouseID, &t.ToWarehouseID, &t.Status, &t.CreatedBy,
		&t.DispatchedBy, &t.ReceivedBy, &t.ExceptionCaseID, &t.Notes, &t.DispatchedAt, &t.ReceivedAt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTransferNotFound
	}
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT sku_id, quantity, received_good_qty, damaged_qty, missing_qty
		FROM inventory.warehouse_transfer_items WHERE transfer_id = $1 ORDER BY sku_id
	`, transferID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var it domain.TransferItem
		if err := rows.Scan(&it.SkuID, &it.Quantity, &it.ReceivedGoodQty, &it.DamagedQty, &it.MissingQty); err != nil {
			return nil, dependency.Wrap(err)
		}
		t.Items = append(t.Items, it)
	}
	return &t, dependency.Wrap(rows.Err())
}

func (r *InventoryRepository) ListTransfers(ctx context.Context, warehouseID string) ([]domain.WarehouseTransfer, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		out := make([]domain.WarehouseTransfer, 0)
		for _, t := range r.transfers {
			if warehouseID == "" || warehouseID == "ALL" || t.FromWarehouseID == warehouseID || t.ToWarehouseID == warehouseID {
				cp := *t
				cp.Items = append([]domain.TransferItem(nil), t.Items...)
				out = append(out, cp)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT transfer_id FROM inventory.warehouse_transfers
		WHERE ($1 = '' OR $1 = 'ALL' OR from_warehouse_id = $1 OR to_warehouse_id = $1)
		ORDER BY created_at DESC LIMIT 100
	`, warehouseID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, dependency.Wrap(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]domain.WarehouseTransfer, 0, len(ids))
	for _, id := range ids {
		t, err := r.GetTransfer(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

// DispatchTransfer deducts source OnHand and moves stock to destination InTransit (SC09).
func (r *InventoryRepository) DispatchTransfer(ctx context.Context, transferID, actorID string) (*domain.WarehouseTransfer, error) {
	now := r.now().UTC()
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		t, ok := r.transfers[transferID]
		if !ok {
			return nil, domain.ErrTransferNotFound
		}
		if t.Status == domain.TransferStatusInTransit {
			cp := *t
			return &cp, nil
		}
		if t.Status != domain.TransferStatusDraft {
			return nil, domain.ErrTransferInvalidState
		}
		lines := make([]domain.ReserveLine, len(t.Items))
		for i, it := range t.Items {
			lines[i] = domain.ReserveLine{SkuID: it.SkuID, Quantity: it.Quantity}
		}
		if err := r.engine.DispatchTransfer(t.FromWarehouseID, t.ToWarehouseID, lines); err != nil {
			return nil, err
		}
		for _, l := range lines {
			r.appendMovement(domain.Movement{
				SkuID: l.SkuID, WarehouseID: t.FromWarehouseID, Type: domain.MovementTransferOut, Qty: l.Quantity,
				RefType: "TRANSFER", RefID: transferID, ActorID: actorID, Reason: "transfer dispatched to " + t.ToWarehouseID,
			})
		}
		t.Status = domain.TransferStatusInTransit
		t.DispatchedBy = actorID
		t.DispatchedAt = &now
		t.UpdatedAt = now
		cp := *t
		cp.Items = append([]domain.TransferItem(nil), t.Items...)
		return &cp, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var fromWH, toWH, status string
	err = tx.QueryRow(ctx, `
		SELECT from_warehouse_id, to_warehouse_id, status
		FROM inventory.warehouse_transfers WHERE transfer_id = $1 FOR UPDATE
	`, transferID).Scan(&fromWH, &toWH, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTransferNotFound
	}
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	if status == domain.TransferStatusInTransit {
		_ = tx.Commit(ctx)
		return r.GetTransfer(ctx, transferID)
	}
	if status != domain.TransferStatusDraft {
		return nil, domain.ErrTransferInvalidState
	}

	itemRows, err := tx.Query(ctx, `
		SELECT sku_id, quantity FROM inventory.warehouse_transfer_items WHERE transfer_id = $1 ORDER BY sku_id
	`, transferID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	var items []domain.ReserveLine
	for itemRows.Next() {
		var l domain.ReserveLine
		if err := itemRows.Scan(&l.SkuID, &l.Quantity); err != nil {
			itemRows.Close()
			return nil, dependency.Wrap(err)
		}
		items = append(items, l)
	}
	itemRows.Close()

	for _, line := range items {
		var onHand, unsellable, reserved, committed int64
		err := tx.QueryRow(ctx, `
			SELECT on_hand, unsellable, reserved, committed
			FROM inventory.stock_items WHERE sku_id = $1 AND warehouse_id = $2 FOR UPDATE
		`, line.SkuID, fromWH).Scan(&onHand, &unsellable, &reserved, &committed)
		if errors.Is(err, pgx.ErrNoRows) || (onHand-unsellable-reserved-committed) < line.Quantity {
			return nil, domain.ErrInsufficientStock
		}
		if err != nil {
			return nil, dependency.Wrap(err)
		}
	}

	for _, line := range items {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory.stock_items
			SET on_hand = on_hand - $1, version = version + 1, updated_at = NOW()
			WHERE sku_id = $2 AND warehouse_id = $3
		`, line.Quantity, line.SkuID, fromWH); err != nil {
			return nil, dependency.Wrap(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory.stock_items (sku_id, warehouse_id, on_hand, in_transit)
			VALUES ($1, $2, 0, $3)
			ON CONFLICT (sku_id, warehouse_id) DO UPDATE
			SET in_transit = inventory.stock_items.in_transit + EXCLUDED.in_transit,
			    version = inventory.stock_items.version + 1,
			    updated_at = NOW()
		`, line.SkuID, toWH, line.Quantity); err != nil {
			return nil, dependency.Wrap(err)
		}
		if err := insertMovement(ctx, tx, domain.Movement{
			SkuID: line.SkuID, WarehouseID: fromWH, Type: domain.MovementTransferOut, Qty: line.Quantity,
			RefType: "TRANSFER", RefID: transferID, ActorID: actorID, Reason: "transfer dispatched to " + toWH,
		}); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory.warehouse_transfers
		SET status = $2, dispatched_by = $3, dispatched_at = $4, updated_at = $4
		WHERE transfer_id = $1
	`, transferID, domain.TransferStatusInTransit, actorID, now); err != nil {
		return nil, dependency.Wrap(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dependency.Wrap(err)
	}
	return r.GetTransfer(ctx, transferID)
}

// ReceiveTransfer records receipt & QC at the destination warehouse and flags discrepancies (SC09).
func (r *InventoryRepository) ReceiveTransfer(ctx context.Context, transferID, actorID string, lines []domain.TransferReceiveLine) (*domain.WarehouseTransfer, bool, error) {
	now := r.now().UTC()
	bySku := make(map[string]domain.TransferReceiveLine, len(lines))
	for _, l := range lines {
		if l.SkuID == "" || l.ReceivedGoodQty < 0 || l.DamagedQty < 0 || l.MissingQty < 0 {
			return nil, false, domain.ErrTransferInvalidInput
		}
		bySku[l.SkuID] = l
	}

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		t, ok := r.transfers[transferID]
		if !ok {
			return nil, false, domain.ErrTransferNotFound
		}
		if t.Status == domain.TransferStatusReceived || t.Status == domain.TransferStatusReceivedWithDiscrepancy {
			cp := *t
			return &cp, t.Status == domain.TransferStatusReceivedWithDiscrepancy, nil
		}
		if t.Status != domain.TransferStatusInTransit {
			return nil, false, domain.ErrTransferInvalidState
		}
		dispatched := make(map[string]int64, len(t.Items))
		normLines := make([]domain.TransferReceiveLine, 0, len(t.Items))
		hasDiscrepancy := false
		for _, it := range t.Items {
			recLine, ok := bySku[it.SkuID]
			if !ok {
				return nil, false, domain.ErrTransferReceiveMath
			}
			if recLine.ReceivedGoodQty+recLine.DamagedQty+recLine.MissingQty != it.Quantity {
				return nil, false, domain.ErrTransferReceiveMath
			}
			if recLine.DamagedQty > 0 || recLine.MissingQty > 0 {
				hasDiscrepancy = true
			}
			dispatched[it.SkuID] = it.Quantity
			normLines = append(normLines, recLine)
		}
		if err := r.engine.ReceiveTransfer(t.ToWarehouseID, dispatched, normLines); err != nil {
			return nil, false, err
		}
		for i := range t.Items {
			recLine := bySku[t.Items[i].SkuID]
			t.Items[i].ReceivedGoodQty = recLine.ReceivedGoodQty
			t.Items[i].DamagedQty = recLine.DamagedQty
			t.Items[i].MissingQty = recLine.MissingQty
			if recLine.ReceivedGoodQty > 0 {
				r.appendMovement(domain.Movement{
					SkuID: recLine.SkuID, WarehouseID: t.ToWarehouseID, Type: domain.MovementTransferInGood, Qty: recLine.ReceivedGoodQty,
					RefType: "TRANSFER", RefID: transferID, ActorID: actorID, Reason: "transfer received good",
				})
			}
			if recLine.DamagedQty > 0 {
				r.appendMovement(domain.Movement{
					SkuID: recLine.SkuID, WarehouseID: t.ToWarehouseID, Type: domain.MovementTransferInDamaged, Qty: recLine.DamagedQty,
					RefType: "TRANSFER", RefID: transferID, ActorID: actorID, Reason: "transfer received damaged",
				})
			}
		}
		t.Status = domain.TransferStatusReceived
		if hasDiscrepancy {
			t.Status = domain.TransferStatusReceivedWithDiscrepancy
		}
		t.ReceivedBy = actorID
		t.ReceivedAt = &now
		t.UpdatedAt = now
		cp := *t
		cp.Items = append([]domain.TransferItem(nil), t.Items...)
		return &cp, hasDiscrepancy, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var toWH, status string
	err = tx.QueryRow(ctx, `
		SELECT to_warehouse_id, status FROM inventory.warehouse_transfers WHERE transfer_id = $1 FOR UPDATE
	`, transferID).Scan(&toWH, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, domain.ErrTransferNotFound
	}
	if err != nil {
		return nil, false, dependency.Wrap(err)
	}
	if status == domain.TransferStatusReceived || status == domain.TransferStatusReceivedWithDiscrepancy {
		_ = tx.Commit(ctx)
		t, err := r.GetTransfer(ctx, transferID)
		return t, status == domain.TransferStatusReceivedWithDiscrepancy, err
	}
	if status != domain.TransferStatusInTransit {
		return nil, false, domain.ErrTransferInvalidState
	}

	itemRows, err := tx.Query(ctx, `
		SELECT sku_id, quantity FROM inventory.warehouse_transfer_items WHERE transfer_id = $1 ORDER BY sku_id
	`, transferID)
	if err != nil {
		return nil, false, dependency.Wrap(err)
	}
	var expected []domain.ReserveLine
	for itemRows.Next() {
		var l domain.ReserveLine
		if err := itemRows.Scan(&l.SkuID, &l.Quantity); err != nil {
			itemRows.Close()
			return nil, false, dependency.Wrap(err)
		}
		expected = append(expected, l)
	}
	itemRows.Close()

	hasDiscrepancy := false
	for _, it := range expected {
		recLine, ok := bySku[it.SkuID]
		if !ok || recLine.ReceivedGoodQty+recLine.DamagedQty+recLine.MissingQty != it.Quantity {
			return nil, false, domain.ErrTransferReceiveMath
		}
		if recLine.DamagedQty > 0 || recLine.MissingQty > 0 {
			hasDiscrepancy = true
		}
		stockTag, err := tx.Exec(ctx, `
			UPDATE inventory.stock_items
			SET in_transit = in_transit - $1,
			    on_hand = on_hand + $2,
			    unsellable = unsellable + $3,
			    version = version + 1,
			    updated_at = NOW()
			WHERE sku_id = $4 AND warehouse_id = $5 AND in_transit >= $1
		`, it.Quantity, recLine.ReceivedGoodQty+recLine.DamagedQty, recLine.DamagedQty, it.SkuID, toWH)
		if err != nil {
			return nil, false, dependency.Wrap(err)
		}
		if stockTag.RowsAffected() != 1 {
			return nil, false, domain.ErrTransferInsufficientInTransit
		}
		itemTag, err := tx.Exec(ctx, `
			UPDATE inventory.warehouse_transfer_items
			SET received_good_qty = $1, damaged_qty = $2, missing_qty = $3
			WHERE transfer_id = $4 AND sku_id = $5
		`, recLine.ReceivedGoodQty, recLine.DamagedQty, recLine.MissingQty, transferID, it.SkuID)
		if err != nil {
			return nil, false, dependency.Wrap(err)
		}
		if itemTag.RowsAffected() != 1 {
			return nil, false, domain.ErrTransferInvalidState
		}
		if recLine.ReceivedGoodQty > 0 {
			if err := insertMovement(ctx, tx, domain.Movement{
				SkuID: it.SkuID, WarehouseID: toWH, Type: domain.MovementTransferInGood, Qty: recLine.ReceivedGoodQty,
				RefType: "TRANSFER", RefID: transferID, ActorID: actorID, Reason: "transfer received good",
			}); err != nil {
				return nil, false, err
			}
		}
		if recLine.DamagedQty > 0 {
			if err := insertMovement(ctx, tx, domain.Movement{
				SkuID: it.SkuID, WarehouseID: toWH, Type: domain.MovementTransferInDamaged, Qty: recLine.DamagedQty,
				RefType: "TRANSFER", RefID: transferID, ActorID: actorID, Reason: "transfer received damaged",
			}); err != nil {
				return nil, false, err
			}
		}
	}

	nextStatus := domain.TransferStatusReceived
	if hasDiscrepancy {
		nextStatus = domain.TransferStatusReceivedWithDiscrepancy
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory.warehouse_transfers
		SET status = $2, received_by = $3, received_at = $4, updated_at = $4
		WHERE transfer_id = $1
	`, transferID, nextStatus, actorID, now); err != nil {
		return nil, false, dependency.Wrap(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, dependency.Wrap(err)
	}
	t, err := r.GetTransfer(ctx, transferID)
	return t, hasDiscrepancy, err
}

func (r *InventoryRepository) SetTransferExceptionCase(ctx context.Context, transferID, caseID string) error {
	if caseID == "" {
		return nil
	}
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if t, ok := r.transfers[transferID]; ok {
			t.ExceptionCaseID = caseID
		}
		return nil
	}
	_, err := r.pool.Exec(ctx, `UPDATE inventory.warehouse_transfers SET exception_case_id = $2 WHERE transfer_id = $1`, transferID, caseID)
	return dependency.Wrap(err)
}
