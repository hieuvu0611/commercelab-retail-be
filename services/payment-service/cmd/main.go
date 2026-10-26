package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/middleware"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/response"
	"github.com/hieuvu0211/full-service-shopping/be/services/payment-service/internal/domain"
)

func main() {
	port := config.EnvOrDefault("PAYMENT_PORT", "8084")
	engine := domain.NewPaymentEngine()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "payment-service healthy", map[string]any{
			"service":        "payment-service",
			"postgresSchema": "payments",
			"mongoColl":      "payment_raw_webhooks (TTL 90d)",
		}, nil)
	})

	// I03, SC08: Giữ trần hoàn tiền trước khi giải ngân
	mux.HandleFunc("POST /api/v1/payments/{orderId}/refund-reservations", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		var req struct {
			AmountVND int64 `json:"amountVnd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Số tiền hoàn không hợp lệ", nil)
			return
		}
		summary, err := engine.ReserveRefundCeiling(orderID, req.AmountVND)
		if err != nil {
			response.Error(w, r, http.StatusConflict, "REFUND_CEILING_EXCEEDED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Khóa trần hoàn tiền thành công (I03)", summary, nil)
	})

	log.Printf("[payment-service] listening on :%s (schema: payments, mongo: payment_raw_webhooks)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
