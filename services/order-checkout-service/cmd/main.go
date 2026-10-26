package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/middleware"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/response"
	"github.com/hieuvu0211/full-service-shopping/be/services/order-checkout-service/internal/domain"
)

func main() {
	port := config.EnvOrDefault("ORDER_CHECKOUT_PORT", "8083")
	engine := domain.NewCheckoutEngine()
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

	log.Printf("[order-checkout-service] listening on :%s (schema: orders, mongo: order_commercial_snapshots)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
