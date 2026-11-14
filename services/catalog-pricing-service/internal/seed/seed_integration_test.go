//go:build integration

package seed

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

func TestSeedIsIdempotentAndTheStorefrontListsIt(t *testing.T) {
	uri, pgDSN := os.Getenv("MONGO_URI"), os.Getenv("DATABASE_URL_PRICING")
	if uri == "" || pgDSN == "" {
		t.Skip("MONGO_URI / DATABASE_URL_PRICING are not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// A throw-away Mongo database keeps the test independent; Postgres rows are removed afterwards.
	mongoStore, err := db.NewMongoStore(uri, fmt.Sprintf("it_seed_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = mongoStore.Database().Drop(context.Background())
		_ = mongoStore.Close(context.Background())
	})
	pool, err := db.ConnectPostgres(ctx, pgDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := repository.NewCatalogPricingRepository(pool, mongoStore)

	var codes []string
	for _, p := range Products() {
		for _, k := range p.SKUs() {
			codes = append(codes, k.Code)
		}
	}
	t.Cleanup(func() {
		// SC01 rows belong to the migration; everything else was created by this test.
		_, _ = pool.Exec(context.Background(), `DELETE FROM sku_price_history WHERE sku_id = ANY($1) AND changed_by = 'seed'`, codes)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sku_prices WHERE sku_id = ANY($1) AND sku_id NOT IN ('SKU-BALO-BLACK-20L','SKU-BOTTLE-BLUE-500ML')`, codes)
	})

	first, err := Run(ctx, mongoStore, repo)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	counts := func() (cats, products, skus, priceRows, historyRows int) {
		t.Helper()
		d := mongoStore.Database()
		count := func(coll string) int {
			n, err := d.Collection(coll).CountDocuments(ctx, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			return int(n)
		}
		cats, products, skus = count("catalog_categories"), count("catalog_products"), count("catalog_sku_specs")
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sku_prices WHERE sku_id = ANY($1)`, codes).Scan(&priceRows); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sku_price_history WHERE sku_id = ANY($1)`, codes).Scan(&historyRows); err != nil {
			t.Fatal(err)
		}
		return
	}
	c1, p1, s1, pr1, h1 := counts()
	if c1 != 11 || p1 != 30 || s1 != len(codes) || pr1 != len(codes) || first.Products != 30 {
		t.Fatalf("after first run: categories=%d products=%d skus=%d priceRows=%d summary=%v", c1, p1, s1, pr1, first)
	}

	second, err := Run(ctx, mongoStore, repo)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	c2, p2, s2, pr2, h2 := counts()
	if c1 != c2 || p1 != p2 || s1 != s2 || pr1 != pr2 || h1 != h2 || second.CategoriesCreated != 0 || second.PricesCreated != 0 {
		t.Fatalf("second run changed data: (%d,%d,%d,%d,%d) -> (%d,%d,%d,%d,%d), summary=%v", c1, p1, s1, pr1, h1, c2, p2, s2, pr2, h2, second)
	}

	// The storefront lists the seeded catalog, with the scenario prices.
	cats := domain.NewCategoryService(mustCategoryStore(t, ctx, mongoStore))
	products := mustProductStore(t, ctx, mongoStore)
	front := domain.NewStorefrontService(products, cats, repo)
	all, err := front.List(ctx, domain.StorefrontQuery{Limit: 100})
	if err != nil || all.Total != 30 {
		t.Fatalf("storefront total = %d, %v", all.Total, err)
	}
	backpacks, err := front.List(ctx, domain.StorefrontQuery{CategoryPath: "backpacks", Limit: 100})
	if err != nil || backpacks.Total != 11 {
		t.Fatalf("backpacks = %d, %v", backpacks.Total, err)
	}
	city, err := front.Detail(ctx, "city-backpack")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range city.SKUs {
		if k.SkuID == "SKU-BALO-BLACK-20L" && k.UnitPriceVND != nil && *k.UnitPriceVND == 300000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("SC01 SKU missing or mispriced: %+v", city.SKUs)
	}
	bottle, err := front.Detail(ctx, "insulated-bottle")
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, k := range bottle.SKUs {
		if k.SkuID == "SKU-BOTTLE-BLUE-500ML" && k.UnitPriceVND != nil && *k.UnitPriceVND == 100000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("SC01 bottle SKU missing or mispriced: %+v", bottle.SKUs)
	}
	// The scenario quote still prices from the seeded rows.
	sp, err := repo.GetSkuPrice(ctx, "SKU-BALO-BLACK-20L")
	if err != nil || sp.UnitPriceVND != 300000 {
		t.Fatalf("quote price = %+v, %v", sp, err)
	}
}

func mustCategoryStore(t *testing.T, ctx context.Context, m *db.MongoStore) *repository.MongoCategoryStore {
	t.Helper()
	s, err := repository.NewMongoCategoryStore(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustProductStore(t *testing.T, ctx context.Context, m *db.MongoStore) *repository.MongoProductStore {
	t.Helper()
	s, err := repository.NewMongoProductStore(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
