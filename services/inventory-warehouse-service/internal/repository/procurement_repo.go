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

// CreateSupplierReturn creates a draft supplier return record for unsellable/damaged goods.
func (r *InventoryRepository) CreateSupplierReturn(ctx context.Context, ret domain.SupplierReturn) (domain.SupplierReturn, error) {
	if len(ret.Lines) == 0 {
		return domain.SupplierReturn{}, errors.New("at least one return line is required")
	}
	for _, l := range ret.Lines {
		if l.SkuID == "" || l.Quantity <= 0 {
			return domain.SupplierReturn{}, errors.New("each line needs a valid skuId and positive quantity")
		}
	}
	if ret.ReturnID == "" {
		ret.ReturnID = fmt.Sprintf("RTV-%s-%04d", time.Now().Format("20060102"), time.Now().UnixNano()%10000)
	}
	if ret.WarehouseID == "" {
		ret.WarehouseID = domain.DefaultWarehouseID
	}
	ret.Status = "DRAFT"
	now := r.now().UTC()
	ret.CreatedAt = now
	ret.UpdatedAt = now

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if sup, ok := r.suppliers[ret.SupplierID]; ok {
			ret.SupplierName = sup.Name
		}
		cp := ret
		r.supplierReturns[ret.ReturnID] = &cp
		return ret, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.SupplierReturn{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO supplier_returns (return_id, supplier_id, warehouse_id, po_id, status, created_by, notes, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, ret.ReturnID, ret.SupplierID, ret.WarehouseID, ret.POID, ret.Status, ret.CreatedBy, ret.Notes, ret.CreatedAt, ret.UpdatedAt)
	if err != nil {
		return domain.SupplierReturn{}, dependency.Wrap(err)
	}

	for _, l := range ret.Lines {
		_, err = tx.Exec(ctx, `
			INSERT INTO supplier_return_lines (return_id, sku_id, quantity, reason)
			VALUES ($1, $2, $3, $4)
		`, ret.ReturnID, l.SkuID, l.Quantity, l.Reason)
		if err != nil {
			return domain.SupplierReturn{}, dependency.Wrap(err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.SupplierReturn{}, dependency.Wrap(err)
	}

	return ret, nil
}

// DispatchSupplierReturn executes the supplier return, decreasing onHand and unsellable stock.
// Invariant I01 ensures available stock is unchanged.
func (r *InventoryRepository) DispatchSupplierReturn(ctx context.Context, returnID, actorID, trackingNumber string) (domain.SupplierReturn, error) {
	ret, ok, err := r.GetSupplierReturn(ctx, returnID)
	if err != nil {
		return domain.SupplierReturn{}, err
	}
	if !ok {
		return domain.SupplierReturn{}, errors.New("supplier return not found")
	}
	if ret.Status != "DRAFT" {
		return domain.SupplierReturn{}, errors.New("supplier return is not in DRAFT status")
	}

	now := r.now().UTC()
	ret.Status = "DISPATCHED"
	ret.DispatchedBy = actorID
	ret.DispatchedAt = &now
	ret.TrackingNumber = trackingNumber
	ret.UpdatedAt = now

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, l := range ret.Lines {
			if err := r.engine.DispatchSupplierReturn(ret.WarehouseID, l.SkuID, l.Quantity); err != nil {
				return domain.SupplierReturn{}, err
			}
			r.appendMovement(domain.Movement{
				SkuID: l.SkuID, WarehouseID: ret.WarehouseID, Type: domain.MovementSupplierReturn, Qty: l.Quantity,
				RefType: "RTV", RefID: returnID, ActorID: actorID, Reason: "Supplier return dispatched: " + l.Reason,
			})
		}
		r.supplierReturns[returnID] = &ret
		return ret, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.SupplierReturn{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, l := range ret.Lines {
		tag, err := tx.Exec(ctx, `
			UPDATE stock_items
			SET on_hand = on_hand - $1, unsellable = unsellable - $1, version = version + 1, updated_at = NOW()
			WHERE sku_id = $2 AND warehouse_id = $3 AND on_hand >= $1 AND unsellable >= $1
		`, l.Quantity, l.SkuID, ret.WarehouseID)
		if err != nil {
			return domain.SupplierReturn{}, dependency.Wrap(err)
		}
		if tag.RowsAffected() == 0 {
			return domain.SupplierReturn{}, domain.ErrInsufficientStock
		}

		err = insertMovement(ctx, tx, domain.Movement{
			SkuID: l.SkuID, WarehouseID: ret.WarehouseID, Type: domain.MovementSupplierReturn, Qty: l.Quantity,
			RefType: "RTV", RefID: returnID, ActorID: actorID, Reason: "Supplier return dispatched: " + l.Reason,
		})
		if err != nil {
			return domain.SupplierReturn{}, err
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE supplier_returns
		SET status = $1, dispatched_by = $2, dispatched_at = $3, tracking_number = $4, updated_at = $5
		WHERE return_id = $6
	`, ret.Status, ret.DispatchedBy, ret.DispatchedAt, ret.TrackingNumber, ret.UpdatedAt, returnID)
	if err != nil {
		return domain.SupplierReturn{}, dependency.Wrap(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.SupplierReturn{}, dependency.Wrap(err)
	}

	return ret, nil
}

// GetSupplierReturn retrieves a supplier return and its lines.
func (r *InventoryRepository) GetSupplierReturn(ctx context.Context, returnID string) (domain.SupplierReturn, bool, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		ret, ok := r.supplierReturns[returnID]
		if !ok {
			return domain.SupplierReturn{}, false, nil
		}
		return *ret, true, nil
	}

	var ret domain.SupplierReturn
	err := r.pool.QueryRow(ctx, `
		SELECT r.return_id, r.supplier_id, COALESCE(s.name, ''), r.warehouse_id, r.po_id, r.status,
		       r.created_by, r.dispatched_by, r.dispatched_at, r.tracking_number, r.notes, r.created_at, r.updated_at
		FROM supplier_returns r
		LEFT JOIN suppliers s ON r.supplier_id = s.supplier_id
		WHERE r.return_id = $1
	`, returnID).Scan(
		&ret.ReturnID, &ret.SupplierID, &ret.SupplierName, &ret.WarehouseID, &ret.POID, &ret.Status,
		&ret.CreatedBy, &ret.DispatchedBy, &ret.DispatchedAt, &ret.TrackingNumber, &ret.Notes, &ret.CreatedAt, &ret.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return domain.SupplierReturn{}, false, nil
	}
	if err != nil {
		return domain.SupplierReturn{}, false, dependency.Wrap(err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, return_id, sku_id, quantity, reason
		FROM supplier_return_lines WHERE return_id = $1 ORDER BY id
	`, returnID)
	if err != nil {
		return domain.SupplierReturn{}, false, dependency.Wrap(err)
	}
	defer rows.Close()

	ret.Lines = make([]domain.SupplierReturnLine, 0)
	for rows.Next() {
		var l domain.SupplierReturnLine
		if err := rows.Scan(&l.ID, &l.ReturnID, &l.SkuID, &l.Quantity, &l.Reason); err != nil {
			return domain.SupplierReturn{}, false, dependency.Wrap(err)
		}
		ret.Lines = append(ret.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return domain.SupplierReturn{}, false, dependency.Wrap(err)
	}

	return ret, true, nil
}

// ListSupplierReturns lists supplier returns by warehouse or supplier.
func (r *InventoryRepository) ListSupplierReturns(ctx context.Context, warehouseID, supplierID string) ([]domain.SupplierReturn, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		list := make([]domain.SupplierReturn, 0)
		for _, ret := range r.supplierReturns {
			if (warehouseID == "" || ret.WarehouseID == warehouseID) && (supplierID == "" || ret.SupplierID == supplierID) {
				list = append(list, *ret)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		return list, nil
	}

	var sb strings.Builder
	sb.WriteString(`
		SELECT r.return_id, r.supplier_id, COALESCE(s.name, ''), r.warehouse_id, r.po_id, r.status,
		       r.created_by, r.dispatched_by, r.dispatched_at, r.tracking_number, r.notes, r.created_at, r.updated_at
		FROM supplier_returns r
		LEFT JOIN suppliers s ON r.supplier_id = s.supplier_id
		WHERE 1=1
	`)
	args := []any{}
	idx := 1
	if warehouseID != "" {
		sb.WriteString(fmt.Sprintf(" AND r.warehouse_id = $%d", idx))
		args = append(args, warehouseID)
		idx++
	}
	if supplierID != "" {
		sb.WriteString(fmt.Sprintf(" AND r.supplier_id = $%d", idx))
		args = append(args, supplierID)
		idx++
	}
	sb.WriteString(" ORDER BY r.created_at DESC")

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.SupplierReturn, 0)
	for rows.Next() {
		var ret domain.SupplierReturn
		if err := rows.Scan(
			&ret.ReturnID, &ret.SupplierID, &ret.SupplierName, &ret.WarehouseID, &ret.POID, &ret.Status,
			&ret.CreatedBy, &ret.DispatchedBy, &ret.DispatchedAt, &ret.TrackingNumber, &ret.Notes, &ret.CreatedAt, &ret.UpdatedAt,
		); err != nil {
			return nil, dependency.Wrap(err)
		}
		list = append(list, ret)
	}
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	return list, nil
}

// CreateStocktake begins a new physical stocktake session.
func (r *InventoryRepository) CreateStocktake(ctx context.Context, warehouseID, startedBy, notes string) (domain.Stocktake, error) {
	if warehouseID == "" {
		warehouseID = domain.DefaultWarehouseID
	}
	id := fmt.Sprintf("STK-%s-%04d", time.Now().Format("20060102"), time.Now().UnixNano()%10000)
	now := r.now().UTC()
	stk := domain.Stocktake{
		StocktakeID: id,
		WarehouseID: warehouseID,
		Status:      "IN_PROGRESS",
		StartedBy:   startedBy,
		Notes:       notes,
		CreatedAt:   now,
	}

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		cp := stk
		r.stocktakes[id] = &cp
		return stk, nil
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO stocktakes (stocktake_id, warehouse_id, status, started_by, notes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, stk.StocktakeID, stk.WarehouseID, stk.Status, stk.StartedBy, stk.Notes, stk.CreatedAt)
	if err != nil {
		return domain.Stocktake{}, dependency.Wrap(err)
	}
	return stk, nil
}

// SubmitStocktakeCount records counted stock on shelf.
// If counted < system (shortage), it immediately blocks sales for the discrepancy (I01)
// by quarantining the shortage into unsellable stock while generating a maker != checker adjustment request.
func (r *InventoryRepository) SubmitStocktakeCount(ctx context.Context, stocktakeID string, counts []domain.StocktakeLine) (domain.Stocktake, []domain.StockAdjustment, error) {
	stk, ok, err := r.GetStocktake(ctx, stocktakeID)
	if err != nil {
		return domain.Stocktake{}, nil, err
	}
	if !ok {
		return domain.Stocktake{}, nil, errors.New("stocktake session not found")
	}
	if stk.Status != "IN_PROGRESS" {
		return domain.Stocktake{}, nil, errors.New("stocktake is not IN_PROGRESS")
	}

	now := r.now().UTC()
	stk.Status = "SUBMITTED"
	stk.CompletedAt = &now

	adjustments := make([]domain.StockAdjustment, 0)
	processedLines := make([]domain.StocktakeLine, 0, len(counts))

	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()

		for _, c := range counts {
			st, ok := r.engine.GetStock(stk.WarehouseID, c.SkuID)
			sysOnHand := int64(0)
			if ok {
				sysOnHand = st.OnHand
			}
			diff := c.CountedOnHand - sysOnHand
			diffAmount := diff * c.UnitCostVND
			c.SystemOnHand = sysOnHand
			c.DiffQty = diff
			c.DiffAmountVND = diffAmount
			processedLines = append(processedLines, c)

			if diff != 0 {
				adjID := fmt.Sprintf("ADJ-%s-%04d", time.Now().Format("20060102"), time.Now().UnixNano()%10000)
				adj := domain.StockAdjustment{
					AdjustmentID:  adjID,
					StocktakeID:   stocktakeID,
					WarehouseID:   stk.WarehouseID,
					SkuID:         c.SkuID,
					DiffQty:       diff,
					UnitCostVND:   c.UnitCostVND,
					DiffAmountVND: diffAmount,
					Status:        "PENDING_APPROVAL",
					MakerID:       stk.StartedBy,
					Reason:        fmt.Sprintf("Discrepancy identified during stocktake %s", stocktakeID),
					CreatedAt:     now,
				}
				// If shortage, immediately block sales for discrepancy (I01)
				if diff < 0 {
					_ = r.engine.QuarantineDiscrepancy(stk.WarehouseID, c.SkuID, -diff)
				}
				r.adjustments[adjID] = &adj
				adjustments = append(adjustments, adj)
			}
		}

		stk.Lines = processedLines
		r.stocktakes[stocktakeID] = &stk
		return stk, adjustments, nil
	}

	// Real Postgres transaction
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Stocktake{}, nil, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, c := range counts {
		var sysOnHand int64
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(on_hand, 0) FROM stock_items WHERE sku_id = $1 AND warehouse_id = $2
		`, c.SkuID, stk.WarehouseID).Scan(&sysOnHand)

		diff := c.CountedOnHand - sysOnHand
		diffAmount := diff * c.UnitCostVND
		c.SystemOnHand = sysOnHand
		c.DiffQty = diff
		c.DiffAmountVND = diffAmount
		processedLines = append(processedLines, c)

		_, err = tx.Exec(ctx, `
			INSERT INTO stocktake_lines (stocktake_id, sku_id, system_on_hand, counted_on_hand, diff_qty, unit_cost_vnd, diff_amount_vnd)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, stocktakeID, c.SkuID, c.SystemOnHand, c.CountedOnHand, c.DiffQty, c.UnitCostVND, c.DiffAmountVND)
		if err != nil {
			return domain.Stocktake{}, nil, dependency.Wrap(err)
		}

		if diff != 0 {
			adjID := fmt.Sprintf("ADJ-%s-%04d", time.Now().Format("20060102"), time.Now().UnixNano()%10000)
			adj := domain.StockAdjustment{
				AdjustmentID:  adjID,
				StocktakeID:   stocktakeID,
				WarehouseID:   stk.WarehouseID,
				SkuID:         c.SkuID,
				DiffQty:       diff,
				UnitCostVND:   c.UnitCostVND,
				DiffAmountVND: diffAmount,
				Status:        "PENDING_APPROVAL",
				MakerID:       stk.StartedBy,
				Reason:        fmt.Sprintf("Discrepancy identified during stocktake %s", stocktakeID),
				CreatedAt:     now,
			}

			// If shortage: block sales immediately by quarantining into unsellable (I01)
			if diff < 0 {
				shortageQty := -diff
				_, err = tx.Exec(ctx, `
					UPDATE stock_items
					SET unsellable = unsellable + $1, version = version + 1, updated_at = NOW()
					WHERE sku_id = $2 AND warehouse_id = $3
				`, shortageQty, c.SkuID, stk.WarehouseID)
				if err != nil {
					return domain.Stocktake{}, nil, dependency.Wrap(err)
				}
			}

			_, err = tx.Exec(ctx, `
				INSERT INTO stock_adjustments (adjustment_id, stocktake_id, warehouse_id, sku_id, diff_qty, unit_cost_vnd, diff_amount_vnd, status, maker_id, reason, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			`, adj.AdjustmentID, adj.StocktakeID, adj.WarehouseID, adj.SkuID, adj.DiffQty, adj.UnitCostVND, adj.DiffAmountVND, adj.Status, adj.MakerID, adj.Reason, adj.CreatedAt)
			if err != nil {
				return domain.Stocktake{}, nil, dependency.Wrap(err)
			}
			adjustments = append(adjustments, adj)
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE stocktakes SET status = $1, completed_at = $2 WHERE stocktake_id = $3
	`, stk.Status, stk.CompletedAt, stocktakeID)
	if err != nil {
		return domain.Stocktake{}, nil, dependency.Wrap(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Stocktake{}, nil, dependency.Wrap(err)
	}

	stk.Lines = processedLines
	return stk, adjustments, nil
}

// DecideStockAdjustment approves or rejects an inventory adjustment enforcing maker != checker P10 and thresholds.
func (r *InventoryRepository) DecideStockAdjustment(ctx context.Context, adjustmentID, approverID string, approverRoles []string, action, note string) (domain.StockAdjustment, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		adj, ok := r.adjustments[adjustmentID]
		if !ok {
			return domain.StockAdjustment{}, errors.New("adjustment not found")
		}
		if err := domain.CanApproveAdjustment(*adj, approverID, approverRoles); err != nil {
			return domain.StockAdjustment{}, err
		}
		now := r.now().UTC()
		adj.CheckerID = approverID
		adj.DecidedAt = &now
		if note != "" {
			adj.Reason = adj.Reason + " | " + note
		}
		if action == "APPROVE" {
			adj.Status = "APPROVED"
			_ = r.engine.ApplyAdjustment(adj.WarehouseID, adj.SkuID, adj.DiffQty, adj.DiffQty < 0)
			r.appendMovement(domain.Movement{
				SkuID: adj.SkuID, WarehouseID: adj.WarehouseID, Type: domain.MovementAdjust, Qty: adj.DiffQty,
				RefType: "ADJUSTMENT", RefID: adjustmentID, ActorID: approverID, Reason: adj.Reason,
			})
		} else {
			adj.Status = "REJECTED"
		}
		r.adjustments[adjustmentID] = adj
		return *adj, nil
	}

	var adj domain.StockAdjustment
	err := r.pool.QueryRow(ctx, `
		SELECT adjustment_id, stocktake_id, warehouse_id, sku_id, diff_qty, unit_cost_vnd, diff_amount_vnd,
		       status, maker_id, checker_id, decided_at, reason, created_at
		FROM stock_adjustments WHERE adjustment_id = $1
	`, adjustmentID).Scan(
		&adj.AdjustmentID, &adj.StocktakeID, &adj.WarehouseID, &adj.SkuID, &adj.DiffQty, &adj.UnitCostVND, &adj.DiffAmountVND,
		&adj.Status, &adj.MakerID, &adj.CheckerID, &adj.DecidedAt, &adj.Reason, &adj.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return domain.StockAdjustment{}, errors.New("adjustment not found")
	}
	if err != nil {
		return domain.StockAdjustment{}, dependency.Wrap(err)
	}

	if err := domain.CanApproveAdjustment(adj, approverID, approverRoles); err != nil {
		return domain.StockAdjustment{}, err
	}

	now := r.now().UTC()
	adj.CheckerID = approverID
	adj.DecidedAt = &now
	if note != "" {
		adj.Reason = adj.Reason + " | " + note
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.StockAdjustment{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if action == "APPROVE" {
		adj.Status = "APPROVED"
		if adj.DiffQty < 0 {
			absShort := -adj.DiffQty
			// OnHand decreases by shortage, and Unsellable decreases by shortage (releasing the quarantine)
			_, err = tx.Exec(ctx, `
				UPDATE stock_items
				SET on_hand = on_hand - $1, unsellable = unsellable - $1, version = version + 1, updated_at = NOW()
				WHERE sku_id = $2 AND warehouse_id = $3
			`, absShort, adj.SkuID, adj.WarehouseID)
			if err != nil {
				return domain.StockAdjustment{}, dependency.Wrap(err)
			}
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE stock_items
				SET on_hand = on_hand + $1, version = version + 1, updated_at = NOW()
				WHERE sku_id = $2 AND warehouse_id = $3
			`, adj.DiffQty, adj.SkuID, adj.WarehouseID)
			if err != nil {
				return domain.StockAdjustment{}, dependency.Wrap(err)
			}
		}

		err = insertMovement(ctx, tx, domain.Movement{
			SkuID: adj.SkuID, WarehouseID: adj.WarehouseID, Type: domain.MovementAdjust, Qty: adj.DiffQty,
			RefType: "ADJUSTMENT", RefID: adjustmentID, ActorID: approverID, Reason: adj.Reason,
		})
		if err != nil {
			return domain.StockAdjustment{}, err
		}
	} else {
		adj.Status = "REJECTED"
	}

	_, err = tx.Exec(ctx, `
		UPDATE stock_adjustments
		SET status = $1, checker_id = $2, decided_at = $3, reason = $4
		WHERE adjustment_id = $5
	`, adj.Status, adj.CheckerID, adj.DecidedAt, adj.Reason, adjustmentID)
	if err != nil {
		return domain.StockAdjustment{}, dependency.Wrap(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.StockAdjustment{}, dependency.Wrap(err)
	}

	return adj, nil
}

// GetStocktake retrieves a stocktake and its lines.
func (r *InventoryRepository) GetStocktake(ctx context.Context, stocktakeID string) (domain.Stocktake, bool, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		stk, ok := r.stocktakes[stocktakeID]
		if !ok {
			return domain.Stocktake{}, false, nil
		}
		return *stk, true, nil
	}

	var stk domain.Stocktake
	err := r.pool.QueryRow(ctx, `
		SELECT stocktake_id, warehouse_id, status, started_by, notes, created_at, completed_at
		FROM stocktakes WHERE stocktake_id = $1
	`, stocktakeID).Scan(&stk.StocktakeID, &stk.WarehouseID, &stk.Status, &stk.StartedBy, &stk.Notes, &stk.CreatedAt, &stk.CompletedAt)
	if err == pgx.ErrNoRows {
		return domain.Stocktake{}, false, nil
	}
	if err != nil {
		return domain.Stocktake{}, false, dependency.Wrap(err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, stocktake_id, sku_id, system_on_hand, counted_on_hand, diff_qty, unit_cost_vnd, diff_amount_vnd
		FROM stocktake_lines WHERE stocktake_id = $1 ORDER BY id
	`, stocktakeID)
	if err != nil {
		return domain.Stocktake{}, false, dependency.Wrap(err)
	}
	defer rows.Close()

	stk.Lines = make([]domain.StocktakeLine, 0)
	for rows.Next() {
		var l domain.StocktakeLine
		if err := rows.Scan(&l.ID, &l.StocktakeID, &l.SkuID, &l.SystemOnHand, &l.CountedOnHand, &l.DiffQty, &l.UnitCostVND, &l.DiffAmountVND); err != nil {
			return domain.Stocktake{}, false, dependency.Wrap(err)
		}
		stk.Lines = append(stk.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return domain.Stocktake{}, false, dependency.Wrap(err)
	}

	return stk, true, nil
}

// ListStocktakes lists stocktakes by warehouse.
func (r *InventoryRepository) ListStocktakes(ctx context.Context, warehouseID string) ([]domain.Stocktake, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		list := make([]domain.Stocktake, 0)
		for _, s := range r.stocktakes {
			if warehouseID == "" || s.WarehouseID == warehouseID {
				list = append(list, *s)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		return list, nil
	}

	var sb strings.Builder
	sb.WriteString(`SELECT stocktake_id, warehouse_id, status, started_by, notes, created_at, completed_at FROM stocktakes WHERE 1=1`)
	args := []any{}
	if warehouseID != "" {
		sb.WriteString(" AND warehouse_id = $1")
		args = append(args, warehouseID)
	}
	sb.WriteString(" ORDER BY created_at DESC")

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.Stocktake, 0)
	for rows.Next() {
		var s domain.Stocktake
		if err := rows.Scan(&s.StocktakeID, &s.WarehouseID, &s.Status, &s.StartedBy, &s.Notes, &s.CreatedAt, &s.CompletedAt); err != nil {
			return nil, dependency.Wrap(err)
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	return list, nil
}

// ListStockAdjustments lists adjustments by warehouse and status.
func (r *InventoryRepository) ListStockAdjustments(ctx context.Context, warehouseID, status string) ([]domain.StockAdjustment, error) {
	if r.pool == nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		list := make([]domain.StockAdjustment, 0)
		for _, a := range r.adjustments {
			if (warehouseID == "" || a.WarehouseID == warehouseID) && (status == "" || a.Status == status) {
				list = append(list, *a)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		return list, nil
	}

	var sb strings.Builder
	sb.WriteString(`
		SELECT adjustment_id, stocktake_id, warehouse_id, sku_id, diff_qty, unit_cost_vnd, diff_amount_vnd,
		       status, maker_id, checker_id, decided_at, reason, created_at
		FROM stock_adjustments WHERE 1=1
	`)
	args := []any{}
	idx := 1
	if warehouseID != "" {
		sb.WriteString(fmt.Sprintf(" AND warehouse_id = $%d", idx))
		args = append(args, warehouseID)
		idx++
	}
	if status != "" {
		sb.WriteString(fmt.Sprintf(" AND status = $%d", idx))
		args = append(args, status)
		idx++
	}
	sb.WriteString(" ORDER BY created_at DESC")

	rows, err := r.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()

	list := make([]domain.StockAdjustment, 0)
	for rows.Next() {
		var a domain.StockAdjustment
		if err := rows.Scan(&a.AdjustmentID, &a.StocktakeID, &a.WarehouseID, &a.SkuID, &a.DiffQty, &a.UnitCostVND, &a.DiffAmountVND,
			&a.Status, &a.MakerID, &a.CheckerID, &a.DecidedAt, &a.Reason, &a.CreatedAt); err != nil {
			return nil, dependency.Wrap(err)
		}
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	return list, nil
}
