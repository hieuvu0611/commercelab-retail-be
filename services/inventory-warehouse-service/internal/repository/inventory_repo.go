package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrReservationNotFound = errors.New("không tìm thấy phiên giữ tồn kho của đơn hàng")
	ErrWarehouseRequired   = errors.New("warehouseId is required to reserve stock")
)

const (
	// ReservationTTL is how long unpaid stock is held (P03); ExceptionHoldTTL is the whole hold of an order
	// whose payment is unknown, counted from the reservation (P05, SC05).
	ReservationTTL   = 10 * time.Minute
	ExceptionHoldTTL = 30 * time.Minute

	statusActive    = "ACTIVE_10M"
	statusException = "EXCEPTION_HOLD_30M"
)

type ReservationRecord struct {
	ReservationID string               `json:"reservationId"`
	OrderID       string               `json:"orderId"`
	WarehouseID   string               `json:"warehouseId"`
	Status        string               `json:"status"` // ACTIVE_10M, EXCEPTION_HOLD_30M, COMMITTED, RELEASED, HANDED_OVER
	Lines         []domain.ReserveLine `json:"lines"`
	ExpiresAt     time.Time            `json:"expiresAt"`
}

type InventoryRepository struct {
	pool            *pgxpool.Pool
	engine          *domain.LedgerEngine
	mu              sync.Mutex
	reservations    map[string][]*ReservationRecord
	movements       []domain.Movement // memory mode only; guarded by mu
	suppliers       map[string]*domain.Supplier
	purchaseOrders  map[string]*domain.PurchaseOrder
	inspections     map[string]*domain.ReceivingInspection
	supplierReturns map[string]*domain.SupplierReturn
	stocktakes      map[string]*domain.Stocktake
	adjustments     map[string]*domain.StockAdjustment
	transfers       map[string]*domain.WarehouseTransfer
	now             func() time.Time
}

// SetClock replaces the time source (tests use a fake clock for expiry rules).
func (r *InventoryRepository) SetClock(now func() time.Time) { r.now = now }

func NewInventoryRepository(pool *pgxpool.Pool) *InventoryRepository {
	seed := []domain.StockItem{
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HCM-01", OnHand: 10, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
		{SkuID: "SKU-BOTTLE-BLUE-500ML", WarehouseID: "WH-HCM-01", OnHand: 20, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HN-01", OnHand: 5, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
		{SkuID: "SKU-BOTTLE-BLUE-500ML", WarehouseID: "WH-HN-01", OnHand: 10, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
	}
	repo := &InventoryRepository{
		pool:            pool,
		engine:          domain.NewLedgerEngine(seed),
		reservations:    make(map[string][]*ReservationRecord),
		suppliers:       make(map[string]*domain.Supplier),
		purchaseOrders:  make(map[string]*domain.PurchaseOrder),
		inspections:     make(map[string]*domain.ReceivingInspection),
		supplierReturns: make(map[string]*domain.SupplierReturn),
		stocktakes:      make(map[string]*domain.Stocktake),
		adjustments:     make(map[string]*domain.StockAdjustment),
		transfers:       make(map[string]*domain.WarehouseTransfer),
		now:             time.Now,
	}
	// Seed memory suppliers
	repo.suppliers["SUP-EVEREST-01"] = &domain.Supplier{
		SupplierID: "SUP-EVEREST-01", Name: "Everest Outdoor Gear Co.", Code: "EVEREST",
		ContactEmail: "supply@everestgear.vn", Phone: "0901234567", Address: "Lot 4, Tan Binh Industrial Park, HCMC",
		Active: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	repo.suppliers["SUP-HYDRO-01"] = &domain.Supplier{
		SupplierID: "SUP-HYDRO-01", Name: "Hydro Flask Vietnam Ltd.", Code: "HYDRO",
		ContactEmail: "sales@hydroflask.vn", Phone: "0912345678", Address: "District 7, HCMC",
		Active: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	for _, it := range seed { // the memory ledger explains the seeded stock like the migration does for Postgres
		repo.appendMovement(domain.Movement{
			SkuID: it.SkuID, WarehouseID: it.WarehouseID, Type: domain.MovementOpening, Qty: it.OnHand,
			ActorID: "system:seed", Reason: "memory seed",
		})
	}
	return repo
}

func (r *InventoryRepository) GetStock(ctx context.Context, warehouseID, skuID string) (domain.StockItem, bool, error) {
	if warehouseID == "" {
		warehouseID = domain.DefaultWarehouseID
	}
	if r.pool != nil {
		var st domain.StockItem
		err := r.pool.QueryRow(ctx, `
			SELECT sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit, version
			FROM stock_items
			WHERE sku_id = $1 AND warehouse_id = $2
		`, skuID, warehouseID).Scan(&st.SkuID, &st.WarehouseID, &st.OnHand, &st.Unsellable, &st.Reserved, &st.Committed, &st.InTransit, &st.Version)
		if err == nil {
			return st, true, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StockItem{}, false, nil
		}
		return domain.StockItem{}, false, dependency.Wrap(err)
	}
	st, ok := r.engine.GetStock(warehouseID, skuID)
	return st, ok, nil
}

func reservationIDFor(orderID, warehouseID string, totalAllocs int) string {
	if totalAllocs == 1 {
		return "RES-" + orderID
	}
	return "RES-" + orderID + "-" + warehouseID
}

func (r *InventoryRepository) ReserveForOrder(ctx context.Context, orderID, warehouseID string, lines []domain.ReserveLine) (*ReservationRecord, error) {
	if warehouseID == "" {
		return nil, ErrWarehouseRequired
	}
	recs, err := r.ReserveAllocations(ctx, orderID, []domain.WarehouseAllocation{{
		WarehouseID: warehouseID,
		Lines:       lines,
	}})
	if err != nil {
		return nil, err
	}
	cp := recs[0]
	return &cp, nil
}

// ReserveAllocations atomically reserves stock for an order across 1 or 2 warehouses (B02, P03, I01).
func (r *InventoryRepository) ReserveAllocations(ctx context.Context, orderID string, rawAllocs []domain.WarehouseAllocation) ([]ReservationRecord, error) {
	if len(rawAllocs) == 0 || len(rawAllocs) > 2 {
		return nil, domain.ErrInvalidLines
	}
	seenWH := map[string]bool{}
	allocs := make([]domain.WarehouseAllocation, len(rawAllocs))
	for i, a := range rawAllocs {
		if a.WarehouseID == "" {
			return nil, ErrWarehouseRequired
		}
		if seenWH[a.WarehouseID] {
			return nil, domain.ErrInvalidLines
		}
		seenWH[a.WarehouseID] = true
		norm, err := domain.NormalizeLines(a.Lines)
		if err != nil {
			return nil, err
		}
		allocs[i] = domain.WarehouseAllocation{WarehouseID: a.WarehouseID, Lines: norm}
	}
	// Sort by warehouse_id so row locking order (warehouse_id, sku_id) is deterministic across all transactions.
	sort.Slice(allocs, func(i, j int) bool { return allocs[i].WarehouseID < allocs[j].WarehouseID })
	expiresAt := r.now().UTC().Add(ReservationTTL)

	if r.pool != nil {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		// Check if order already has reservations (idempotent retry).
		existingRows, err := loadReservationsTx(ctx, tx, orderID, "")
		if err != nil {
			return nil, err
		}
		if len(existingRows) > 0 {
			_ = tx.Commit(ctx)
			return existingRows, nil
		}

		// Lock and verify availability in deterministic (warehouse_id, sku_id) order.
		for _, alloc := range allocs {
			for _, line := range alloc.Lines {
				var onHand, unsellable, reserved, committed int64
				err := tx.QueryRow(ctx, `
					SELECT on_hand, unsellable, reserved, committed
					FROM stock_items
					WHERE sku_id = $1 AND warehouse_id = $2
					FOR UPDATE
				`, line.SkuID, alloc.WarehouseID).Scan(&onHand, &unsellable, &reserved, &committed)
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, domain.ErrInsufficientStock
				}
				if err != nil {
					return nil, dependency.Wrap(err)
				}
				if (onHand - unsellable - reserved - committed) < line.Quantity {
					return nil, domain.ErrInsufficientStock
				}
			}
		}

		out := make([]ReservationRecord, 0, len(allocs))
		for _, alloc := range allocs {
			linesJSON, _ := json.Marshal(alloc.Lines)
			resID := reservationIDFor(orderID, alloc.WarehouseID, len(allocs))
			claim, err := tx.Exec(ctx, `
				INSERT INTO reservations (reservation_id, order_id, warehouse_id, status, lines_json, expires_at)
				VALUES ($1, $2, $3, 'ACTIVE_10M', $4::jsonb, $5)
				ON CONFLICT (order_id, warehouse_id) DO NOTHING
			`, resID, orderID, alloc.WarehouseID, string(linesJSON), expiresAt)
			if err != nil {
				return nil, dependency.Wrap(err)
			}
			if claim.RowsAffected() == 0 {
				existingRows, err := loadReservationsTx(ctx, tx, orderID, "")
				if err != nil {
					return nil, err
				}
				_ = tx.Commit(ctx)
				return existingRows, nil
			}
			for _, line := range alloc.Lines {
				if _, err := tx.Exec(ctx, `
					UPDATE stock_items
					SET reserved = reserved + $1, version = version + 1, updated_at = NOW()
					WHERE sku_id = $2 AND warehouse_id = $3
				`, line.Quantity, line.SkuID, alloc.WarehouseID); err != nil {
					return nil, dependency.Wrap(err)
				}
				if err := insertMovement(ctx, tx, lineMovement(domain.MovementReserve, alloc.WarehouseID, orderID, line, "order reservation")); err != nil {
					return nil, err
				}
			}
			out = append(out, ReservationRecord{
				ReservationID: resID,
				OrderID:       orderID,
				WarehouseID:   alloc.WarehouseID,
				Status:        statusActive,
				Lines:         alloc.Lines,
				ExpiresAt:     expiresAt,
			})
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, dependency.Wrap(err)
		}
		return out, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.reservations[orderID]; ok && len(existing) > 0 {
		out := make([]ReservationRecord, len(existing))
		for i, rec := range existing {
			out[i] = *rec
		}
		return out, nil
	}
	if err := r.engine.ReserveAllocations(allocs); err != nil {
		return nil, err
	}
	out := make([]ReservationRecord, 0, len(allocs))
	recs := make([]*ReservationRecord, 0, len(allocs))
	for _, alloc := range allocs {
		r.logLines(domain.MovementReserve, alloc.WarehouseID, orderID, alloc.Lines, "order reservation")
		rec := &ReservationRecord{
			ReservationID: reservationIDFor(orderID, alloc.WarehouseID, len(allocs)),
			OrderID:       orderID,
			WarehouseID:   alloc.WarehouseID,
			Status:        statusActive,
			Lines:         alloc.Lines,
			ExpiresAt:     expiresAt,
		}
		recs = append(recs, rec)
		out = append(out, *rec)
	}
	r.reservations[orderID] = recs
	return out, nil
}

type resQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadReservationsTx(ctx context.Context, q resQueryer, orderID, warehouseID string) ([]ReservationRecord, error) {
	rows, err := q.Query(ctx, `
		SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
		FROM reservations
		WHERE order_id = $1 AND ($2 = '' OR warehouse_id = $2)
		ORDER BY warehouse_id
		FOR UPDATE
	`, orderID, warehouseID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	var out []ReservationRecord
	for rows.Next() {
		var rec ReservationRecord
		var rawLines []byte
		if err := rows.Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt); err != nil {
			return nil, dependency.Wrap(err)
		}
		_ = json.Unmarshal(rawLines, &rec.Lines)
		out = append(out, rec)
	}
	return out, dependency.Wrap(rows.Err())
}

func (r *InventoryRepository) CommitOrderReservation(ctx context.Context, orderID string) (*ReservationRecord, error) {
	if r.pool != nil {
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
		allCommitted := true
		for _, rec := range recs {
			if rec.Status != "COMMITTED" {
				allCommitted = false
				break
			}
		}
		if allCommitted {
			_ = tx.Commit(ctx)
			return &recs[0], nil
		}
		for i := range recs {
			rec := &recs[i]
			if rec.Status == "COMMITTED" {
				continue
			}
			if rec.Status != statusActive && rec.Status != statusException {
				return nil, domain.ErrInvalidReservationState
			}
			for _, line := range rec.Lines {
				if _, err := tx.Exec(ctx, `
					UPDATE stock_items
					SET reserved = reserved - $1, committed = committed + $1, version = version + 1, updated_at = NOW()
					WHERE sku_id = $2 AND warehouse_id = $3
				`, line.Quantity, line.SkuID, rec.WarehouseID); err != nil {
					return nil, dependency.Wrap(err)
				}
				if err := insertMovement(ctx, tx, lineMovement(domain.MovementCommit, rec.WarehouseID, orderID, line, "payment confirmed")); err != nil {
					return nil, err
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE reservations SET status = 'COMMITTED', version = version + 1 WHERE reservation_id = $1
			`, rec.ReservationID); err != nil {
				return nil, dependency.Wrap(err)
			}
			rec.Status = "COMMITTED"
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, dependency.Wrap(err)
		}
		return &recs[0], nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	recs, ok := r.reservations[orderID]
	if !ok || len(recs) == 0 {
		return nil, ErrReservationNotFound
	}
	allCommitted := true
	for _, rec := range recs {
		if rec.Status != "COMMITTED" {
			allCommitted = false
			break
		}
	}
	if allCommitted {
		cp := *recs[0]
		return &cp, nil
	}
	for _, rec := range recs {
		if rec.Status == "COMMITTED" {
			continue
		}
		if rec.Status != statusActive && rec.Status != statusException {
			return nil, domain.ErrInvalidReservationState
		}
		if err := r.engine.CommitReservation(rec.WarehouseID, rec.Lines); err != nil {
			return nil, err
		}
		r.logLines(domain.MovementCommit, rec.WarehouseID, orderID, rec.Lines, "payment confirmed")
		rec.Status = "COMMITTED"
	}
	cp := *recs[0]
	return &cp, nil
}

func (r *InventoryRepository) ReleaseOrderReservation(ctx context.Context, orderID string) (*ReservationRecord, error) {
	if r.pool != nil {
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
		allReleased := true
		for _, rec := range recs {
			if rec.Status != "RELEASED" {
				allReleased = false
				break
			}
		}
		if allReleased {
			_ = tx.Commit(ctx)
			return &recs[0], nil
		}
		for i := range recs {
			rec := &recs[i]
			if rec.Status == "RELEASED" {
				continue
			}
			if rec.Status != statusActive && rec.Status != statusException {
				return nil, domain.ErrInvalidReservationState
			}
			if err := releaseHeldStock(ctx, tx, rec, domain.SystemActor, "reservation released"); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, dependency.Wrap(err)
		}
		return &recs[0], nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	recs, ok := r.reservations[orderID]
	if !ok || len(recs) == 0 {
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
		cp := *recs[0]
		return &cp, nil
	}
	for _, rec := range recs {
		if rec.Status == "RELEASED" {
			continue
		}
		if rec.Status != statusActive && rec.Status != statusException {
			return nil, domain.ErrInvalidReservationState
		}
		if err := r.engine.ReleaseReservation(rec.WarehouseID, rec.Lines); err != nil {
			return nil, err
		}
		r.logLines(domain.MovementRelease, rec.WarehouseID, orderID, rec.Lines, "reservation released")
		rec.Status = "RELEASED"
	}
	cp := *recs[0]
	return &cp, nil
}

// GetReservation returns the reservation of an order without changing it.
func (r *InventoryRepository) GetReservation(ctx context.Context, orderID string) (*ReservationRecord, error) {
	return r.GetReservationForWarehouse(ctx, orderID, "")
}

// GetReservationForWarehouse returns the order's reservation in warehouseID (or the first one if warehouseID is empty).
func (r *InventoryRepository) GetReservationForWarehouse(ctx context.Context, orderID, warehouseID string) (*ReservationRecord, error) {
	if r.pool != nil {
		var rec ReservationRecord
		var rawLines []byte
		err := r.pool.QueryRow(ctx, `
			SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
			FROM reservations
			WHERE order_id = $1 AND ($2 = '' OR warehouse_id = $2)
			ORDER BY warehouse_id
			LIMIT 1
		`, orderID, warehouseID).Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrReservationNotFound
		}
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		_ = json.Unmarshal(rawLines, &rec.Lines)
		return &rec, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	recs, ok := r.reservations[orderID]
	if !ok || len(recs) == 0 {
		return nil, ErrReservationNotFound
	}
	for _, rec := range recs {
		if warehouseID == "" || rec.WarehouseID == warehouseID {
			cp := *rec
			return &cp, nil
		}
	}
	return nil, ErrReservationNotFound
}

func (r *InventoryRepository) HandoverOrderShipment(ctx context.Context, orderID string) (*ReservationRecord, error) {
	return r.HandoverOrderShipmentForWarehouse(ctx, orderID, "")
}

// HandoverOrderShipmentForWarehouse dispatches the reservation of one warehouse (or the order's single reservation when warehouseID == "").
func (r *InventoryRepository) HandoverOrderShipmentForWarehouse(ctx context.Context, orderID, warehouseID string) (*ReservationRecord, error) {
	if r.pool != nil {
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
		rec := recs[0]
		if rec.Status == "HANDED_OVER" {
			return &rec, nil
		}
		if rec.Status != "COMMITTED" {
			return nil, domain.ErrInvalidReservationState
		}
		for _, line := range rec.Lines {
			if _, err := tx.Exec(ctx, `
				UPDATE stock_items
				SET on_hand = on_hand - $1, committed = committed - $1, version = version + 1, updated_at = NOW()
				WHERE sku_id = $2 AND warehouse_id = $3
			`, line.Quantity, line.SkuID, rec.WarehouseID); err != nil {
				return nil, dependency.Wrap(err)
			}
			if err := insertMovement(ctx, tx, lineMovement(domain.MovementHandover, rec.WarehouseID, orderID, line, "handed over to courier")); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE reservations SET status = 'HANDED_OVER', version = version + 1 WHERE reservation_id = $1
		`, rec.ReservationID); err != nil {
			return nil, dependency.Wrap(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, dependency.Wrap(err)
		}
		rec.Status = "HANDED_OVER"
		return &rec, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	recs, ok := r.reservations[orderID]
	if !ok || len(recs) == 0 {
		return nil, ErrReservationNotFound
	}
	var target *ReservationRecord
	for _, rec := range recs {
		if warehouseID == "" || rec.WarehouseID == warehouseID {
			target = rec
			break
		}
	}
	if target == nil {
		return nil, ErrReservationNotFound
	}
	if target.Status == "HANDED_OVER" {
		cp := *target
		return &cp, nil
	}
	if target.Status != "COMMITTED" {
		return nil, domain.ErrInvalidReservationState
	}
	if err := r.engine.HandoverShipment(target.WarehouseID, target.Lines); err != nil {
		return nil, err
	}
	r.logLines(domain.MovementHandover, target.WarehouseID, orderID, target.Lines, "handed over to courier")
	target.Status = "HANDED_OVER"
	cp := *target
	return &cp, nil
}

// ReceiveReturnedItem implements SC07: OnHand +qty, Unsellable +qty, Available unchanged.
func (r *InventoryRepository) ReceiveReturnedItem(ctx context.Context, warehouseID, skuID string, qty int64) (domain.StockItem, error) {
	if warehouseID == "" {
		warehouseID = domain.DefaultWarehouseID
	}
	if qty <= 0 {
		return domain.StockItem{}, domain.ErrInvalidReservationState
	}
	if r.pool != nil {
		var st domain.StockItem
		err := r.pool.QueryRow(ctx, `
			UPDATE stock_items
			SET on_hand = on_hand + $1,
			    unsellable = unsellable + $1,
			    version = version + 1
			WHERE sku_id = $2 AND warehouse_id = $3
			RETURNING sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit, version
		`, qty, skuID, warehouseID).Scan(&st.SkuID, &st.WarehouseID, &st.OnHand, &st.Unsellable, &st.Reserved, &st.Committed, &st.InTransit, &st.Version)
		if err != nil {
			return domain.StockItem{}, dependency.Wrap(err)
		}
		return st, nil
	}
	if err := r.engine.ReceiveReturnedItem(warehouseID, skuID, qty); err != nil {
		return domain.StockItem{}, err
	}
	st, _ := r.engine.GetStock(warehouseID, skuID)
	return st, nil
}

// ApproveReturnQC implements SC07: QC pass -> Unsellable -qty, Available +qty.
func (r *InventoryRepository) ApproveReturnQC(ctx context.Context, warehouseID, skuID string, qty int64) (domain.StockItem, error) {
	if warehouseID == "" {
		warehouseID = domain.DefaultWarehouseID
	}
	if qty <= 0 {
		return domain.StockItem{}, domain.ErrInvalidReservationState
	}
	if r.pool != nil {
		var st domain.StockItem
		err := r.pool.QueryRow(ctx, `
			UPDATE stock_items
			SET unsellable = unsellable - $1,
			    version = version + 1
			WHERE sku_id = $2 AND warehouse_id = $3 AND unsellable >= $1
			RETURNING sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit, version
		`, qty, skuID, warehouseID).Scan(&st.SkuID, &st.WarehouseID, &st.OnHand, &st.Unsellable, &st.Reserved, &st.Committed, &st.InTransit, &st.Version)
		if err != nil {
			return domain.StockItem{}, dependency.Wrap(err)
		}
		return st, nil
	}
	if err := r.engine.ApproveReturnQC(warehouseID, skuID, qty); err != nil {
		return domain.StockItem{}, err
	}
	st, _ := r.engine.GetStock(warehouseID, skuID)
	return st, nil
}
