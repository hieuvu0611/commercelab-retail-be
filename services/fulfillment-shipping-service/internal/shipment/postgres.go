package shipment

import (
	"context"
	"errors"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxTable is the fulfillment outbox.
const OutboxTable = "fulfillment.outbox_events"

// PostgresStore keeps shipments in fulfillment.shipments and fulfillment.shipment_items.
type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const shipmentColumns = `shipment_id, order_id, user_id, warehouse_id, status, version, handed_over_at, created_at, updated_at`

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func load(ctx context.Context, q querier, where string, arg string) (Shipment, error) {
	var s Shipment
	err := q.QueryRow(ctx, `SELECT `+shipmentColumns+` FROM fulfillment.shipments WHERE `+where+` = $1`, arg).
		Scan(&s.ShipmentID, &s.OrderID, &s.UserID, &s.WarehouseID, &s.Status, &s.Version, &s.HandedOverAt, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	if err != nil {
		return Shipment{}, dependency.Wrap(err)
	}
	rows, err := q.Query(ctx, `SELECT sku_id, quantity, picked_quantity FROM fulfillment.shipment_items WHERE shipment_id = $1 ORDER BY sku_id`, s.ShipmentID)
	if err != nil {
		return Shipment{}, dependency.Wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.SkuID, &it.Quantity, &it.PickedQuantity); err != nil {
			return Shipment{}, dependency.Wrap(err)
		}
		s.Items = append(s.Items, it)
	}
	return s, dependency.Wrap(rows.Err())
}

func (p *PostgresStore) Get(ctx context.Context, shipmentID string) (Shipment, error) {
	return load(ctx, p.pool, "shipment_id", shipmentID)
}

func (p *PostgresStore) GetByOrder(ctx context.Context, orderID string) (Shipment, error) {
	return load(ctx, p.pool, "order_id", orderID)
}

func (p *PostgresStore) Create(ctx context.Context, s Shipment, eventID string, ev ...events.DomainEvent) (Shipment, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Shipment{}, false, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The unique order id makes concurrent deliveries queue on the first insert; the losers see 0 rows.
	tag, err := tx.Exec(ctx, `INSERT INTO fulfillment.shipments (shipment_id, order_id, user_id, warehouse_id, status, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7) ON CONFLICT DO NOTHING`,
		s.ShipmentID, s.OrderID, s.UserID, s.WarehouseID, s.Status, s.Version, s.CreatedAt)
	if err != nil {
		return Shipment{}, false, dependency.Wrap(err)
	}
	created := tag.RowsAffected() == 1
	if _, err := tx.Exec(ctx, `INSERT INTO fulfillment.processed_events (event_id) VALUES ($1) ON CONFLICT DO NOTHING`, eventID); err != nil {
		return Shipment{}, false, dependency.Wrap(err)
	}
	if created {
		for _, it := range s.Items {
			if _, err := tx.Exec(ctx, `INSERT INTO fulfillment.shipment_items (shipment_id, sku_id, quantity) VALUES ($1, $2, $3)`,
				s.ShipmentID, it.SkuID, it.Quantity); err != nil {
				return Shipment{}, false, dependency.Wrap(err)
			}
		}
		for _, e := range ev {
			if err := outbox.Insert(ctx, tx, OutboxTable, e); err != nil {
				return Shipment{}, false, dependency.Wrap(err)
			}
		}
	}
	stored, err := load(ctx, tx, "order_id", s.OrderID)
	if err != nil {
		return Shipment{}, false, err
	}
	return stored, created, dependency.Wrap(tx.Commit(ctx))
}

func (p *PostgresStore) List(ctx context.Context, f Filter) ([]Shipment, int, error) {
	warehouses := f.Warehouses
	if warehouses == nil {
		warehouses = []string{}
	}
	const where = `WHERE ($1 = '' OR status = $1) AND ($2 OR warehouse_id = ANY($3))`
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fulfillment.shipments `+where, string(f.Status), f.AllWarehouses, warehouses).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := p.pool.Query(ctx, `SELECT shipment_id FROM fulfillment.shipments `+where+` ORDER BY created_at, shipment_id LIMIT $4 OFFSET $5`,
		string(f.Status), f.AllWarehouses, warehouses, limit, max(f.Offset, 0))
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, dependency.Wrap(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	out := make([]Shipment, 0, len(ids))
	for _, id := range ids {
		s, err := load(ctx, p.pool, "shipment_id", id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

func (p *PostgresStore) Save(ctx context.Context, s Shipment, expectedVersion int64, ev ...events.DomainEvent) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE fulfillment.shipments SET status = $3, version = $4, updated_at = $5, handed_over_at = $6 WHERE shipment_id = $1 AND version = $2`,
		s.ShipmentID, expectedVersion, s.Status, s.Version, s.UpdatedAt, s.HandedOverAt)
	if err != nil {
		return dependency.Wrap(err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fulfillment.shipments WHERE shipment_id = $1)`, s.ShipmentID).Scan(&exists); err != nil {
			return dependency.Wrap(err)
		}
		if !exists {
			return ErrNotFound
		}
		return ErrStaleVersion
	}
	for _, it := range s.Items {
		if _, err := tx.Exec(ctx, `UPDATE fulfillment.shipment_items SET picked_quantity = $3 WHERE shipment_id = $1 AND sku_id = $2`,
			s.ShipmentID, it.SkuID, it.PickedQuantity); err != nil {
			return dependency.Wrap(err)
		}
	}
	for _, e := range ev {
		if err := outbox.Insert(ctx, tx, OutboxTable, e); err != nil {
			return dependency.Wrap(err)
		}
	}
	return dependency.Wrap(tx.Commit(ctx))
}
