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

const shipmentColumns = `shipment_id, order_id, user_id, warehouse_id, status, version, created_at, updated_at`

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func load(ctx context.Context, q querier, where string, arg string) (Shipment, error) {
	var s Shipment
	err := q.QueryRow(ctx, `SELECT `+shipmentColumns+` FROM fulfillment.shipments WHERE `+where+` = $1`, arg).
		Scan(&s.ShipmentID, &s.OrderID, &s.UserID, &s.WarehouseID, &s.Status, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	if err != nil {
		return Shipment{}, dependency.Wrap(err)
	}
	rows, err := q.Query(ctx, `SELECT sku_id, quantity FROM fulfillment.shipment_items WHERE shipment_id = $1 ORDER BY sku_id`, s.ShipmentID)
	if err != nil {
		return Shipment{}, dependency.Wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.SkuID, &it.Quantity); err != nil {
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
