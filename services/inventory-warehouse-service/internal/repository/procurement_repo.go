package repository

import (
	"context"
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
