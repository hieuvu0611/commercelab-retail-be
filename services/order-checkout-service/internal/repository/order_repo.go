package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type OrderRepository struct {
	pgPool     *pgxpool.Pool
	mongoStore *db.MongoStore
	engine     *domain.CheckoutEngine
}

func NewOrderRepository(pgPool *pgxpool.Pool, mongoStore *db.MongoStore) *OrderRepository {
	return &OrderRepository{
		pgPool:     pgPool,
		mongoStore: mongoStore,
		engine:     domain.NewCheckoutEngine(),
	}
}

func (r *OrderRepository) PlaceOrder(
	ctx context.Context,
	idempotencyKey, orderID, userID, quoteID, voucherCode string,
	subtotalVND, discountVND, shippingFeeVND, totalPayableVND int64,
	items []domain.OrderItemLine,
) (*domain.OrderRecord, bool, error) {
	items = domain.DefaultItemsIfEmpty(items)
	reqHash := domain.HashIntent(userID, quoteID, voucherCode, totalPayableVND)

	if r.pgPool != nil {
		tx, err := r.pgPool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(ctx) }()

			var existingHash, existingResp string
			err = tx.QueryRow(ctx, `
				SELECT request_hash, response_json
				FROM idempotency_keys
				WHERE idempotency_key = $1
			`, idempotencyKey).Scan(&existingHash, &existingResp)
			if err == nil {
				if existingHash != reqHash {
					return nil, false, domain.ErrIdempotencyConflict
				}
				var cached domain.OrderRecord
				_ = json.Unmarshal([]byte(existingResp), &cached)
				_ = tx.Commit(ctx)
				return &cached, true, nil
			}

			if voucherCode != "" {
				tag, err := tx.Exec(ctx, `
					UPDATE voucher_campaigns
					SET reserved_quota = reserved_quota + 1, version = version + 1
					WHERE voucher_code = $1 AND (total_quota - reserved_quota - consumed_quota) > 0
				`, voucherCode)
				if err != nil || tag.RowsAffected() == 0 {
					return nil, false, domain.ErrVoucherExhausted
				}
			}

			rec := &domain.OrderRecord{
				OrderID:         orderID,
				UserID:          userID,
				QuoteID:         quoteID,
				VoucherCode:     voucherCode,
				Status:          "PENDING",
				PaymentStatus:   "PROCESSING",
				SubtotalVND:     subtotalVND,
				DiscountVND:     discountVND,
				ShippingFeeVND:  shippingFeeVND,
				TotalPayableVND: totalPayableVND,
				Items:           items,
			}

			if _, err := tx.Exec(ctx, `
				INSERT INTO orders (order_id, user_id, quote_id, voucher_code, status, payment_status, subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			`, rec.OrderID, rec.UserID, rec.QuoteID, rec.VoucherCode, rec.Status, rec.PaymentStatus, rec.SubtotalVND, rec.DiscountVND, rec.ShippingFeeVND, rec.TotalPayableVND); err != nil {
				return nil, false, err
			}

			for _, line := range items {
				if _, err := tx.Exec(ctx, `
					INSERT INTO order_items (order_id, sku_id, sku_name, quantity, unit_price_vnd, line_discount_vnd, allocated_unit_paid_vnd)
					VALUES ($1, $2, $3, $4, $5, $6, $7)
				`, rec.OrderID, line.SkuID, line.SkuName, line.Quantity, line.UnitPriceVND, line.LineDiscountVND, line.AllocatedUnitPaidVND); err != nil {
					return nil, false, err
				}
			}

			respJSON, _ := json.Marshal(rec)
			if _, err := tx.Exec(ctx, `
				INSERT INTO idempotency_keys (idempotency_key, user_id, request_hash, order_id, response_json)
				VALUES ($1, $2, $3, $4, $5)
			`, idempotencyKey, userID, reqHash, orderID, string(respJSON)); err != nil {
				return nil, false, err
			}

			if err := tx.Commit(ctx); err != nil {
				return nil, false, err
			}

			r.persistCommercialSnapshot(ctx, rec)
			return rec, false, nil
		}
	}

	rec, replayed, err := r.engine.PlaceOrderWithLines(
		idempotencyKey, orderID, userID, quoteID, voucherCode,
		subtotalVND, discountVND, shippingFeeVND, totalPayableVND, items,
	)
	if err != nil {
		return nil, false, err
	}
	if !replayed {
		r.persistCommercialSnapshot(ctx, rec)
	}
	return rec, replayed, nil
}

func (r *OrderRepository) persistCommercialSnapshot(ctx context.Context, ord *domain.OrderRecord) {
	if r.mongoStore == nil {
		return
	}
	_ = r.mongoStore.InsertOne(ctx, "order_commercial_snapshots", bson.D{
		{Key: "order_id", Value: ord.OrderID},
		{Key: "user_id", Value: ord.UserID},
		{Key: "quote_id", Value: ord.QuoteID},
		{Key: "voucher_code", Value: ord.VoucherCode},
		{Key: "subtotal_vnd", Value: ord.SubtotalVND},
		{Key: "discount_vnd", Value: ord.DiscountVND},
		{Key: "shipping_fee_vnd", Value: ord.ShippingFeeVND},
		{Key: "total_payable_vnd", Value: ord.TotalPayableVND},
		{Key: "items", Value: ord.Items},
		{Key: "frozen_at", Value: time.Now().UTC()},
	})
}

func (r *OrderRepository) GetOrder(ctx context.Context, orderID string) (*domain.OrderRecord, error) {
	if r.pgPool != nil {
		var ord domain.OrderRecord
		err := r.pgPool.QueryRow(ctx, `
			SELECT order_id, user_id, quote_id, COALESCE(voucher_code, ''), status, payment_status, subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd
			FROM orders
			WHERE order_id = $1
		`, orderID).Scan(
			&ord.OrderID, &ord.UserID, &ord.QuoteID, &ord.VoucherCode,
			&ord.Status, &ord.PaymentStatus, &ord.SubtotalVND,
			&ord.DiscountVND, &ord.ShippingFeeVND, &ord.TotalPayableVND,
		)
		if err == nil {
			rows, qErr := r.pgPool.Query(ctx, `
				SELECT sku_id, sku_name, quantity, unit_price_vnd, line_discount_vnd, allocated_unit_paid_vnd
				FROM order_items
				WHERE order_id = $1
				ORDER BY id
			`, orderID)
			if qErr == nil {
				defer rows.Close()
				for rows.Next() {
					var it domain.OrderItemLine
					if err := rows.Scan(&it.SkuID, &it.SkuName, &it.Quantity, &it.UnitPriceVND, &it.LineDiscountVND, &it.AllocatedUnitPaidVND); err == nil {
						ord.Items = append(ord.Items, it)
					}
				}
			}
			return &ord, nil
		}
	}
	return r.engine.GetOrder(orderID)
}

func (r *OrderRepository) ListOrders(ctx context.Context, userID string) ([]domain.OrderRecord, error) {
	if r.pgPool != nil {
		rows, err := r.pgPool.Query(ctx, `
			SELECT order_id, user_id, quote_id, COALESCE(voucher_code, ''), status, payment_status, subtotal_vnd, discount_vnd, shipping_fee_vnd, total_payable_vnd
			FROM orders
			WHERE ($1 = '' OR user_id = $1)
			ORDER BY created_at DESC
		`, userID)
		if err == nil {
			defer rows.Close()
			var list []domain.OrderRecord
			for rows.Next() {
				var ord domain.OrderRecord
				if err := rows.Scan(
					&ord.OrderID, &ord.UserID, &ord.QuoteID, &ord.VoucherCode,
					&ord.Status, &ord.PaymentStatus, &ord.SubtotalVND,
					&ord.DiscountVND, &ord.ShippingFeeVND, &ord.TotalPayableVND,
				); err == nil {
					list = append(list, ord)
				}
			}
			return list, nil
		}
	}
	return r.engine.ListOrders(userID), nil
}

func (r *OrderRepository) ApplyPaymentOutcome(ctx context.Context, orderID, outcome string) (*domain.OrderRecord, error) {
	if r.pgPool != nil {
		tx, err := r.pgPool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(ctx) }()

			var voucherCode, status, paymentStatus string
			err := tx.QueryRow(ctx, `
				SELECT COALESCE(voucher_code, ''), status, payment_status
				FROM orders
				WHERE order_id = $1
				FOR UPDATE
			`, orderID).Scan(&voucherCode, &status, &paymentStatus)
			if err != nil {
				return nil, domain.ErrOrderNotFound
			}

			switch outcome {
			case "CAPTURED":
				if status != "CONFIRMED" {
					if status != "PENDING" {
						return nil, domain.ErrInvalidOrderState
					}
					if _, err := tx.Exec(ctx, `
						UPDATE orders SET status = 'CONFIRMED', payment_status = 'CAPTURED', version = version + 1, updated_at = NOW()
						WHERE order_id = $1
					`, orderID); err != nil {
						return nil, err
					}
					if voucherCode != "" {
						_, _ = tx.Exec(ctx, `
							UPDATE voucher_campaigns
							SET reserved_quota = GREATEST(reserved_quota - 1, 0), consumed_quota = consumed_quota + 1, version = version + 1
							WHERE voucher_code = $1
						`, voucherCode)
					}
				}
			case "UNKNOWN_30M":
				if status != "PENDING" {
					return nil, domain.ErrInvalidOrderState
				}
				if _, err := tx.Exec(ctx, `
					UPDATE orders SET payment_status = 'UNKNOWN_30M', version = version + 1, updated_at = NOW()
					WHERE order_id = $1
				`, orderID); err != nil {
					return nil, err
				}
			case "FAILED", "EXPIRED_30M":
				if status == "PENDING" {
					nextStatus := "CANCELLED"
					if outcome == "EXPIRED_30M" {
						nextStatus = "FAILED_CLOSED"
					}
					if _, err := tx.Exec(ctx, `
						UPDATE orders SET status = $2, payment_status = 'FAILED', version = version + 1, updated_at = NOW()
						WHERE order_id = $1
					`, orderID, nextStatus); err != nil {
						return nil, err
					}
					if voucherCode != "" {
						_, _ = tx.Exec(ctx, `
							UPDATE voucher_campaigns
							SET reserved_quota = GREATEST(reserved_quota - 1, 0), version = version + 1
							WHERE voucher_code = $1
						`, voucherCode)
					}
				}
			default:
				return nil, domain.ErrInvalidOrderState
			}

			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			return r.GetOrder(ctx, orderID)
		}
	}
	return r.engine.ApplyPaymentOutcome(orderID, outcome)
}
