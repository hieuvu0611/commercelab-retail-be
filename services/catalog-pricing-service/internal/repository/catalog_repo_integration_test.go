//go:build integration

package repository

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
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
		"product_id":    productID,
		"name":          "Integration Backpack",
		"category_type": "BACKPACK",
		"status":        "PUBLISHED",
		"specs":         bson.M{"capacityLiters": 20, "material": "Cordura"},
	})
	if err != nil {
		t.Fatalf("insert product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.Database(dbName).Collection("catalog_products").DeleteOne(context.Background(), bson.M{"product_id": productID})
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
	if found.Name != "Integration Backpack" || found.CategoryType != "BACKPACK" {
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
