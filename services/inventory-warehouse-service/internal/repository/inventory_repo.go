package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/hieuvu0211/full-service-shopping/be/services/inventory-warehouse-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrReservationNotFound = errors.New("không tìm thấy phiên giữ tồn kho của đơn hàng")
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
	pool         *pgxpool.Pool
	engine       *domain.LedgerEngine
	mu           sync.Mutex
	reservations map[string]*ReservationRecord
}

func NewInventoryRepository(pool *pgxpool.Pool) *InventoryRepository {
	engine := domain.NewLedgerEngine([]domain.StockItem{
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HCM-01", OnHand: 10, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
		{SkuID: "SKU-BOTTLE-BLUE-500ML", WarehouseID: "WH-HCM-01", OnHand: 20, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
	})
	return &InventoryRepository{
		pool:         pool,
		engine:       engine,
		reservations: make(map[string]*ReservationRecord),
	}
}

func (r *InventoryRepository) GetStock(ctx context.Context, warehouseID, skuID string) (domain.StockItem, bool) {
	if warehouseID == "" {
		warehouseID = "WH-HCM-01"
	}
	if r.pool != nil {
		var st domain.StockItem
		err := r.pool.QueryRow(ctx, `
			SELECT sku_id, warehouse_id, on_hand, unsellable, reserved, committed, in_transit, version
			FROM stock_items
			WHERE sku_id = $1 AND warehouse_id = $2
		`, skuID, warehouseID).Scan(&st.SkuID, &st.WarehouseID, &st.OnHand, &st.Unsellable, &st.Reserved, &st.Committed, &st.InTransit, &st.Version)
		if err == nil {
			return st, true
		}
	}
	return r.engine.GetStock(skuID)
}

func (r *InventoryRepository) ReserveForOrder(ctx context.Context, orderID, warehouseID string, lines []domain.ReserveLine) (*ReservationRecord, error) {
	if warehouseID == "" {
		warehouseID = "WH-HCM-01"
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	linesJSON, _ := json.Marshal(lines)

	if r.pool != nil {
		tx, err := r.pool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(ctx) }()

			// Kiểm tra idempotent nếu orderID đã giữ tồn trước đó
			var existing ReservationRecord
			var rawLines []byte
			err = tx.QueryRow(ctx, `
				SELECT reservation_id, order_id, warehouse_id, status, lines_json, expires_at
				FROM reservations
				WHERE order_id = $1
			`, orderID).Scan(&existing.ReservationID, &existing.OrderID, &existing.WarehouseID, &existing.Status, &rawLines, &existing.ExpiresAt)
			if err == nil {
				_ = json.Unmarshal(rawLines, &existing.Lines)
				_ = tx.Commit(ctx)
				return &existing, nil
			}

			for _, line := range lines {
				if line.Quantity <= 0 {
					return nil, domain.ErrInsufficientStock
				}
				var onHand, unsellable, reserved, committed int64
				err := tx.QueryRow(ctx, `
					SELECT on_hand, unsellable, reserved, committed
					FROM stock_items
					WHERE sku_id = $1 AND warehouse_id = $2
					FOR UPDATE
				`, line.SkuID, warehouseID).Scan(&onHand, &unsellable, &reserved, &committed)
				if err != nil || (onHand-unsellable-reserved-committed) < line.Quantity {
					return nil, domain.ErrInsufficientStock
				}
			}

			for _, line := range lines {
				if _, err := tx.Exec(ctx, `
					UPDATE stock_items
					SET reserved = reserved + $1, version = version + 1, updated_at = NOW()
					WHERE sku_id = $2 AND warehouse_id = $3
				`, line.Quantity, line.SkuID, warehouseID); err != nil {
					return nil, err
				}
			}

			resID := "RES-" + orderID
			if _, err := tx.Exec(ctx, `
				INSERT INTO reservations (reservation_id, order_id, warehouse_id, status, lines_json, expires_at)
				VALUES ($1, $2, $3, 'ACTIVE_10M', $4::jsonb, $5)
			`, resID, orderID, warehouseID, string(linesJSON), expiresAt); err != nil {
				return nil, err
			}

			if err := tx.Commit(ctx); err != nil {
				return nil, err
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
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.reservations[orderID]; ok {
		cp := *existing
		return &cp, nil
	}
	if err := r.engine.ReserveAllOrNothing(lines); err != nil {
		return nil, err
	}
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
			if err != nil {
				return nil, ErrReservationNotFound
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
					return nil, err
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE reservations SET status = 'COMMITTED', version = version + 1 WHERE order_id = $1
			`, orderID); err != nil {
				return nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			rec.Status = "COMMITTED"
			return &rec, nil
		}
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
	if err := r.engine.CommitReservation(rec.Lines); err != nil {
		return nil, err
	}
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
			if err != nil {
				return nil, ErrReservationNotFound
			}
			_ = json.Unmarshal(rawLines, &rec.Lines)
			if rec.Status == "RELEASED" {
				_ = tx.Commit(ctx)
				return &rec, nil
			}
			if rec.Status != "ACTIVE_10M" && rec.Status != "EXCEPTION_HOLD_30M" {
				return nil, domain.ErrInvalidReservationState
			}
			for _, line := range rec.Lines {
				if _, err := tx.Exec(ctx, `
					UPDATE stock_items
					SET reserved = reserved - $1, version = version + 1, updated_at = NOW()
					WHERE sku_id = $2 AND warehouse_id = $3
				`, line.Quantity, line.SkuID, rec.WarehouseID); err != nil {
					return nil, err
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE reservations SET status = 'RELEASED', version = version + 1 WHERE order_id = $1
			`, orderID); err != nil {
				return nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			rec.Status = "RELEASED"
			return &rec, nil
		}
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
	if rec.Status != "ACTIVE_10M" && rec.Status != "EXCEPTION_HOLD_30M" {
		return nil, domain.ErrInvalidReservationState
	}
	// Nhả Reserved bằng cách trừ lại từ r.engine
	if err := r.engine.ReleaseReservation(rec.Lines); err != nil {
		return nil, err
	}
	rec.Status = "RELEASED"
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
			if err != nil {
				return nil, ErrReservationNotFound
			}
			_ = json.Unmarshal(rawLines, &rec.Lines)
			if rec.Status != "COMMITTED" {
				return nil, domain.ErrInvalidReservationState
			}
			for _, line := range rec.Lines {
				if _, err := tx.Exec(ctx, `
					UPDATE stock_items
					SET on_hand = on_hand - $1, committed = committed - $1, version = version + 1, updated_at = NOW()
					WHERE sku_id = $2 AND warehouse_id = $3
				`, line.Quantity, line.SkuID, rec.WarehouseID); err != nil {
					return nil, err
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE reservations SET status = 'HANDED_OVER', version = version + 1 WHERE order_id = $1
			`, orderID); err != nil {
				return nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			rec.Status = "HANDED_OVER"
			return &rec, nil
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.reservations[orderID]
	if !ok {
		return nil, ErrReservationNotFound
	}
	if rec.Status != "COMMITTED" {
		return nil, domain.ErrInvalidReservationState
	}
	if err := r.engine.HandoverShipment(rec.Lines); err != nil {
		return nil, err
	}
	rec.Status = "HANDED_OVER"
	cp := *rec
	return &cp, nil
}
