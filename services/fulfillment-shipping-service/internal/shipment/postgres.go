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

const shipmentColumns = `shipment_id, order_id, user_id, warehouse_id, parcel_index, total_parcels, payment_method, cod_amount_to_collect, status, version, handed_over_at, delivered_at, return_window_ends_at, failure_reason, stop_requested_at, created_at, updated_at`

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func load(ctx context.Context, q querier, where string, arg string) (Shipment, error) {
	var s Shipment
	err := q.QueryRow(ctx, `SELECT `+shipmentColumns+` FROM fulfillment.shipments WHERE `+where+` = $1 ORDER BY parcel_index, shipment_id LIMIT 1`, arg).
		Scan(&s.ShipmentID, &s.OrderID, &s.UserID, &s.WarehouseID, &s.ParcelIndex, &s.TotalParcels, &s.PaymentMethod, &s.CodAmountToCollect, &s.Status, &s.Version, &s.HandedOverAt, &s.DeliveredAt, &s.ReturnWindowEndsAt, &s.FailureReason, &s.StopRequestedAt, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	if err != nil {
		return Shipment{}, dependency.Wrap(err)
	}
	rows, err := q.Query(ctx, `SELECT sku_id, quantity, picked_quantity, delivered_quantity FROM fulfillment.shipment_items WHERE shipment_id = $1 ORDER BY sku_id`, s.ShipmentID)
	if err != nil {
		return Shipment{}, dependency.Wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.SkuID, &it.Quantity, &it.PickedQuantity, &it.DeliveredQuantity); err != nil {
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

func (p *PostgresStore) ListByOrder(ctx context.Context, orderID string) ([]Shipment, error) {
	rows, err := p.pool.Query(ctx, `SELECT shipment_id FROM fulfillment.shipments WHERE order_id = $1 ORDER BY parcel_index, shipment_id`, orderID)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, dependency.Wrap(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dependency.Wrap(err)
	}
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	out := make([]Shipment, 0, len(ids))
	for _, id := range ids {
		s, err := load(ctx, p.pool, "shipment_id", id)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (p *PostgresStore) Create(ctx context.Context, s Shipment, eventID string, ev ...events.DomainEvent) (Shipment, bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Shipment{}, false, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	method := s.PaymentMethod
	if method == "" {
		method = "PREPAID"
	}
	pIdx, pTotal := s.ParcelIndex, s.TotalParcels
	if pIdx < 1 {
		pIdx = 1
	}
	if pTotal < 1 {
		pTotal = 1
	}
	tag, err := tx.Exec(ctx, `INSERT INTO fulfillment.shipments (shipment_id, order_id, user_id, warehouse_id, parcel_index, total_parcels, payment_method, cod_amount_to_collect, status, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11) ON CONFLICT DO NOTHING`,
		s.ShipmentID, s.OrderID, s.UserID, s.WarehouseID, pIdx, pTotal, method, s.CodAmountToCollect, s.Status, s.Version, s.CreatedAt)
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
	stored, err := load(ctx, tx, "shipment_id", s.ShipmentID)
	if err != nil {
		stored, err = load(ctx, tx, "order_id", s.OrderID)
		if err != nil {
			return Shipment{}, false, err
		}
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
	return p.SaveCarrierEvent(ctx, s, expectedVersion, "", ev...)
}

func (p *PostgresStore) SaveCarrierEvent(ctx context.Context, s Shipment, expectedVersion int64, carrierEventID string, ev ...events.DomainEvent) error {
	return p.SaveWith(ctx, s, expectedVersion, SaveOptions{CarrierEventID: carrierEventID, Events: ev})
}

func (p *PostgresStore) SaveWith(ctx context.Context, s Shipment, expectedVersion int64, o SaveOptions) error {
	carrierEventID, ev := o.CarrierEventID, o.Events
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if carrierEventID != "" {
		// The primary key makes a concurrent copy of the same notice wait here and then see 0 rows.
		tag, err := tx.Exec(ctx, `INSERT INTO fulfillment.carrier_events (event_id, shipment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, carrierEventID, s.ShipmentID)
		if err != nil {
			return dependency.Wrap(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrDuplicateEvent
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE fulfillment.shipments SET status = $3, version = $4, updated_at = $5, handed_over_at = $6,
		delivered_at = $7, return_window_ends_at = $8, failure_reason = $9, stop_requested_at = $10 WHERE shipment_id = $1 AND version = $2`,
		s.ShipmentID, expectedVersion, s.Status, s.Version, s.UpdatedAt, s.HandedOverAt, s.DeliveredAt, s.ReturnWindowEndsAt, s.FailureReason, s.StopRequestedAt)
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
		if _, err := tx.Exec(ctx, `UPDATE fulfillment.shipment_items SET picked_quantity = $3, delivered_quantity = $4 WHERE shipment_id = $1 AND sku_id = $2`,
			s.ShipmentID, it.SkuID, it.PickedQuantity, it.DeliveredQuantity); err != nil {
			return dependency.Wrap(err)
		}
	}
	if c := o.Case; c != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO fulfillment.cases (case_id, case_type, status, shipment_id, order_id, warehouse_id, sku_id, quantity, reason, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
			c.CaseID, c.Type, c.Status, c.ShipmentID, c.OrderID, c.WarehouseID, c.SkuID, c.Quantity, c.Reason, c.CreatedAt); err != nil {
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

func (p *PostgresStore) ListCases(ctx context.Context, f CaseFilter) ([]Case, int, error) {
	warehouses := f.Warehouses
	if warehouses == nil {
		warehouses = []string{}
	}
	const where = `WHERE ($1 = '' OR status = $1) AND ($2 = '' OR case_type = $2) AND ($3 OR warehouse_id = ANY($4))`
	var total int
	if err := p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fulfillment.cases `+where, f.Status, f.Type, f.AllWarehouses, warehouses).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := p.pool.Query(ctx, `SELECT case_id, case_type, status, shipment_id, order_id, warehouse_id, sku_id, quantity, reason, created_at
		FROM fulfillment.cases `+where+` ORDER BY created_at, case_id LIMIT $5 OFFSET $6`,
		f.Status, f.Type, f.AllWarehouses, warehouses, limit, max(f.Offset, 0))
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	defer rows.Close()
	var out []Case
	for rows.Next() {
		var c Case
		if err := rows.Scan(&c.CaseID, &c.Type, &c.Status, &c.ShipmentID, &c.OrderID, &c.WarehouseID, &c.SkuID, &c.Quantity, &c.Reason, &c.CreatedAt); err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		c.SkuBlocked = c.Type == CasePickShortage && c.Status == CaseOpen
		out = append(out, c)
	}
	return out, total, dependency.Wrap(rows.Err())
}
