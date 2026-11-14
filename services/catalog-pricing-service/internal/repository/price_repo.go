package repository

import (
	"context"
	"slices"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/jackc/pgx/v5"
)

// effectivePeriod returns the period of periods (any order) that is in effect at the given time.
func effectivePeriod(periods []domain.PricePeriod, at time.Time) (domain.PricePeriod, bool) {
	var best domain.PricePeriod
	found := false
	for _, p := range periods {
		active := !p.EffectiveFrom.After(at) && (p.EffectiveTo == nil || p.EffectiveTo.After(at))
		if active && (!found || p.EffectiveFrom.After(best.EffectiveFrom)) {
			best, found = p, true
		}
	}
	return best, found
}

// SKUsWithActivePrice returns the given SKU ids that have a price in effect now.
func (r *CatalogPricingRepository) SKUsWithActivePrice(ctx context.Context, skuIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(skuIDs) == 0 {
		return out, nil
	}
	now := r.now()
	if r.pgPool != nil {
		rows, err := r.pgPool.Query(ctx, `
			SELECT DISTINCT sku_id FROM sku_price_history
			WHERE sku_id = ANY($1) AND effective_from <= $2 AND (effective_to IS NULL OR effective_to > $2)`, skuIDs, now)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, dependency.Wrap(err)
			}
			out[id] = true
		}
		return out, dependency.Wrap(rows.Err())
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range skuIDs {
		if _, ok := effectivePeriod(r.history[id], now); ok {
			out[id] = true
		}
	}
	return out, nil
}

// SetSkuPrice records a price period. Periods that start at or after the new one are superseded (they never took
// effect, because the new one is not in the past) and the period it interrupts is closed at its start.
func (r *CatalogPricingRepository) SetSkuPrice(ctx context.Context, in domain.SetPriceInput) error {
	if r.pgPool != nil {
		return dependency.Wrap(pgx.BeginFunc(ctx, r.pgPool, func(tx pgx.Tx) error { return setSkuPriceTx(ctx, tx, in, r.now()) }))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sp, ok := r.skuPrices[in.SkuID]
	if !ok {
		sp = SkuPrice{SkuID: in.SkuID, ProductID: in.ProductID, VatRatePct: 10, IsPublished: in.Orderable}
	}
	sp.SkuName = in.SkuName
	history := slices.DeleteFunc(slices.Clone(r.history[in.SkuID]), func(p domain.PricePeriod) bool {
		return !p.EffectiveFrom.Before(in.EffectiveFrom)
	})
	for i := range history {
		if history[i].EffectiveTo == nil || history[i].EffectiveTo.After(in.EffectiveFrom) {
			to := in.EffectiveFrom
			history[i].EffectiveTo = &to
		}
	}
	history = append(history, domain.PricePeriod{UnitPriceVND: in.UnitPriceVND, EffectiveFrom: in.EffectiveFrom, ChangedBy: in.ChangedBy})
	slices.SortFunc(history, func(a, b domain.PricePeriod) int { return b.EffectiveFrom.Compare(a.EffectiveFrom) })
	r.history[in.SkuID] = history
	if !in.EffectiveFrom.After(r.now()) {
		sp.UnitPriceVND = in.UnitPriceVND
	}
	r.skuPrices[in.SkuID] = sp
	return nil
}

func setSkuPriceTx(ctx context.Context, tx pgx.Tx, in domain.SetPriceInput, now time.Time) error {
	// The upsert also takes the row lock that serializes concurrent price changes of the same SKU.
	if _, err := tx.Exec(ctx, `
		INSERT INTO sku_prices (sku_id, product_id, sku_name, unit_price_vnd, is_published, effective_from)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (sku_id) DO UPDATE SET sku_name = EXCLUDED.sku_name, updated_at = NOW()`,
		in.SkuID, in.ProductID, in.SkuName, in.UnitPriceVND, in.Orderable, in.EffectiveFrom); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sku_price_history WHERE sku_id = $1 AND effective_from >= $2`, in.SkuID, in.EffectiveFrom); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sku_price_history SET effective_to = $2
		WHERE sku_id = $1 AND (effective_to IS NULL OR effective_to > $2)`, in.SkuID, in.EffectiveFrom); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sku_price_history (sku_id, unit_price_vnd, effective_from, changed_by) VALUES ($1, $2, $3, $4)`,
		in.SkuID, in.UnitPriceVND, in.EffectiveFrom, in.ChangedBy); err != nil {
		return err
	}
	if !in.EffectiveFrom.After(now) {
		if _, err := tx.Exec(ctx, `
			UPDATE sku_prices SET unit_price_vnd = $2, effective_from = $3, effective_to = NULL, updated_at = NOW()
			WHERE sku_id = $1`, in.SkuID, in.UnitPriceVND, in.EffectiveFrom); err != nil {
			return err
		}
	}
	return nil
}

// SkuPriceHistory returns every period of the SKU, newest first.
func (r *CatalogPricingRepository) SkuPriceHistory(ctx context.Context, skuID string) ([]domain.PricePeriod, error) {
	if r.pgPool != nil {
		rows, err := r.pgPool.Query(ctx, `
			SELECT unit_price_vnd, effective_from, effective_to, changed_by FROM sku_price_history
			WHERE sku_id = $1 ORDER BY effective_from DESC`, skuID)
		if err != nil {
			return nil, dependency.Wrap(err)
		}
		defer rows.Close()
		out := []domain.PricePeriod{}
		for rows.Next() {
			var p domain.PricePeriod
			if err := rows.Scan(&p.UnitPriceVND, &p.EffectiveFrom, &p.EffectiveTo, &p.ChangedBy); err != nil {
				return nil, dependency.Wrap(err)
			}
			out = append(out, p)
		}
		return out, dependency.Wrap(rows.Err())
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.history[skuID]), nil
}
