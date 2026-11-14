//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func integrationEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Skipf("%s is not set; run `make test-integration`", key)
	}
	return value
}

// insertProduct writes a published product straight into the authenticated Mongo and returns its id.
func insertProduct(t *testing.T, uri, dbName string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect with the official driver: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	productID := fmt.Sprintf("IT-PROD-%d", time.Now().UnixNano())
	_, err = client.Database(dbName).Collection("catalog_products").InsertOne(ctx, bson.M{
		"_id":               productID,
		"slug":              "integration-backpack-" + productID,
		"name":              "Integration Backpack",
		"primaryCategoryId": "cat-integration",
		"status":            "PUBLISHED",
		"specs":             bson.M{"capacity_liters": 20, "material": "Cordura"},
	})
	if err != nil {
		t.Fatalf("insert product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.Database(dbName).Collection("catalog_products").DeleteOne(context.Background(), bson.M{"_id": productID})
	})
	return productID
}

func TestCatalogReadsRealMongoDocumentsWithAuthentication(t *testing.T) {
	uri := integrationEnv(t, "MONGO_URI")
	dbName := integrationEnv(t, "MONGO_DATABASE")
	pgDSN := integrationEnv(t, "DATABASE_URL_PRICING")

	productID := insertProduct(t, uri, dbName)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, mongoStore, err := db.ConnectMongo(ctx, uri, dbName)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	pool, err := db.ConnectPostgres(ctx, pgDSN)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := NewCatalogPricingRepository(pool, mongoStore)
	products, err := repo.ListPublishedProducts(ctx)
	if err != nil {
		t.Fatalf("list products: %v", err)
	}

	var found *ProductCatalogDoc
	for i := range products {
		if products[i].ProductID == productID {
			found = &products[i]
		}
	}
	if found == nil {
		t.Fatalf("inserted product %s not returned; got %d products", productID, len(products))
	}
	if found.Name != "Integration Backpack" || found.PrimaryCategoryID != "cat-integration" {
		t.Fatalf("unexpected product fields: %+v", found)
	}
	if found.Specs["material"] != "Cordura" {
		t.Fatalf("nested specs not decoded: %+v", found.Specs)
	}
}

func TestMongoRejectsWrongCredentials(t *testing.T) {
	uri := integrationEnv(t, "MONGO_URI")
	dbName := integrationEnv(t, "MONGO_DATABASE")

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse MONGO_URI: %v", err)
	}
	parsed.User = url.UserPassword(parsed.User.Username(), "not-the-password")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := db.ConnectMongo(ctx, parsed.String(), dbName); err == nil {
		t.Fatal("connecting with a wrong password must fail; authentication is not being used")
	}
}

func TestPostgresPricingPortsOpenAndCloseSKUs(t *testing.T) {
	pgDSN := integrationEnv(t, "DATABASE_URL_PRICING")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, pgDSN)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewCatalogPricingRepository(pool, nil)

	productID := fmt.Sprintf("IT-PORT-%d", time.Now().UnixNano())
	skuA, skuB := productID+"-A", productID+"-B"
	for _, sku := range []string{skuA, skuB} {
		if _, err := pool.Exec(ctx, `INSERT INTO sku_prices (sku_id, product_id, sku_name, unit_price_vnd) VALUES ($1, $2, $1, 1000)`, sku, productID); err != nil {
			t.Fatalf("insert price: %v", err)
		}
	}
	for _, sku := range []string{skuA, skuB} {
		if _, err := pool.Exec(ctx, `INSERT INTO sku_price_history (sku_id, unit_price_vnd, effective_from, changed_by) VALUES ($1, 1000, NOW() - INTERVAL '1 hour', 'it')`, sku); err != nil {
			t.Fatalf("insert history: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sku_prices WHERE product_id = $1`, productID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sku_price_history WHERE sku_id LIKE $1`, productID+"%")
	})

	priced, err := repo.SKUsWithActivePrice(ctx, []string{skuA, skuB, "IT-MISSING"})
	if err != nil || !priced[skuA] || !priced[skuB] || priced["IT-MISSING"] {
		t.Fatalf("priced = %v, %v", priced, err)
	}
	if err := repo.SetOrderable(ctx, productID, []string{skuA}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSkuPrice(ctx, skuA); err != nil {
		t.Fatalf("A must quote: %v", err)
	}
	if _, err := repo.GetSkuPrice(ctx, skuB); !errors.Is(err, ErrSkuNotOrderable) {
		t.Fatalf("B must be closed, err = %v", err)
	}
	if err := repo.SetOrderable(ctx, productID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSkuPrice(ctx, skuA); !errors.Is(err, ErrSkuNotOrderable) {
		t.Fatalf("A must be closed, err = %v", err)
	}
}

func TestPostgresPricePeriods(t *testing.T) {
	pgDSN := integrationEnv(t, "DATABASE_URL_PRICING")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, pgDSN)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewCatalogPricingRepository(pool, nil)

	// The migration turned the seeded SC01 prices into open periods.
	seeded, err := repo.GetSkuPrice(ctx, "SKU-BALO-BLACK-20L")
	if err != nil || seeded.UnitPriceVND != 300000 {
		t.Fatalf("seeded price = %+v, %v", seeded, err)
	}

	productID := fmt.Sprintf("IT-PRICE-%d", time.Now().UnixNano())
	sku := productID + "-A"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sku_prices WHERE sku_id = $1`, sku)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sku_price_history WHERE sku_id = $1`, sku)
	})
	now := time.Now().UTC().Truncate(time.Microsecond)
	set := func(price int64, from time.Time) {
		t.Helper()
		err := repo.SetSkuPrice(ctx, domain.SetPriceInput{
			SkuID: sku, ProductID: productID, SkuName: "IT SKU", UnitPriceVND: price, EffectiveFrom: from, ChangedBy: "mkt", Orderable: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// A SKU with only a future price has no price in effect.
	set(500000, now.Add(time.Hour))
	if _, err := repo.GetSkuPrice(ctx, sku); !errors.Is(err, ErrSkuNotOrderable) {
		t.Fatalf("future-only price must not quote, err = %v", err)
	}
	if priced, _ := repo.SKUsWithActivePrice(ctx, []string{sku}); priced[sku] {
		t.Fatal("future-only price must not count as active")
	}

	// An immediate price supersedes the scheduled one that starts later.
	set(400000, now)
	got, err := repo.GetSkuPrice(ctx, sku)
	if err != nil || got.UnitPriceVND != 400000 || got.SkuName != "IT SKU" {
		t.Fatalf("immediate price = %+v, %v", got, err)
	}
	history, _ := repo.SkuPriceHistory(ctx, sku)
	if len(history) != 1 {
		t.Fatalf("superseded scheduled price must be gone: %+v", history)
	}

	// A scheduled change closes the open period at its start and keeps both in history.
	later := now.Add(48 * time.Hour)
	set(450000, later)
	history, _ = repo.SkuPriceHistory(ctx, sku)
	if len(history) != 2 || history[0].UnitPriceVND != 450000 || history[0].EffectiveTo != nil ||
		history[1].EffectiveTo == nil || !history[1].EffectiveTo.Equal(later) || history[1].ChangedBy != "mkt" {
		t.Fatalf("history = %+v", history)
	}
	if got, _ = repo.GetSkuPrice(ctx, sku); got.UnitPriceVND != 400000 {
		t.Fatalf("price before the change = %d", got.UnitPriceVND)
	}

	// The mirror columns on sku_prices follow the period that has started.
	var mirror int64
	if err := pool.QueryRow(ctx, `SELECT unit_price_vnd FROM sku_prices WHERE sku_id = $1`, sku).Scan(&mirror); err != nil || mirror != 400000 {
		t.Fatalf("mirror = %d, %v", mirror, err)
	}

	// Closing the product makes the SKU not orderable even though it has a price.
	if err := repo.SetOrderable(ctx, productID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSkuPrice(ctx, sku); !errors.Is(err, ErrSkuNotOrderable) {
		t.Fatalf("closed SKU, err = %v", err)
	}
	if _, err := repo.GetSkuPrice(ctx, "IT-UNKNOWN"); !errors.Is(err, ErrSkuNotFound) {
		t.Fatalf("unknown SKU, err = %v", err)
	}
}
