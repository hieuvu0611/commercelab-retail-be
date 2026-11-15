package orders

import (
	"context"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore keeps orders in orders.orders, orders.order_items and orders.idempotency_keys.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const orderColumns = `order_id, user_id, quote_id, address_id, warehouse_id, COALESCE(voucher_code, ''), status, payment_status,
	failure_code, subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd, captured_amount_vnd, refund_pending_vnd, version, expires_at, created_at, updated_at`

func scanOrder(row pgx.Row) (Order, error) {
	var o Order
	err := row.Scan(&o.OrderID, &o.UserID, &o.QuoteID, &o.AddressID, &o.WarehouseID, &o.VoucherCode, &o.Status, &o.PaymentStatus,
		&o.FailureCode, &o.SubtotalVND, &o.DiscountVND, &o.ShippingFeeVND, &o.TotalPayableVND, &o.CapturedVND, &o.RefundPendingVND, &o.Version, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadOrder(ctx context.Context, q queryer, orderID string) (Order, error) {
	o, err := scanOrder(q.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders.orders WHERE order_id = $1`, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, dependency.Wrap(err)
	}
	rows, err := q.Query(ctx, `SELECT sku_id, sku_name, quantity, unit_price_vnd, line_discount_vnd, allocated_unit_paid_vnd
		FROM orders.order_items WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return Order{}, dependency.Wrap(err)
	}
	defer rows.Close()
	o.Items = []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.SkuID, &it.SkuName, &it.Quantity, &it.UnitPriceVND, &it.LineDiscountVND, &it.AllocatedUnitPaidVND); err != nil {
			return Order{}, dependency.Wrap(err)
		}
		o.Items = append(o.Items, it)
	}
	return o, dependency.Wrap(rows.Err())
}

// replay returns the order stored for a claimed key after checking that the stored hash matches.
func replay(ctx context.Context, q queryer, userID, key, hash string) (Order, error) {
	var storedHash, orderID string
	err := q.QueryRow(ctx, `SELECT request_hash, order_id FROM orders.idempotency_keys WHERE user_id = $1 AND idempotency_key = $2`,
		userID, key).Scan(&storedHash, &orderID)
	if err != nil {
		return Order{}, err
	}
	if storedHash != hash {
		return Order{}, ErrIdempotencyConflict
	}
	return loadOrder(ctx, q, orderID)
}

func (s *PostgresStore) Create(ctx context.Context, key, hash string, o Order) (Order, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, false, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim the key first. ON CONFLICT DO NOTHING waits for a concurrent claimant to finish, so a duplicate
	// never creates a second order: it reads the first one back below.
	claim, err := tx.Exec(ctx, `INSERT INTO orders.idempotency_keys (user_id, idempotency_key, request_hash, order_id, response_json)
		VALUES ($1, $2, $3, $4, '{}') ON CONFLICT (user_id, idempotency_key) DO NOTHING`, o.UserID, key, hash, o.OrderID)
	if err != nil {
		return Order{}, false, dependency.Wrap(err)
	}
	if claim.RowsAffected() == 0 {
		existing, err := replay(ctx, tx, o.UserID, key, hash)
		if err != nil {
			if errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrNotFound) || dependency.IsUnavailable(err) {
				return Order{}, false, err
			}
			return Order{}, false, dependency.Wrap(err)
		}
		return existing, true, nil
	}

	status, payment := o.Status, o.PaymentStatus
	if status == "" {
		status = StatusPending
	}
	if payment == "" {
		payment = PaymentUnpaid
	}
	if _, err := tx.Exec(ctx, `INSERT INTO orders.orders (order_id, user_id, quote_id, address_id, warehouse_id, voucher_code, status, payment_status,
			subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd, expires_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11, $12, $13)`,
		o.OrderID, o.UserID, o.QuoteID, o.AddressID, o.WarehouseID, o.VoucherCode, status, payment,
		o.SubtotalVND, o.DiscountVND, o.ShippingFeeVND, o.TotalPayableVND, o.ExpiresAt); err != nil {
		return Order{}, false, dependency.Wrap(err)
	}
	for _, it := range o.Items {
		if _, err := tx.Exec(ctx, `INSERT INTO orders.order_items (order_id, sku_id, sku_name, quantity, unit_price_vnd, line_discount_vnd, allocated_unit_paid_vnd)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, o.OrderID, it.SkuID, it.SkuName, it.Quantity, it.UnitPriceVND, it.LineDiscountVND, it.AllocatedUnitPaidVND); err != nil {
			return Order{}, false, dependency.Wrap(err)
		}
	}
	stored, err := loadOrder(ctx, tx, o.OrderID)
	if err != nil {
		return Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, false, dependency.Wrap(err)
	}
	return stored, false, nil
}

func (s *PostgresStore) Lookup(ctx context.Context, userID, key, hash string) (Order, bool, error) {
	o, err := replay(ctx, s.pool, userID, key, hash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Order{}, false, nil
	case err != nil:
		if errors.Is(err, ErrIdempotencyConflict) || dependency.IsUnavailable(err) {
			return Order{}, false, err
		}
		return Order{}, false, dependency.Wrap(err)
	}
	return o, true, nil
}

func (s *PostgresStore) Get(ctx context.Context, orderID string) (Order, error) {
	return loadOrder(ctx, s.pool, orderID)
}

func (s *PostgresStore) Query(ctx context.Context, f Filter) ([]Order, int, error) {
	var before *time.Time
	if !f.UpdatedBefore.IsZero() {
		before = &f.UpdatedBefore
	}
	var after *time.Time
	if !f.UpdatedAfter.IsZero() {
		after = &f.UpdatedAfter
	}
	statuses := f.Statuses
	if statuses == nil {
		statuses = []string{}
	}
	const where = `WHERE ($1 = '' OR user_id = $1) AND (cardinality($2::text[]) = 0 OR status = ANY($2)) AND ($3 = '' OR failure_code = $3)
		AND ($4::timestamptz IS NULL OR updated_at <= $4) AND ($5::timestamptz IS NULL OR updated_at >= $5)`
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders.orders `+where, f.UserID, statuses, f.FailureCode, before, after).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+orderColumns+` FROM orders.orders `+where+` ORDER BY created_at DESC, order_id DESC LIMIT $6 OFFSET $7`,
		f.UserID, statuses, f.FailureCode, before, after, PageLimit(f.Limit), max(f.Offset, 0))
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		out = append(out, o)
	}
	return out, total, dependency.Wrap(rows.Err())
}

func (s *PostgresStore) RecordCapture(ctx context.Context, orderID string, capturedVND, refundPendingVND int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE orders.orders SET captured_amount_vnd = $2, refund_pending_vnd = $3, updated_at = $4 WHERE order_id = $1`,
		orderID, capturedVND, refundPendingVND, time.Now().UTC())
	if err != nil {
		return dependency.Wrap(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) Transition(ctx context.Context, orderID, from, to, paymentStatus, failureCode string) (Order, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE orders.orders SET status = $3, payment_status = $4, failure_code = $5,
		version = version + 1, updated_at = $6 WHERE order_id = $1 AND status = $2`,
		orderID, from, to, paymentStatus, failureCode, time.Now().UTC())
	if err != nil {
		return Order{}, dependency.Wrap(err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := loadOrder(ctx, s.pool, orderID); err != nil {
			return Order{}, err
		}
		return Order{}, ErrStaleState
	}
	return loadOrder(ctx, s.pool, orderID)
}
