package promotion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const promoColumns = `campaign_id, code, name, discount_type, discount_value, max_discount_vnd, min_order_vnd,
	eligible_category_ids, eligible_sku_ids, customer_segment, auto_apply, stackable, priority,
	total_quota, reserved_quota, consumed_quota, budget_vnd, budget_reserved_vnd, budget_consumed_vnd,
	per_customer_limit, starts_at, ends_at, status, version, created_by, created_at, updated_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	var catRaw, skuRaw []byte
	err := row.Scan(
		&c.CampaignID, &c.Code, &c.Name, &c.DiscountType, &c.DiscountValue, &c.MaxDiscountVND, &c.MinOrderVND,
		&catRaw, &skuRaw, &c.CustomerSegment, &c.AutoApply, &c.Stackable, &c.Priority,
		&c.TotalQuota, &c.ReservedQuota, &c.ConsumedQuota, &c.BudgetVND, &c.BudgetReservedVND, &c.BudgetConsumedVND,
		&c.PerCustomerLimit, &c.StartsAt, &c.EndsAt, &c.Status, &c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return Campaign{}, err
	}
	_ = json.Unmarshal(catRaw, &c.EligibleCategoryIDs)
	_ = json.Unmarshal(skuRaw, &c.EligibleSkuIDs)
	return enrich(c), nil
}

func (s *PostgresStore) ListActive(ctx context.Context, now time.Time) ([]Campaign, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+promoColumns+` FROM orders.promotion_campaigns
		WHERE status = 'ACTIVE' AND (starts_at IS NULL OR starts_at <= $1) AND (ends_at IS NULL OR ends_at > $1)
		ORDER BY priority ASC, campaign_id ASC`, now)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		out = append(out, c)
	}
	return out, dependency.Wrap(rows.Err())
}

func (s *PostgresStore) List(ctx context.Context, status, q string) ([]Campaign, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+promoColumns+` FROM orders.promotion_campaigns
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR code ILIKE '%' || $2 || '%' OR name ILIKE '%' || $2 || '%')
		ORDER BY priority ASC, created_at DESC`, status, strings.TrimSpace(q))
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		out = append(out, c)
	}
	return out, dependency.Wrap(rows.Err())
}

func (s *PostgresStore) Get(ctx context.Context, campaignID string) (Campaign, error) {
	c, err := scanCampaign(s.pool.QueryRow(ctx, `SELECT `+promoColumns+` FROM orders.promotion_campaigns WHERE campaign_id = $1`, campaignID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	return c, dependency.Wrap(err)
}

func (s *PostgresStore) Create(ctx context.Context, campaignID, actor string, f Fields) (Campaign, error) {
	if err := ValidateFields(f, true); err != nil {
		return Campaign{}, err
	}
	catJSON, _ := json.Marshal(f.EligibleCategoryIDs)
	skuJSON, _ := json.Marshal(f.EligibleSkuIDs)
	seg := f.CustomerSegment
	if seg == "" {
		seg = SegmentAll
	}
	c, err := scanCampaign(s.pool.QueryRow(ctx, `INSERT INTO orders.promotion_campaigns (
		campaign_id, code, name, discount_type, discount_value, max_discount_vnd, min_order_vnd,
		eligible_category_ids, eligible_sku_ids, customer_segment, auto_apply, stackable, priority,
		total_quota, budget_vnd, per_customer_limit, starts_at, ends_at, status, created_by
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) RETURNING `+promoColumns,
		campaignID, NormalizeCode(f.Code), strings.TrimSpace(f.Name), f.DiscountType, f.DiscountValue, f.MaxDiscountVND, f.MinOrderVND,
		string(catJSON), string(skuJSON), seg, f.AutoApply, f.Stackable, f.Priority,
		f.TotalQuota, f.BudgetVND, f.PerCustomerLimit, f.StartsAt, f.EndsAt, f.Status, actor))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Campaign{}, ErrCodeExists
	}
	return c, dependency.Wrap(err)
}

func (s *PostgresStore) Update(ctx context.Context, campaignID string, f Fields) (Campaign, error) {
	if err := ValidateFields(f, false); err != nil {
		return Campaign{}, err
	}
	catJSON, _ := json.Marshal(f.EligibleCategoryIDs)
	skuJSON, _ := json.Marshal(f.EligibleSkuIDs)
	seg := f.CustomerSegment
	if seg == "" {
		seg = SegmentAll
	}
	c, err := scanCampaign(s.pool.QueryRow(ctx, `UPDATE orders.promotion_campaigns SET
		name = $2, discount_type = $3, discount_value = $4, max_discount_vnd = $5, min_order_vnd = $6,
		eligible_category_ids = $7, eligible_sku_ids = $8, customer_segment = $9, auto_apply = $10,
		stackable = $11, priority = $12, total_quota = $13, budget_vnd = $14, per_customer_limit = $15,
		starts_at = $16, ends_at = $17, status = $18, version = version + 1, updated_at = NOW()
		WHERE campaign_id = $1 RETURNING `+promoColumns,
		campaignID, strings.TrimSpace(f.Name), f.DiscountType, f.DiscountValue, f.MaxDiscountVND, f.MinOrderVND,
		string(catJSON), string(skuJSON), seg, f.AutoApply, f.Stackable, f.Priority,
		f.TotalQuota, f.BudgetVND, f.PerCustomerLimit, f.StartsAt, f.EndsAt, f.Status))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	return c, dependency.Wrap(err)
}

func (s *PostgresStore) CustomerUsesMap(ctx context.Context, userID string, campaignIDs []string) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT campaign_id, COUNT(*) FROM orders.promotion_usages
		WHERE user_id = $1 AND state IN ('RESERVED', 'CONSUMED') AND (cardinality($2::text[]) = 0 OR campaign_id = ANY($2))
		GROUP BY campaign_id`, userID, campaignIDs)
	if err != nil {
		return nil, dependency.Wrap(err)
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var cid string
		var cnt int64
		if err := rows.Scan(&cid, &cnt); err != nil {
			return nil, dependency.Wrap(err)
		}
		out[cid] = cnt
	}
	return out, dependency.Wrap(rows.Err())
}

func (s *PostgresStore) IsNewCustomer(ctx context.Context, userID string) (bool, error) {
	var cnt int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders.orders WHERE user_id = $1 AND status NOT IN ('CANCELLED', 'FAILED')`, userID).Scan(&cnt)
	if err != nil {
		return false, dependency.Wrap(err)
	}
	return cnt == 0, nil
}

func (s *PostgresStore) Reserve(ctx context.Context, campaignID, userID, orderID string, discountVND int64, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	c, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+promoColumns+` FROM orders.promotion_campaigns WHERE campaign_id = $1 FOR UPDATE`, campaignID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return dependency.Wrap(err)
	}

	var prevState string
	err = tx.QueryRow(ctx, `SELECT state FROM orders.promotion_usages WHERE campaign_id = $1 AND order_id = $2`, campaignID, orderID).Scan(&prevState)
	if err == nil && prevState != "RELEASED" {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dependency.Wrap(err)
	}

	var uses int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM orders.promotion_usages
		WHERE campaign_id = $1 AND user_id = $2 AND state IN ('RESERVED', 'CONSUMED')`, campaignID, userID).Scan(&uses); err != nil {
		return dependency.Wrap(err)
	}
	if c.CalcAvailableQuota() <= 0 {
		return ErrQuotaExhausted
	}
	if rem := c.CalcRemainingBudget(); rem >= 0 && discountVND > rem {
		return ErrBudgetExceeded
	}
	if uses >= int64(c.PerCustomerLimit) {
		return ErrCustomerLimitHit
	}
	if c.Status != StatusActive || (c.StartsAt != nil && now.Before(*c.StartsAt)) || (c.EndsAt != nil && !now.Before(*c.EndsAt)) {
		return ErrValidation
	}

	if _, err := tx.Exec(ctx, `INSERT INTO orders.promotion_usages (campaign_id, user_id, order_id, discount_vnd, state)
		VALUES ($1, $2, $3, $4, 'RESERVED')
		ON CONFLICT (campaign_id, order_id) DO UPDATE SET state = 'RESERVED', discount_vnd = EXCLUDED.discount_vnd`,
		campaignID, userID, orderID, discountVND); err != nil {
		return dependency.Wrap(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE orders.promotion_campaigns
		SET reserved_quota = reserved_quota + 1, budget_reserved_vnd = budget_reserved_vnd + $2, version = version + 1, updated_at = NOW()
		WHERE campaign_id = $1`, campaignID, discountVND); err != nil {
		return dependency.Wrap(err)
	}
	return dependency.Wrap(tx.Commit(ctx))
}

func (s *PostgresStore) ConsumeOrder(ctx context.Context, orderID string) error {
	return s.transitionOrder(ctx, orderID, "RESERVED", "CONSUMED")
}

func (s *PostgresStore) ReleaseOrder(ctx context.Context, orderID string) error {
	return s.transitionOrder(ctx, orderID, "RESERVED", "RELEASED")
}

func (s *PostgresStore) transitionOrder(ctx context.Context, orderID, fromState, toState string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dependency.Wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT id, campaign_id, discount_vnd FROM orders.promotion_usages
		WHERE order_id = $1 AND state = $2 FOR UPDATE`, orderID, fromState)
	if err != nil {
		return dependency.Wrap(err)
	}
	type item struct {
		id         int64
		campaignID string
		disc       int64
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.campaignID, &it.disc); err != nil {
			rows.Close()
			return dependency.Wrap(err)
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dependency.Wrap(err)
	}

	for _, it := range items {
		if _, err := tx.Exec(ctx, `UPDATE orders.promotion_usages SET state = $2 WHERE id = $1`, it.id, toState); err != nil {
			return dependency.Wrap(err)
		}
		if toState == "CONSUMED" {
			if _, err := tx.Exec(ctx, `UPDATE orders.promotion_campaigns SET
				reserved_quota = reserved_quota - 1, consumed_quota = consumed_quota + 1,
				budget_reserved_vnd = budget_reserved_vnd - $2, budget_consumed_vnd = budget_consumed_vnd + $2,
				version = version + 1, updated_at = NOW()
				WHERE campaign_id = $1`, it.campaignID, it.disc); err != nil {
				return dependency.Wrap(err)
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE orders.promotion_campaigns SET
				reserved_quota = reserved_quota - 1,
				budget_reserved_vnd = budget_reserved_vnd - $2,
				version = version + 1, updated_at = NOW()
				WHERE campaign_id = $1`, it.campaignID, it.disc); err != nil {
				return dependency.Wrap(err)
			}
		}
	}
	return dependency.Wrap(tx.Commit(ctx))
}
