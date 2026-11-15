package voucher

import (
	"context"
	"errors"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
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
