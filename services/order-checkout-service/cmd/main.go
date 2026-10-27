package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/eventbus"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/repository"
)

func NewServer(repo *repository.OrderRepository, bus *eventbus.Bus, pgConnected, mongoConnected bool) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "order-checkout-service healthy", map[string]any{
			"service":        "order-checkout-service",
			"postgresSchema": "orders",
			"mongoColl":      "order_commercial_snapshots",
			"pgConnected":    pgConnected,
			"mongoConnected": mongoConnected,
		}, nil)
	})

	// B03, I02, SC01, SC04: Place order with mandatory Idempotency-Key
	mux.HandleFunc("POST /api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" {
			response.Error(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key header is required (I02)", nil)
			return
		}
		var req struct {
			OrderID         string                 `json:"orderId"`
			UserID          string                 `json:"userId"`
			QuoteID         string                 `json:"quoteId"`
			VoucherCode     string                 `json:"voucherCode"`
			SubtotalVND     int64                  `json:"subtotalVnd"`
			DiscountVND     int64                  `json:"discountVnd"`
			ShippingFeeVND  int64                  `json:"shippingFeeVnd"`
			TotalPayableVND int64                  `json:"totalPayableVnd"`
			Items           []domain.OrderItemLine `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Invalid order checkout payload", nil)
			return
		}
		if req.OrderID == "" {
			req.OrderID = "O-001"
		}
		if req.UserID == "" {
			req.UserID = "user-lan"
		}
		if req.SubtotalVND == 0 {
			req.SubtotalVND = 700000
			req.DiscountVND = 70000
			req.ShippingFeeVND = 30000
		}
		if req.TotalPayableVND == 0 {
			req.TotalPayableVND = 660000
		}

		order, replayed, err := repo.PlaceOrder(
			r.Context(), idemKey, req.OrderID, req.UserID, req.QuoteID, req.VoucherCode,
			req.SubtotalVND, req.DiscountVND, req.ShippingFeeVND, req.TotalPayableVND, req.Items,
		)
		if err != nil {
			if errors.Is(err, domain.ErrIdempotencyConflict) {
				response.Error(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error(), nil)
				return
			}
			response.Error(w, r, http.StatusUnprocessableEntity, "CHECKOUT_REJECTED", err.Error(), nil)
			return
		}
		status := http.StatusCreated
		msg := "Order accepted and inventory reserved for 10 minutes"
		if replayed {
			status = http.StatusOK
			msg = "Returned existing order from processed Idempotency-Key (SC04)"
		} else if bus != nil {
			_ = bus.Publish(r.Context(), "evt-order-"+order.OrderID, events.EventOrderCreated, "ORDER", order.OrderID, map[string]any{
				"orderId":         order.OrderID,
				"userId":          order.UserID,
				"totalPayableVnd": order.TotalPayableVND,
			})
		}
		response.JSON(w, status, msg, order, nil)
	})

	// List orders (optionally filtered by userId)
	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("userId")
		orders, err := repo.ListOrders(r.Context(), userID)
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "ORDER_LIST_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Orders retrieved successfully", orders, &response.Metadata{
			Total:     int64(len(orders)),
			Page:      1,
			PageCount: 1,
			Limit:     20,
		})
	})

	// Get order detail by orderId
	mux.HandleFunc("GET /api/v1/orders/{orderId}", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		order, err := repo.GetOrder(r.Context(), orderID)
		if err != nil {
			response.Error(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Order detail retrieved successfully", order, nil)
	})

	// B03, P05, SC05: Apply payment outcome (CAPTURED, UNKNOWN_30M, FAILED, EXPIRED_30M)
	mux.HandleFunc("POST /api/v1/orders/{orderId}/payment-outcome", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		var req struct {
			Outcome string `json:"outcome"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Outcome == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Payment outcome is required", nil)
			return
		}
		order, err := repo.ApplyPaymentOutcome(r.Context(), orderID, req.Outcome)
		if err != nil {
			if errors.Is(err, domain.ErrOrderNotFound) {
				response.Error(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", err.Error(), nil)
				return
			}
			response.Error(w, r, http.StatusConflict, "INVALID_ORDER_STATE", err.Error(), nil)
			return
		}
		if bus != nil && req.Outcome == "CAPTURED" {
			_ = bus.Publish(r.Context(), "evt-confirmed-"+order.OrderID, events.EventOrderConfirmed, "ORDER", order.OrderID, map[string]any{
				"orderId": order.OrderID,
				"status":  order.Status,
			})
		}
		response.JSON(w, http.StatusOK, "Order payment outcome applied successfully", order, nil)
	})

	return middleware.Chain(mux)
}

func main() {
	port := config.EnvOrDefault("ORDER_CHECKOUT_PORT", "8083")
	pgURL := config.EnvOrDefault("DATABASE_URL_ORDERS", "postgres://order_svc_user:order_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=orders")
	mongoURI := config.EnvOrDefault("MONGO_URI", "mongodb://mongo_admin:mongo_password@localhost:27017/commerce_lab_doc?authSource=admin")
	mongoDB := config.EnvOrDefault("MONGO_DATABASE", "commerce_lab_doc")

	ctx := context.Background()
	pgPool, pgErr := db.ConnectPostgres(ctx, pgURL)
	if pgErr != nil {
		log.Printf("[order-checkout-service] postgres unavailable (%v), using in-memory CheckoutEngine fallback", pgErr)
	} else {
		defer pgPool.Close()
	}

	_, mongoStore, mongoErr := db.ConnectMongo(ctx, mongoURI, mongoDB)
	if mongoErr != nil {
		log.Printf("[order-checkout-service] mongodb unavailable (%v), skipping snapshot persistence", mongoErr)
	}

	repo := repository.NewOrderRepository(pgPool, mongoStore)
	bus := eventbus.New()
	log.Printf("[order-checkout-service] listening on :%s (schema: orders, mongo: order_commercial_snapshots)", port)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, bus, pgPool != nil, mongoStore != nil)))
}
