package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/migrations"
)

// NewServer builds the order service handler. vouchers is the voucher campaign service.
func NewServer(engine *domain.CheckoutEngine, vouchers *voucher.Service) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "order-checkout-service healthy", map[string]any{
			"service":        "order-checkout-service",
			"postgresSchema": "orders",
			"mongoColl":      "order_commercial_snapshots",
		}, nil)
	})

	// B03, I02, SC01, SC04: Đặt hàng với Idempotency-Key
	mux.HandleFunc("POST /api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" {
			response.Error(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Header Idempotency-Key là bắt buộc (I02)", nil)
			return
		}
		var req struct {
			OrderID         string `json:"orderId"`
			UserID          string `json:"userId"`
			QuoteID         string `json:"quoteId"`
			VoucherCode     string `json:"voucherCode"`
			TotalPayableVND int64  `json:"totalPayableVnd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Payload đặt hàng không hợp lệ", nil)
			return
		}
		if req.OrderID == "" {
			req.OrderID = "O-001"
		}
		order, replayed, err := engine.PlaceOrder(idemKey, req.OrderID, req.UserID, req.QuoteID, req.VoucherCode, req.TotalPayableVND)
		if err != nil {
			if errors.Is(err, domain.ErrIdempotencyConflict) {
				response.Error(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error(), nil)
				return
			}
			response.Error(w, r, http.StatusUnprocessableEntity, "CHECKOUT_REJECTED", err.Error(), nil)
			return
		}
		status := http.StatusCreated
		msg := "Tiếp nhận đơn hàng và giữ tồn 10 phút thành công"
		if replayed {
			status = http.StatusOK
			msg = "Trả lại đơn hàng từ Idempotency-Key đã xử lý (SC04)"
		}
		response.JSON(w, status, msg, order, nil)
	})

	registerVoucherRoutes(mux, vouchers)
	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("ORDER_CHECKOUT_PORT", "8083")
	pgURL := config.EnvOrDefault("DATABASE_URL_ORDERS", "postgres://order_svc_user:order_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=orders")

	secrets := config.NewLoader()
	auth.LoadSecrets(secrets)
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
	// Without Postgres (ALLOW_MEMORY_FALLBACK=true, dev/test only) the seeded development vouchers are used.
	var store voucher.Store = voucher.NewMemoryStore(voucher.DevCampaigns()...)
	if pool != nil {
		defer pool.Close()
		store = voucher.NewPostgresStore(pool)
	}

	log.Printf("[order-checkout-service] listening on :%s (schema: orders, mongo: order_commercial_snapshots)", port)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(domain.NewCheckoutEngine(), voucher.NewService(store))))
}
