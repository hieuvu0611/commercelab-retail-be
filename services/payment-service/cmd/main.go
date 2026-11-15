package main

import (
	"context"
	"encoding/json"
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
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/orderclient"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/refund"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/migrations"
)

// defaultRefundInterval is how often pending refunds are paid out (and retried after a provider failure).
const defaultRefundInterval = "3s"

// minWebhookSecretBytes is the shortest webhook signing secret accepted outside APP_ENV=dev.
const minWebhookSecretBytes = 32

// NewServer builds the payment service handler.
func NewServer(engine *domain.PaymentEngine, store attempt.Store, opts Options) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "payment-service healthy", map[string]any{
			"service":        "payment-service",
			"postgresSchema": "payments",
			"mongoColl":      "payment_raw_webhooks (TTL 90d)",
		}, nil)
	})

	// I03, SC08: reserve the refund ceiling before paying out. Only other services may call it.
	mux.Handle("POST /api/v1/payments/{orderId}/refund-reservations", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		var req struct {
			AmountVND int64 `json:"amountVnd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "The refund amount is invalid", nil)
			return
		}
		summary, err := engine.ReserveRefundCeiling(orderID, req.AmountVND)
		if err != nil {
			response.Error(w, r, http.StatusConflict, "REFUND_CEILING_EXCEEDED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Refund ceiling reserved (I03)", summary, nil)
	})))

	registerAttemptRoutes(mux, store, opts)
	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("PAYMENT_PORT", "8084")
	pgURL := config.EnvOrDefault("DATABASE_URL_PAYMENTS", "postgres://payment_svc_user:payment_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=payments")

	secrets := config.NewLoader()
	auth.LoadSecrets(secrets)
	internalauth.LoadToken(secrets)
	webhookSecret := secrets.Secret("PAYMENT_WEBHOOK_SECRET", minWebhookSecretBytes)
	if err := secrets.Err(); err != nil {
		log.Fatalf("[payment-service] %v", err)
	}
	// The outcome simulator is for development: on in APP_ENV=dev, otherwise only when asked for explicitly.
	simulator := config.EnvOrDefault("PAYMENT_SIMULATOR_ENABLED", "") == "true" ||
		(config.IsDev() && config.EnvOrDefault("PAYMENT_SIMULATOR_ENABLED", "") != "false")

	ctx := context.Background()
	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(ctx, pgURL, migrations.FS); err != nil {
			log.Fatalf("[payment-service] migrations failed: %v", err)
		}
	}
	pool, pgErr := db.ConnectPostgres(ctx, pgURL)
	if err := dependency.CheckStartup("payment-service", "postgres", pgErr); err != nil {
		log.Fatal(err)
	}
	var store attempt.Store = attempt.NewMemoryStore()
	if pool != nil {
		defer pool.Close()
		store = attempt.NewPostgresStore(pool)
	}

	if pool != nil {
		if err := outbox.Start(ctx, "payment-service", pool, outbox.PaymentsTable); err != nil {
			log.Fatalf("[payment-service] %v", err)
		}
	}
	refundEvery, err := time.ParseDuration(config.EnvOrDefault("PAYMENT_REFUND_INTERVAL", defaultRefundInterval))
	if err != nil || refundEvery < 500*time.Millisecond {
		log.Fatalf("[payment-service] PAYMENT_REFUND_INTERVAL must be a duration of at least 500ms: %v", err)
	}
	// Refund obligations (cancelled paid orders, late captures) are paid out through the simulated provider.
	go (&refund.Worker{Store: store, Provider: refund.NewSimulator()}).Run(ctx, refundEvery)
	orders := orderclient.New(config.EnvOrDefault("ORDER_SERVICE_URL", "http://localhost:8083"), svcclient.New(svcclient.Config{MaxRetries: 2}))
	log.Printf("[payment-service] listening on :%s (schema: payments, simulator: %v)", port, simulator)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(domain.NewPaymentEngine(), store, Options{SimulatorEnabled: simulator, WebhookSecret: webhookSecret, Orders: orders})))
}
