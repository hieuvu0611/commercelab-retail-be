// Command seed loads the demo catalog (categories, products, SKUs, prices). Run `make migrate` first.
package main

import (
	"context"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/seed"
)

func main() {
	pgURL := config.EnvOrDefault("DATABASE_URL_PRICING", "postgres://pricing_svc_user:pricing_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=pricing")
	mongoURI := config.EnvOrDefault("MONGO_URI", "mongodb://mongo_admin:mongo_password@localhost:27017/commerce_lab_doc?authSource=admin")
	mongoDB := config.EnvOrDefault("MONGO_DATABASE", "commerce_lab_doc")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, pgURL)
	if err != nil {
		log.Fatalf("[seed] postgres is required: %v", err)
	}
	defer pool.Close()
	_, mongoStore, err := db.ConnectMongo(ctx, mongoURI, mongoDB)
	if err != nil {
		log.Fatalf("[seed] mongodb is required: %v", err)
	}
	defer func() { _ = mongoStore.Close(context.Background()) }()

	summary, err := seed.Run(ctx, mongoStore, repository.NewCatalogPricingRepository(pool, mongoStore))
	if err != nil {
		log.Fatalf("[seed] failed: %v", err)
	}
	log.Printf("[seed] done: %s", summary)
}
