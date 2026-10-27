package main

import (
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/domain"
)

func main() {
	port := config.EnvOrDefault("FULFILLMENT_SHIPPING_PORT", "8085")
	arb := domain.NewFulfillmentArbitrator()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "fulfillment-shipping-service healthy", map[string]any{
			"service":        "fulfillment-shipping-service",
			"postgresSchema": "fulfillment",
			"mongoColl":      "fulfillment_timelines ($push)",
		}, nil)
	})

	// SC06: Kho nhận lệnh dừng khẩn cấp khi khách hủy
	mux.HandleFunc("POST /api/v1/shipments/{shipmentId}/stop-for-cancel", func(w http.ResponseWriter, r *http.Request) {
		shipmentID := r.PathValue("shipmentId")
		sh, err := arb.RequestStopForCancel(shipmentID)
		if err != nil {
			response.Error(w, r, http.StatusConflict, "STOP_SHIPMENT_REJECTED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Đã dừng đóng gói kiện hàng thành công (SC06 Nhánh A)", sh, nil)
	})

	// SC06: Bưu tá quét nhận bàn giao kiện hàng
	mux.HandleFunc("POST /api/v1/shipments/{shipmentId}/handover", func(w http.ResponseWriter, r *http.Request) {
		shipmentID := r.PathValue("shipmentId")
		sh, err := arb.ConfirmHandover(shipmentID)
		if err != nil {
			response.Error(w, r, http.StatusConflict, "HANDOVER_REJECTED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Bàn giao kiện hàng cho vận chuyển thành công", sh, nil)
	})

	log.Printf("[fulfillment-shipping-service] listening on :%s (schema: fulfillment, mongo: fulfillment_timelines)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
