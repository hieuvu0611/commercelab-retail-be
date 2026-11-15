// Command seed-stock loads demo opening stock for the seeded catalog SKUs into the inventory schema
// (some in stock, some low, some out). Run `make migrate` first. Idempotent.
package main

import (
	"context"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/seed"
)

func main() {
	pgURL := config.EnvOrDefault("DATABASE_URL_INVENTORY", "postgres://inventory_svc_user:inventory_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=inventory")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, pgURL)
	if err != nil {
		log.Fatalf("[seed-stock] postgres is required: %v", err)
	}
	defer pool.Close()
	plan := seed.StockPlan()
	created, err := seed.LoadStock(ctx, pool, plan)
	if err != nil {
		log.Fatalf("[seed-stock] failed: %v", err)
	}
	log.Printf("[seed-stock] done: %d SKUs in plan, %d new stock rows in %s", len(plan), created, seed.SeedWarehouse)
}
