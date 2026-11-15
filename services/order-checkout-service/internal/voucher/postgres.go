package voucher

import (
	"context"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore reads campaigns from orders.voucher_campaigns.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const campaignColumns = `voucher_code, name, discount_pct, total_quota, reserved_quota, consumed_quota, per_customer_limit,
	starts_at, ends_at, status, version, created_by, created_at, updated_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.Code, &c.Name, &c.DiscountPct, &c.TotalQuota, &c.ReservedQuota, &c.ConsumedQuota, &c.PerCustomerLimit,
		&c.StartsAt, &c.EndsAt, &c.Status, &c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *PostgresStore) Get(ctx context.Context, code string) (Campaign, error) {
	c, err := scanCampaign(s.pool.QueryRow(ctx, `SELECT `+campaignColumns+` FROM orders.voucher_campaigns WHERE voucher_code = $1`, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	return c, dependency.Wrap(err)
}

func (s *PostgresStore) CustomerUses(ctx context.Context, code, userID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders.voucher_usages
		WHERE voucher_code = $1 AND user_id = $2 AND state IN ('RESERVED', 'CONSUMED')`, code, userID).Scan(&n)
	return n, dependency.Wrap(err)
}

func (s *PostgresStore) Create(ctx context.Context, code, actor string, f Fields) (Campaign, error) {
	c, err := scanCampaign(s.pool.QueryRow(ctx, `INSERT INTO orders.voucher_campaigns
		(voucher_code, name, discount_pct, total_quota, per_customer_limit, starts_at, ends_at, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+campaignColumns,
		code, f.Name, f.DiscountPct, f.TotalQuota, f.PerCustomerLimit, f.StartsAt, f.EndsAt, f.Status, actor))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Campaign{}, ErrCodeExists
	}
	return c, dependency.Wrap(err)
}

func (s *PostgresStore) List(ctx context.Context, f ListFilter) ([]Campaign, int64, error) {
	where := `WHERE ($1 = '' OR status = $1)
		AND ($2 = '' OR voucher_code ILIKE '%' || $2 || '%' ESCAPE '\' OR name ILIKE '%' || $2 || '%' ESCAPE '\')`
	q := EscapeLike(f.Q)
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders.voucher_campaigns `+where, f.Status, q).Scan(&total); err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+campaignColumns+` FROM orders.voucher_campaigns `+where+`
		ORDER BY created_at DESC, voucher_code LIMIT $3 OFFSET $4`, f.Status, q, f.Limit, (f.Page-1)*f.Limit)
	if err != nil {
		return nil, 0, dependency.Wrap(err)
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, 0, dependency.Wrap(err)
		}
		out = append(out, c)
	}
	return out, total, dependency.Wrap(rows.Err())
}

func (s *PostgresStore) Update(ctx context.Context, code string, f Fields) (Campaign, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Campaign{}, dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The row lock serialises this edit with the checkout saga's quota changes, so the in-use checks hold.
	current, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignColumns+` FROM orders.voucher_campaigns WHERE voucher_code = $1 FOR UPDATE`, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, dependency.Wrap(err)
	}
	next, err := applyUpdate(current, f, time.Now().UTC())
	if err != nil {
		return Campaign{}, err
	}
	updated, err := scanCampaign(tx.QueryRow(ctx, `UPDATE orders.voucher_campaigns SET name = $2, discount_pct = $3, total_quota = $4,
		per_customer_limit = $5, starts_at = $6, ends_at = $7, status = $8, version = version + 1, updated_at = NOW()
		WHERE voucher_code = $1 RETURNING `+campaignColumns,
		code, next.Name, next.DiscountPct, next.TotalQuota, next.PerCustomerLimit, next.StartsAt, next.EndsAt, next.Status))
	if err != nil {
		return Campaign{}, dependency.Wrap(err)
	}
	return updated, dependency.Wrap(tx.Commit(ctx))
}

// Reserve implements Reserver. The campaign row lock serialises every reservation of one code, so the quota
// and the per-customer limit are checked and taken atomically (SC10: the last use goes to exactly one order).
func (s *PostgresStore) Reserve(ctx context.Context, code, userID, orderID string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	campaign, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignColumns+` FROM orders.voucher_campaigns WHERE voucher_code = $1 FOR UPDATE`, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return &Refusal{Reason: ReasonNotFound}
	}
	if err != nil {
		return dependency.Wrap(err)
	}
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM orders.voucher_usages WHERE voucher_code = $1 AND order_id = $2`, code, orderID).Scan(&state)
	if err == nil && state != "RELEASED" {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dependency.Wrap(err)
	}
	var uses int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM orders.voucher_usages
		WHERE voucher_code = $1 AND user_id = $2 AND state IN ('RESERVED', 'CONSUMED')`, code, userID).Scan(&uses); err != nil {
		return dependency.Wrap(err)
	}
	if reason := Evaluate(campaign, uses, now); reason != "" {
		return &Refusal{Reason: reason}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO orders.voucher_usages (voucher_code, user_id, order_id, state) VALUES ($1, $2, $3, 'RESERVED')
		ON CONFLICT (voucher_code, order_id) DO UPDATE SET state = 'RESERVED', user_id = EXCLUDED.user_id`, code, userID, orderID); err != nil {
		return dependency.Wrap(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE orders.voucher_campaigns SET reserved_quota = reserved_quota + 1, version = version + 1, updated_at = NOW()
		WHERE voucher_code = $1`, code); err != nil {
		return dependency.Wrap(err)
	}
	return dependency.Wrap(tx.Commit(ctx))
}

// Release implements Reserver.
func (s *PostgresStore) Release(ctx context.Context, code, orderID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM orders.voucher_campaigns WHERE voucher_code = $1 FOR UPDATE`, code); err != nil {
		return dependency.Wrap(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE orders.voucher_usages SET state = 'RELEASED' WHERE voucher_code = $1 AND order_id = $2 AND state = 'RESERVED'`, code, orderID)
	if err != nil {
		return dependency.Wrap(err)
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `UPDATE orders.voucher_campaigns SET reserved_quota = reserved_quota - 1, version = version + 1, updated_at = NOW()
			WHERE voucher_code = $1`, code); err != nil {
			return dependency.Wrap(err)
		}
	}
	return dependency.Wrap(tx.Commit(ctx))
}
