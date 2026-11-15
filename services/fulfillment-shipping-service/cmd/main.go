package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/eventconsumer"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/intake"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/migrations"
)

// consumerGroup is the consumer group of this service on the order topic.
const consumerGroup = "fulfillment-shipping-service"

// Deps are the collaborators of the HTTP handler.
type Deps struct {
	Shipments shipment.Store
	// Now is the clock (default: UTC wall clock).
	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

// NewServer builds the HTTP handler of the fulfillment service.
func NewServer(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "fulfillment-shipping-service healthy", map[string]any{
			"service":        "fulfillment-shipping-service",
			"postgresSchema": "fulfillment",
			"mongoColl":      "fulfillment_timelines",
		}, nil)
	})
	registerStaffRoutes(mux, d)
	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("FULFILLMENT_SHIPPING_PORT", "8085")
	pgURL := config.EnvOrDefault("DATABASE_URL_FULFILLMENT", "postgres://fulfillment_svc_user:fulfillment_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=fulfillment")

	secrets := config.NewLoader()
	auth.LoadSecrets(secrets)
	if err := secrets.Err(); err != nil {
		log.Fatalf("[fulfillment-shipping-service] %v", err)
	}

	ctx := context.Background()
	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(ctx, pgURL, migrations.FS); err != nil {
			log.Fatalf("[fulfillment-shipping-service] migrations failed: %v", err)
		}
	}
	pool, pgErr := db.ConnectPostgres(ctx, pgURL)
	if err := dependency.CheckStartup("fulfillment-shipping-service", "postgres", pgErr); err != nil {
		log.Fatal(err)
	}
	var store shipment.Store = shipment.NewMemoryStore()
	if pool != nil {
		defer pool.Close()
		store = shipment.NewPostgresStore(pool)
		if err := outbox.Start(ctx, "fulfillment-shipping-service", pool, shipment.OutboxTable); err != nil {
			log.Fatalf("[fulfillment-shipping-service] %v", err)
		}
	}

	in := &intake.Intake{Store: store, Now: func() time.Time { return time.Now().UTC() }}
	brokers := config.EnvOrDefault("REDPANDA_BROKERS", "localhost:19092")
	go func() {
		err := eventconsumer.Run(ctx, eventconsumer.Config{Brokers: brokers, Group: consumerGroup, Topic: outbox.TopicFor("Order")}, in.Handle)
		if err != nil {
			log.Printf("[fulfillment-shipping-service] order event consumer stopped: %v", err)
		}
	}()

	log.Printf("[fulfillment-shipping-service] listening on :%s (schema: fulfillment, brokers: %s)", port, brokers)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(Deps{Shipments: store})))
}
