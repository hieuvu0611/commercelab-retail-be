package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/hieuvu0211/full-service-shopping/be/pkg/config"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/middleware"
	"github.com/hieuvu0211/full-service-shopping/be/pkg/response"
	"github.com/hieuvu0211/full-service-shopping/be/services/inventory-warehouse-service/internal/domain"
)

func main() {
	port := config.EnvOrDefault("INVENTORY_WAREHOUSE_PORT", "8082")
	engine := domain.NewLedgerEngine([]domain.StockItem{
		{SkuID: "SKU-BALO-BLACK-20L", WarehouseID: "WH-HCM-01", OnHand: 10, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
		{SkuID: "SKU-BOTTLE-BLUE-500ML", WarehouseID: "WH-HCM-01", OnHand: 20, Unsellable: 0, Reserved: 0, Committed: 0, Version: 1},
	})

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "inventory-warehouse-service healthy", map[string]any{
			"service":        "inventory-warehouse-service",
			"postgresSchema": "inventory",
			"mongoColl":      "wh_receiving_inspections",
		}, nil)
	})

	mux.HandleFunc("GET /api/v1/inventory/{skuId}", func(w http.ResponseWriter, r *http.Request) {
		skuID := r.PathValue("skuId")
		st, ok := engine.GetStock(skuID)
		if !ok {
			response.Error(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "Không tìm thấy tồn kho cho SKU "+skuID, nil)
			return
		}
		response.JSON(w, http.StatusOK, "Lấy thông tin tồn kho thành công", map[string]any{
			"skuId":       st.SkuID,
			"warehouseId": st.WarehouseID,
			"onHand":      st.OnHand,
			"unsellable":  st.Unsellable,
			"reserved":    st.Reserved,
			"committed":   st.Committed,
			"available":   st.Available(),
			"version":     st.Version,
		}, nil)
	})

	mux.HandleFunc("POST /api/v1/inventory/reservations", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID string               `json:"orderId"`
			Lines   []domain.ReserveLine `json:"lines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Dữ liệu giữ tồn không hợp lệ", nil)
			return
		}
		if err := engine.ReserveAllOrNothing(req.Lines); err != nil {
			response.Error(w, r, http.StatusConflict, "INSUFFICIENT_STOCK", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Giữ hàng 10 phút thành công (P03)", map[string]any{
			"orderId": req.OrderID,
			"status":  "ACTIVE_10M",
		}, nil)
	})

	log.Printf("[inventory-warehouse-service] listening on :%s (schema: inventory)", port)
	log.Fatal(http.ListenAndServe(":"+port, middleware.Chain(mux)))
}
