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
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/svcclient"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/peer"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/migrations"
)

// voucherStore is what the order service needs from the voucher campaigns: admin, eligibility and reservation.
type voucherStore interface {
	voucher.AdminStore
	voucher.Reserver
}

// NewServer builds the order service handler. checkoutSvc places orders; vouchers and admin serve the voucher APIs.
func NewServer(checkoutSvc *checkout.Service, vouchers *voucher.Service, admin *voucher.Admin) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "order-checkout-service healthy", map[string]any{
			"service":        "order-checkout-service",
			"postgresSchema": "orders",
			"mongoColl":      "order_commercial_snapshots",
		}, nil)
	})

	registerOrderRoutes(mux, checkoutSvc)
	registerVoucherRoutes(mux, vouchers)
	registerVoucherAdminRoutes(mux, admin)
	return middleware.Chain(mux)
}

// defaultWarehouseID is the warehouse that fulfils online orders until warehouse routing exists.
const defaultWarehouseID = "WH-HCM-01"

// defaultSweepInterval is how often the order sweeper times out unpaid orders and repairs lost callbacks.
const defaultSweepInterval = "30s"

func main() {
	port := config.EnvOrDefault("ORDER_CHECKOUT_PORT", "8083")
	pgURL := config.EnvOrDefault("DATABASE_URL_ORDERS", "postgres://order_svc_user:order_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=orders")

	secrets := config.NewLoader()
	auth.LoadSecrets(secrets)
	internalauth.LoadToken(secrets)
	if err := secrets.Err(); err != nil {
		log.Fatalf("[order-checkout-service] %v", err)
	}

	ctx := context.Background()
	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(ctx, pgURL, migrations.FS); err != nil {
			log.Fatalf("[order-checkout-service] migrations failed: %v", err)
		}
	}
	pool, pgErr := db.ConnectPostgres(ctx, pgURL)
	if err := dependency.CheckStartup("order-checkout-service", "postgres", pgErr); err != nil {
		log.Fatal(err)
	}
	// Without Postgres (ALLOW_MEMORY_FALLBACK=true, dev/test only) the seeded development vouchers and an
	// in-memory order store are used.
	var (
		store      voucherStore = voucher.NewMemoryStore(voucher.DevCampaigns()...)
		orderStore orders.Store = orders.NewMemoryStore()
	)
	if pool != nil {
		defer pool.Close()
		store = voucher.NewPostgresStore(pool)
		orderStore = orders.NewPostgresStore(pool)
	}
	peers := svcclient.New(svcclient.Config{MaxRetries: 2})
	checkoutSvc := checkout.New(checkout.Deps{
		Orders:      orderStore,
		Quotes:      peer.NewQuotes(config.EnvOrDefault("CATALOG_SERVICE_URL", "http://localhost:8081"), peers),
		Stock:       peer.NewStock(config.EnvOrDefault("INVENTORY_SERVICE_URL", "http://localhost:8082"), peers),
		Payments:    peer.NewPayments(config.EnvOrDefault("PAYMENT_SERVICE_URL", "http://localhost:8084"), peers),
		Vouchers:    store,
		Carts:       peer.NewCarts(config.EnvOrDefault("CATALOG_SERVICE_URL", "http://localhost:8081"), peers),
		WarehouseID: config.EnvOrDefault("ORDER_WAREHOUSE_ID", defaultWarehouseID),
	})

	sweepEvery, err := time.ParseDuration(config.EnvOrDefault("ORDER_SWEEP_INTERVAL", defaultSweepInterval))
	if err != nil || sweepEvery < time.Second {
		log.Fatalf("[order-checkout-service] ORDER_SWEEP_INTERVAL must be a duration of at least 1s: %v", err)
	}
	go checkoutSvc.RunSweeper(ctx, sweepEvery)
	if pool != nil {
		if err := outbox.Start(ctx, "order-checkout-service", pool, outbox.OrdersTable); err != nil {
			log.Fatalf("[order-checkout-service] %v", err)
		}
	}

	log.Printf("[order-checkout-service] listening on :%s (schema: orders, mongo: order_commercial_snapshots)", port)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(checkoutSvc, voucher.NewService(store), voucher.NewAdmin(store))))
}
