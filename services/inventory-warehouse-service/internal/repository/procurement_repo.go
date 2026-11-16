package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5"
)

// CreateSupplier adds a new supplier record.
func (r *InventoryRepository) CreateSupplier(ctx context.Context, s domain.Supplier) (domain.Supplier, error) {
	if s.SupplierID == "" {
		s.SupplierID = fmt.Sprintf("SUP-%d", time.Now().UnixNano()%1000000)
	}
	now := r.now().UTC()
	s.CreatedAt = now
	s.UpdatedAt = now

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		cp := s
		r.suppliers[s.SupplierID] = &cp
		return s, nil
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO suppliers (supplier_id, name, code, contact_email, phone, address, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, s.SupplierID, s.Name, s.Code, s.ContactEmail, s.Phone, s.Address, s.Active, s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return domain.Supplier{}, dependency.Wrap(err)
	}
	return s, nil
}

// ListSuppliers returns all suppliers.
func (r *InventoryRepository) ListSuppliers(ctx context.Context) ([]domain.Supplier, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		list := make([]domain.Supplier, 0, len(r.suppliers))
		for _, s := range r.suppliers {
			list = append(list, *s)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		return list, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT supplier_id, name, code, contact_email, phone, address, active, created_at, updated_at
		FROM suppliers ORDER BY name
	`)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.Supplier, 0)
	for rows.Next() {
		var s domain.Supplier
		if err := rows.Scan(&s.SupplierID, &s.Name, &s.Code, &s.ContactEmail, &s.Phone, &s.Address, &s.Active, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, dependency.Wrap(err)
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	return list, nil
}

// GetSupplier finds a supplier by ID.
func (r *InventoryRepository) GetSupplier(ctx context.Context, id string) (domain.Supplier, bool, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		s, ok := r.suppliers[id]
		if !ok {
			return domain.Supplier{}, false, nil
		}
		return *s, true, nil
	}

	var s domain.Supplier
	err := r.pool.QueryRow(ctx, `
		SELECT supplier_id, name, code, contact_email, phone, address, active, created_at, updated_at
		FROM suppliers WHERE supplier_id = $1
	`, id).Scan(&s.SupplierID, &s.Name, &s.Code, &s.ContactEmail, &s.Phone, &s.Address, &s.Active, &s.CreatedAt, &s.UpdatedAt)
	if err == pgx.ErrNoRows {
		return domain.Supplier{}, false, nil
	}
	if err != nil {
		return domain.Supplier{}, false, dependency.Wrap(err)
	}
	return s, true, nil
}

// CreatePurchaseOrder records a new PO with lines.
func (r *InventoryRepository) CreatePurchaseOrder(ctx context.Context, po domain.PurchaseOrder) (domain.PurchaseOrder, error) {
	total, err := domain.ValidatePOLines(po.Lines)
	if err != nil {
		return domain.PurchaseOrder{}, err
	}
	po.TotalAmountVND = total
	if po.POID == "" {
		po.POID = fmt.Sprintf("PO-%s-%04d", time.Now().Format("20060102"), time.Now().UnixNano()%10000)
	}
	if po.WarehouseID == "" {
		po.WarehouseID = domain.DefaultWarehouseID
	}
	if po.Status == "" {
		po.Status = domain.POStatusPendingApproval
	}
	now := r.now().UTC()
	po.CreatedAt = now
	po.UpdatedAt = now

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if sup, ok := r.suppliers[po.SupplierID]; ok {
			po.SupplierName = sup.Name
		}
		cp := po
		r.purchaseOrders[po.POID] = &cp
		return po, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.PurchaseOrder{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO purchase_orders (po_id, supplier_id, warehouse_id, status, total_amount_vnd, created_by, notes, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, po.POID, po.SupplierID, po.WarehouseID, po.Status, po.TotalAmountVND, po.CreatedBy, po.Notes, po.CreatedAt, po.UpdatedAt)
	if err != nil {
		return domain.PurchaseOrder{}, dependency.Wrap(err)
	}

	for _, line := range po.Lines {
		_, err = tx.Exec(ctx, `
			INSERT INTO purchase_order_lines (po_id, sku_id, ordered_qty, unit_cost_vnd, received_qty, damaged_qty, short_qty, created_at)
			VALUES ($1, $2, $3, $4, 0, 0, 0, $5)
		`, po.POID, line.SkuID, line.OrderedQty, line.UnitCostVND, po.CreatedAt)
		if err != nil {
			return domain.PurchaseOrder{}, dependency.Wrap(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.PurchaseOrder{}, dependency.Wrap(err)
	}

	return po, nil
}

// GetPurchaseOrder retrieves a PO and its lines.
func (r *InventoryRepository) GetPurchaseOrder(ctx context.Context, poID string) (domain.PurchaseOrder, bool, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		po, ok := r.purchaseOrders[poID]
		if !ok {
			return domain.PurchaseOrder{}, false, nil
		}
		return *po, true, nil
	}

	var po domain.PurchaseOrder
	var approvedBy *string
	err := r.pool.QueryRow(ctx, `
		SELECT p.po_id, p.supplier_id, s.name, p.warehouse_id, p.status, p.total_amount_vnd,
		       p.created_by, p.approved_by, p.approved_at, p.notes, p.created_at, p.updated_at
		FROM purchase_orders p
		LEFT JOIN suppliers s ON p.supplier_id = s.supplier_id
		WHERE p.po_id = $1
	`, poID).Scan(
		&po.POID, &po.SupplierID, &po.SupplierName, &po.WarehouseID, &po.Status, &po.TotalAmountVND,
		&po.CreatedBy, &approvedBy, &po.ApprovedAt, &po.Notes, &po.CreatedAt, &po.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return domain.PurchaseOrder{}, false, nil
	}
	if err != nil {
		return domain.PurchaseOrder{}, false, dependency.Wrap(err)
	}
	if approvedBy != nil {
		po.ApprovedBy = *approvedBy
	}

	linesRows, err := r.pool.Query(ctx, `
		SELECT id, po_id, sku_id, ordered_qty, unit_cost_vnd, received_qty, damaged_qty, short_qty
		FROM purchase_order_lines WHERE po_id = $1 ORDER BY id
	`, poID)
	if err != nil {
		return domain.PurchaseOrder{}, false, dependency.Wrap(err)
	}
	defer linesRows.Close()

	po.Lines = make([]domain.PurchaseOrderLine, 0)
	for linesRows.Next() {
		var l domain.PurchaseOrderLine
		if err := linesRows.Scan(&l.ID, &l.POID, &l.SkuID, &l.OrderedQty, &l.UnitCostVND, &l.ReceivedQty, &l.DamagedQty, &l.ShortQty); err != nil {
			return domain.PurchaseOrder{}, false, dependency.Wrap(err)
		}
		po.Lines = append(po.Lines, l)
	}
	if err := linesRows.Err(); err != nil {
		return domain.PurchaseOrder{}, false, dependency.Wrap(err)
	}

	return po, true, nil
}

// ListPurchaseOrders lists POs filtered by warehouse and status.
func (r *InventoryRepository) ListPurchaseOrders(ctx context.Context, warehouseID, status string) ([]domain.PurchaseOrder, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		list := make([]domain.PurchaseOrder, 0)
		for _, po := range r.purchaseOrders {
			if (warehouseID == "" || po.WarehouseID == warehouseID) && (status == "" || po.Status == status) {
				list = append(list, *po)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		return list, nil
	}

	var sb strings.Builder
	sb.WriteString(`
		SELECT p.po_id, p.supplier_id, COALESCE(s.name, ''), p.warehouse_id, p.status, p.total_amount_vnd,
		       p.created_by, COALESCE(p.approved_by, ''), p.approved_at, p.notes, p.created_at, p.updated_at
		FROM purchase_orders p
		LEFT JOIN suppliers s ON p.supplier_id = s.supplier_id
		WHERE 1=1
	`)
	args := []any{}
	idx := 1
	if warehouseID != "" {
		sb.WriteString(fmt.Sprintf(" AND p.warehouse_id = $%d", idx))
		args = append(args, warehouseID)
		idx++
	}
	if status != "" {
		sb.WriteString(fmt.Sprintf(" AND p.status = $%d", idx))
		args = append(args, status)
		idx++
	}
	sb.WriteString(" ORDER BY p.created_at DESC")

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.PurchaseOrder, 0)
	for rows.Next() {
		var po domain.PurchaseOrder
		if err := rows.Scan(
			&po.POID, &po.SupplierID, &po.SupplierName, &po.WarehouseID, &po.Status, &po.TotalAmountVND,
			&po.CreatedBy, &po.ApprovedBy, &po.ApprovedAt, &po.Notes, &po.CreatedAt, &po.UpdatedAt,
		); err != nil {
			return nil, dependency.Wrap(err)
		}
		list = append(list, po)
	}
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	return list, nil
}

// ApprovePurchaseOrder approves a PO after checking maker-checker P10 and threshold authority.
func (r *InventoryRepository) ApprovePurchaseOrder(ctx context.Context, poID string, approverID string, approverRoles []string) (domain.PurchaseOrder, error) {
	po, ok, err := r.GetPurchaseOrder(ctx, poID)
	if err != nil {
		return domain.PurchaseOrder{}, err
	}
	if !ok {
		return domain.PurchaseOrder{}, domain.ErrPurchaseOrderNotFound
	}

	// Validate P10 and thresholds
	if err := domain.CanApprovePO(po, approverID, approverRoles); err != nil {
		return domain.PurchaseOrder{}, err
	}

	now := r.now().UTC()
	po.Status = domain.POStatusApproved
	po.ApprovedBy = approverID
	po.ApprovedAt = &now
	po.UpdatedAt = now

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.purchaseOrders[poID] = &po
		return po, nil
	}

	_, err = r.pool.Exec(ctx, `
		UPDATE purchase_orders
		SET status = $1, approved_by = $2, approved_at = $3, updated_at = $4
		WHERE po_id = $5
	`, po.Status, po.ApprovedBy, po.ApprovedAt, po.UpdatedAt, poID)
	if err != nil {
		return domain.PurchaseOrder{}, dependency.Wrap(err)
	}

	return po, nil
}

// RejectPurchaseOrder rejects a PO.
func (r *InventoryRepository) RejectPurchaseOrder(ctx context.Context, poID string, rejecterID string, reason string) (domain.PurchaseOrder, error) {
	po, ok, err := r.GetPurchaseOrder(ctx, poID)
	if err != nil {
		return domain.PurchaseOrder{}, err
	}
	if !ok {
		return domain.PurchaseOrder{}, domain.ErrPurchaseOrderNotFound
	}
	if po.Status != domain.POStatusPendingApproval {
		return domain.PurchaseOrder{}, domain.ErrInvalidPOState
	}

	now := r.now().UTC()
	po.Status = domain.POStatusRejected
	if reason != "" {
		po.Notes = po.Notes + " | Rejected: " + reason
	}
	po.UpdatedAt = now

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.purchaseOrders[poID] = &po
		return po, nil
	}

	_, err = r.pool.Exec(ctx, `
		UPDATE purchase_orders
		SET status = $1, notes = $2, updated_at = $3
		WHERE po_id = $4
	`, po.Status, po.Notes, po.UpdatedAt, poID)
	if err != nil {
		return domain.PurchaseOrder{}, dependency.Wrap(err)
	}

	return po, nil
}

// RecordReceivingInspection processes inbound receipt and QC according to SC09:
// Order 100, receive 90, 5 damaged -> on-hand +90, available +85, unsellable +5, short 10.
func (r *InventoryRepository) RecordReceivingInspection(ctx context.Context, poID string, inspectorID string, notes string, lines []domain.ReceivingInspectionLine) (domain.ReceivingInspection, error) {
	po, ok, err := r.GetPurchaseOrder(ctx, poID)
	if err != nil {
		return domain.ReceivingInspection{}, err
	}
	if !ok {
		return domain.ReceivingInspection{}, domain.ErrPurchaseOrderNotFound
	}
	if po.Status != domain.POStatusApproved && po.Status != domain.POStatusOrdered && po.Status != domain.POStatusPartiallyReceived {
		return domain.ReceivingInspection{}, errors.New("PO is not in an approved/receivable state")
	}

	if len(lines) == 0 {
		return domain.ReceivingInspection{}, errors.New("at least one inspection line is required")
	}

	poLinesMap := make(map[string]*domain.PurchaseOrderLine)
	for i := range po.Lines {
		poLinesMap[po.Lines[i].SkuID] = &po.Lines[i]
	}

	// Validate lines against PO
	validatedLines := make([]domain.ReceivingInspectionLine, 0, len(lines))
	for _, l := range lines {
		poLine, exists := poLinesMap[l.SkuID]
		if !exists {
			return domain.ReceivingInspection{}, fmt.Errorf("SKU %s does not belong to PO %s", l.SkuID, poID)
		}
		short, err := domain.ValidateInspectionLine(poLine.OrderedQty, l.DeliveredQty, l.PassedQty, l.DamagedQty)
		if err != nil {
			return domain.ReceivingInspection{}, err
		}
		l.ShortQty = short
		validatedLines = append(validatedLines, l)
	}

	inspectionID := fmt.Sprintf("INSP-%s-%04d", time.Now().Format("20060102"), time.Now().UnixNano()%10000)
	now := r.now().UTC()
	insp := domain.ReceivingInspection{
		InspectionID: inspectionID,
		POID:         poID,
		WarehouseID:  po.WarehouseID,
		InspectorID:  inspectorID,
		Status:       "COMPLETED",
		Notes:        notes,
		Lines:        validatedLines,
		CreatedAt:    now,
	}

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		for _, l := range validatedLines {
			if err := r.engine.ReceiveInboundQC(po.WarehouseID, l.SkuID, l.PassedQty, l.DamagedQty); err != nil {
				return domain.ReceivingInspection{}, err
			}
			if l.PassedQty > 0 {
				r.appendMovement(domain.Movement{
					SkuID: l.SkuID, WarehouseID: po.WarehouseID, Type: domain.MovementPOReceiveGood, Qty: l.PassedQty,
					RefType: "PO", RefID: poID, ActorID: inspectorID, Reason: "Inbound QC passed stock receipt",
				})
			}
			if l.DamagedQty > 0 {
				r.appendMovement(domain.Movement{
					SkuID: l.SkuID, WarehouseID: po.WarehouseID, Type: domain.MovementPOReceiveDamage, Qty: l.DamagedQty,
					RefType: "PO", RefID: poID, ActorID: inspectorID, Reason: "Inbound QC damaged stock quarantined into unsellable",
				})
			}
			// Update in-memory PO lines
			if pol, exists := poLinesMap[l.SkuID]; exists {
				pol.ReceivedQty += l.PassedQty
				pol.DamagedQty += l.DamagedQty
				pol.ShortQty = l.ShortQty
			}
		}

		po.Status = domain.POStatusPartiallyReceived
		allCompleted := true
		for _, pol := range po.Lines {
			if pol.ReceivedQty+pol.DamagedQty+pol.ShortQty < pol.OrderedQty {
				allCompleted = false
			}
		}
		if allCompleted {
			po.Status = domain.POStatusCompleted
		}
		po.UpdatedAt = now
		r.purchaseOrders[poID] = &po
		r.inspections[inspectionID] = &insp
		return insp, nil
	}

	// Real Postgres transaction
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.ReceivingInspection{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO receiving_inspections (inspection_id, po_id, warehouse_id, inspector_id, status, notes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, insp.InspectionID, insp.POID, insp.WarehouseID, insp.InspectorID, insp.Status, insp.Notes, insp.CreatedAt)
	if err != nil {
		return domain.ReceivingInspection{}, dependency.Wrap(err)
	}

	for _, l := range validatedLines {
		_, err = tx.Exec(ctx, `
			INSERT INTO receiving_inspection_lines (inspection_id, sku_id, delivered_qty, passed_qty, damaged_qty, short_qty)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, insp.InspectionID, l.SkuID, l.DeliveredQty, l.PassedQty, l.DamagedQty, l.ShortQty)
		if err != nil {
			return domain.ReceivingInspection{}, dependency.Wrap(err)
		}

		// Update stock items: OnHand += (passed + damaged), Unsellable += damaged
		deliveredTotal := l.PassedQty + l.DamagedQty
		if deliveredTotal > 0 {
			_, err = tx.Exec(ctx, `
				INSERT INTO stock_items (sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit)
				VALUES ($1, $2, $3, $4, 0, 0, 0)
				ON CONFLICT (sku_id, warehouse_id) DO UPDATE
				SET on_hand = stock_items.on_hand + $3,
				    unsellable = stock_items.unsellable + $4,
				    version = stock_items.version + 1,
				    updated_at = NOW()
			`, l.SkuID, po.WarehouseID, deliveredTotal, l.DamagedQty)
			if err != nil {
				return domain.ReceivingInspection{}, dependency.Wrap(err)
			}
		}

		// Insert stock movements
		if l.PassedQty > 0 {
			err = insertMovement(ctx, tx, domain.Movement{
				SkuID: l.SkuID, WarehouseID: po.WarehouseID, Type: domain.MovementPOReceiveGood, Qty: l.PassedQty,
				RefType: "PO", RefID: poID, ActorID: inspectorID, Reason: "Inbound QC passed stock receipt",
			})
			if err != nil {
				return domain.ReceivingInspection{}, err
			}
		}
		if l.DamagedQty > 0 {
			err = insertMovement(ctx, tx, domain.Movement{
				SkuID: l.SkuID, WarehouseID: po.WarehouseID, Type: domain.MovementPOReceiveDamage, Qty: l.DamagedQty,
				RefType: "PO", RefID: poID, ActorID: inspectorID, Reason: "Inbound QC damaged stock quarantined into unsellable",
			})
			if err != nil {
				return domain.ReceivingInspection{}, err
			}
		}

		// Update purchase order line
		_, err = tx.Exec(ctx, `
			UPDATE purchase_order_lines
			SET received_qty = received_qty + $1, damaged_qty = damaged_qty + $2, short_qty = $3
			WHERE po_id = $4 AND sku_id = $5
		`, l.PassedQty, l.DamagedQty, l.ShortQty, poID, l.SkuID)
		if err != nil {
			return domain.ReceivingInspection{}, dependency.Wrap(err)
		}
	}

	// Update PO status
	newStatus := domain.POStatusPartiallyReceived
	var remainingUnresolved int64
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(ordered_qty - (received_qty + damaged_qty + short_qty)), 0)
		FROM purchase_order_lines WHERE po_id = $1
	`, poID).Scan(&remainingUnresolved)
	if err == nil && remainingUnresolved <= 0 {
		newStatus = domain.POStatusCompleted
	}

	_, err = tx.Exec(ctx, `
		UPDATE purchase_orders SET status = $1, updated_at = NOW() WHERE po_id = $2
	`, newStatus, poID)
	if err != nil {
		return domain.ReceivingInspection{}, dependency.Wrap(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.ReceivingInspection{}, dependency.Wrap(err)
	}

	return insp, nil
}

// ListReceivingInspections lists inspections for a warehouse or PO.
func (r *InventoryRepository) ListReceivingInspections(ctx context.Context, warehouseID, poID string) ([]domain.ReceivingInspection, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		list := make([]domain.ReceivingInspection, 0)
		for _, insp := range r.inspections {
			if (warehouseID == "" || insp.WarehouseID == warehouseID) && (poID == "" || insp.POID == poID) {
				list = append(list, *insp)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		return list, nil
	}

	var sb strings.Builder
	sb.WriteString(`
		SELECT inspection_id, po_id, warehouse_id, inspector_id, status, notes, created_at
		FROM receiving_inspections WHERE 1=1
	`)
	args := []any{}
	idx := 1
	if warehouseID != "" {
		sb.WriteString(fmt.Sprintf(" AND warehouse_id = $%d", idx))
		args = append(args, warehouseID)
		idx++
	}
	if poID != "" {
		sb.WriteString(fmt.Sprintf(" AND po_id = $%d", idx))
		args = append(args, poID)
		idx++
	}
	sb.WriteString(" ORDER BY created_at DESC")

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.ReceivingInspection, 0)
	for rows.Next() {
		var insp domain.ReceivingInspection
		if err := rows.Scan(&insp.InspectionID, &insp.POID, &insp.WarehouseID, &insp.InspectorID, &insp.Status, &insp.Notes, &insp.CreatedAt); err != nil {
			return nil, dependency.Wrap(err)
		}
		list = append(list, insp)
	}
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	return list, nil
}
