package repository

import (
	"context"
	"encoding/json"
	"errors"
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
	pool           *pgxpool.Pool
	engine         *domain.LedgerEngine
	mu             sync.Mutex
	reservations   map[string]*ReservationRecord
	movements      []domain.Movement // memory mode only; guarded by mu
	suppliers      map[string]*domain.Supplier
	purchaseOrders  map[string]*domain.PurchaseOrder
	inspections     map[string]*domain.ReceivingInspection
	supplierReturns map[string]*domain.SupplierReturn
	now             func() time.Time
}

// SetClock replaces the time source (tests use a fake clock for expiry rules).
func (r *InventoryRepository) SetClock(now func() time.Time) { r.now = now }

func NewInventoryRepository(pool *pgxpool.Pool) *InventoryRepository {
	seed := []domain.StockItem{
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HCM-01", OnHand: 10, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
		{SkuID: "SKU-BOTTLE-BLUE-500ML", WarehouseID: "WH-HCM-01", OnHand: 20, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HN-01", OnHand: 5, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
	}
	repo := &InventoryRepository{
		pool:            pool,
		engine:          domain.NewLedgerEngine(seed),
		reservations:    make(map[string]*ReservationRecord),
		suppliers:       make(map[string]*domain.Supplier),
		purchaseOrders:  make(map[string]*domain.PurchaseOrder),
		inspections:     make(map[string]*domain.ReceivingInspection),
		supplierReturns: make(map[string]*domain.SupplierReturn),
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

func (r *InventoryRepository) ReserveForOrder(ctx context.Context, orderID, warehouseID string, lines []domain.ReserveLine) (*ReservationRecord, error) {
	if warehouseID == "" {
		return nil, ErrWarehouseRequired
	}
	lines, err := domain.NormalizeLines(lines)
	if err != nil {
		return nil, err
	}
	expiresAt := r.now().UTC().Add(ReservationTTL)
	linesJSON, _ := json.Marshal(lines)

	if r.pool != nil {
		tx, err := r.pool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(ctx) }()

			// Claim the order id first. A repeat of the same order (double click, retry), even a concurrent one,
			// blocks here until the first transaction ends and then gets the stored reservation back.
			resID := "RES-" + orderID
			claim, err := tx.Exec(ctx, `
				INSERT INTO reservations (reservation_id, order_id, warehouse_id, status, lines_json, expires_at)
				VALUES ($1, $2, $3, 'ACTIVE_10M', $4::jsonb, $5)
				ON CONFLICT DO NOTHING
			`, resID, orderID, warehouseID, string(linesJSON), expiresAt)
			if err != nil {
				return nil, dependency.Wrap(err)
			}
			if claim.RowsAffected() == 0 {
				var existing ReservationRecord
				var rawLines []byte
				err = tx.QueryRow(ctx, `
					SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
					FROM reservations
					WHERE order_id = $1
				`, orderID).Scan(&existing.ReservationID, &existing.OrderID, &existing.WarehouseID, &existing.Status, &rawLines, &existing.ExpiresAt)
				if err != nil {
					return nil, dependency.Wrap(err)
				}
				_ = json.Unmarshal(rawLines, &existing.Lines)
				return &existing, nil
			}

			// lines are sorted by SKU id: every transaction locks rows in the same order, so none can deadlock.
			for _, line := range lines {
				var onHand, unsellable, reserved, committed int64
				err := tx.QueryRow(ctx, `
					SELECT on_hand, unsellable, reserved, committed
					FROM stock_items
					WHERE sku_id = $1 AND warehouse_id = $2
					FOR UPDATE
				`, line.SkuID, warehouseID).Scan(&onHand, &unsellable, &reserved, &committed)
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

			for _, line := range lines {
				if _, err := tx.Exec(ctx, `
					UPDATE stock_items
					SET reserved = reserved + $1, version = version + 1, updated_at = NOW()
					WHERE sku_id = $2 AND warehouse_id = $3
				`, line.Quantity, line.SkuID, warehouseID); err != nil {
					return nil, dependency.Wrap(err)
				}
				if err := insertMovement(ctx, tx, lineMovement(domain.MovementReserve, warehouseID, orderID, line, "order reservation")); err != nil {
					return nil, err
				}
			}

			if err := tx.Commit(ctx); err != nil {
				return nil, dependency.Wrap(err)
			}
			return &ReservationRecord{
				ReservationID: resID,
				OrderID:       orderID,
				WarehouseID:   warehouseID,
				Status:        "ACTIVE_10M",
				Lines:         lines,
				ExpiresAt:     expiresAt,
			}, nil
		}
		return nil, dependency.Wrap(err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.reservations[orderID]; ok {
		cp := *existing
		return &cp, nil
	}
	if err := r.engine.ReserveAllOrNothing(warehouseID, lines); err != nil {
		return nil, err
	}
	r.logLines(domain.MovementReserve, warehouseID, orderID, lines, "order reservation")
	rec := &ReservationRecord{
		ReservationID: "RES-" + orderID,
		OrderID:       orderID,
		WarehouseID:   warehouseID,
		Status:        "ACTIVE_10M",
		Lines:         lines,
		ExpiresAt:     expiresAt,
	}
	r.reservations[orderID] = rec
	cp := *rec
	return &cp, nil
}

func (r *InventoryRepository) CommitOrderReservation(ctx context.Context, orderID string) (*ReservationRecord, error) {
	if r.pool != nil {
		tx, err := r.pool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(ctx) }()
			var rec ReservationRecord
			var rawLines []byte
			err := tx.QueryRow(ctx, `
				SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
				FROM reservations
				WHERE order_id = $1
				FOR UPDATE
			`, orderID).Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrReservationNotFound
			}
			if err != nil {
				return nil, dependency.Wrap(err)
			}
			_ = json.Unmarshal(rawLines, &rec.Lines)
			if rec.Status == "COMMITTED" {
				_ = tx.Commit(ctx)
				return &rec, nil
			}
			if rec.Status != "ACTIVE_10M" && rec.Status != "EXCEPTION_HOLD_30M" {
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
				UPDATE reservations SET status = 'COMMITTED', version = version + 1 WHERE order_id = $1
			`, orderID); err != nil {
				return nil, dependency.Wrap(err)
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, dependency.Wrap(err)
			}
			rec.Status = "COMMITTED"
			return &rec, nil
		}
		return nil, dependency.Wrap(err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.reservations[orderID]
	if !ok {
		return nil, ErrReservationNotFound
	}
	if rec.Status == "COMMITTED" {
		cp := *rec
		return &cp, nil
	}
	if err := r.engine.CommitReservation(rec.WarehouseID, rec.Lines); err != nil {
		return nil, err
	}
	r.logLines(domain.MovementCommit, rec.WarehouseID, orderID, rec.Lines, "payment confirmed")
	rec.Status = "COMMITTED"
	cp := *rec
	return &cp, nil
}

func (r *InventoryRepository) ReleaseOrderReservation(ctx context.Context, orderID string) (*ReservationRecord, error) {
	if r.pool != nil {
		tx, err := r.pool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(ctx) }()
			var rec ReservationRecord
			var rawLines []byte
			err := tx.QueryRow(ctx, `
				SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
				FROM reservations
				WHERE order_id = $1
				FOR UPDATE
			`, orderID).Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrReservationNotFound
			}
			if err != nil {
				return nil, dependency.Wrap(err)
			}
			_ = json.Unmarshal(rawLines, &rec.Lines)
			if rec.Status == "RELEASED" {
				_ = tx.Commit(ctx)
				return &rec, nil
			}
			if rec.Status != "ACTIVE_10M" && rec.Status != "EXCEPTION_HOLD_30M" {
				return nil, domain.ErrInvalidReservationState
			}
			if err := releaseHeldStock(ctx, tx, &rec, domain.SystemActor, "reservation released"); err != nil {
				return nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, dependency.Wrap(err)
			}
			rec.Status = "RELEASED"
			return &rec, nil
		}
		return nil, dependency.Wrap(err)
	}

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
	if rec.Status != statusActive && rec.Status != statusException {
		return nil, domain.ErrInvalidReservationState
	}
	// Nhả Reserved bằng cách trừ lại từ r.engine
	if err := r.engine.ReleaseReservation(rec.WarehouseID, rec.Lines); err != nil {
		return nil, err
	}
	r.logLines(domain.MovementRelease, rec.WarehouseID, orderID, rec.Lines, "reservation released")
	rec.Status = "RELEASED"
	cp := *rec
	return &cp, nil
}

// GetReservation returns the reservation of an order without changing it.
func (r *InventoryRepository) GetReservation(ctx context.Context, orderID string) (*ReservationRecord, error) {
	if r.pool != nil {
		var rec ReservationRecord
		var rawLines []byte
		err := r.pool.QueryRow(ctx, `
			SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
			FROM reservations
			WHERE order_id = $1
		`, orderID).Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt)
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
	rec, ok := r.reservations[orderID]
	if !ok {
		return nil, ErrReservationNotFound
	}
	cp := *rec
	return &cp, nil
}

func (r *InventoryRepository) HandoverOrderShipment(ctx context.Context, orderID string) (*ReservationRecord, error) {
	if r.pool != nil {
		tx, err := r.pool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(ctx) }()
			var rec ReservationRecord
			var rawLines []byte
			err := tx.QueryRow(ctx, `
				SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
				FROM reservations
				WHERE order_id = $1
				FOR UPDATE
			`, orderID).Scan(&rec.ReservationID, &rec.OrderID, &rec.WarehouseID, &rec.Status, &rawLines, &rec.ExpiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrReservationNotFound
			}
			if err != nil {
				return nil, dependency.Wrap(err)
			}
			_ = json.Unmarshal(rawLines, &rec.Lines)
			if rec.Status == "HANDED_OVER" {
				return &rec, nil // a repeated handover changes nothing
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
				UPDATE reservations SET status = 'HANDED_OVER', version = version + 1 WHERE order_id = $1
			`, orderID); err != nil {
				return nil, dependency.Wrap(err)
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, dependency.Wrap(err)
			}
			rec.Status = "HANDED_OVER"
			return &rec, nil
		}
		return nil, dependency.Wrap(err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.reservations[orderID]
	if !ok {
		return nil, ErrReservationNotFound
	}
	if rec.Status == "HANDED_OVER" {
		cp := *rec
		return &cp, nil // a repeated handover changes nothing
	}
	if rec.Status != "COMMITTED" {
		return nil, domain.ErrInvalidReservationState
	}
	if err := r.engine.HandoverShipment(rec.WarehouseID, rec.Lines); err != nil {
		return nil, err
	}
	r.logLines(domain.MovementHandover, rec.WarehouseID, orderID, rec.Lines, "handed over to courier")
	rec.Status = "HANDED_OVER"
	cp := *rec
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
