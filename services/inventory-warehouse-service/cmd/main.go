package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/migrations"
)

func NewServer(repo *repository.InventoryRepository, pgConnected bool) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "inventory-warehouse-service healthy", map[string]any{
			"service":        "inventory-warehouse-service",
			"postgresSchema": "inventory",
			"mongoColl":      "wh_receiving_inspections",
			"pgConnected":    pgConnected,
		}, nil)
	})

	mux.HandleFunc("GET /api/v1/inventory/{skuId}", func(w http.ResponseWriter, r *http.Request) {
		skuID := r.PathValue("skuId")
		warehouseID := r.URL.Query().Get("warehouseId")
		if warehouseID == "" {
			warehouseID = "WH-HCM-01"
		}
		st, ok, err := repo.GetStock(r.Context(), warehouseID, skuID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
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
			"inTransit":   st.InTransit,
			"available":   st.Available(),
			"version":     st.Version,
		}, nil)
	})

	// P03, SC02, SC03: Giữ tồn kho nguyên tử 10 phút cho toàn bộ đơn
	mux.HandleFunc("POST /api/v1/inventory/reservations", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID     string               `json:"orderId"`
			WarehouseID string               `json:"warehouseId"`
			Lines       []domain.ReserveLine `json:"lines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrderID == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Dữ liệu giữ tồn không hợp lệ", nil)
			return
		}
		rec, err := repo.ReserveForOrder(r.Context(), req.OrderID, req.WarehouseID, req.Lines)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "INSUFFICIENT_STOCK", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Giữ hàng 10 phút thành công (P03)", rec, nil)
	})

	// SC02: Chuyển Giữ tạm -> Cam kết chưa xuất khi thu tiền thành công
	mux.HandleFunc("POST /api/v1/inventory/reservations/{orderId}/commit", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		rec, err := repo.CommitOrderReservation(r.Context(), orderID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "COMMIT_RESERVATION_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Cam kết giữ tồn kho thành công (SC02)", rec, nil)
	})

	// SC05: Nhả Giữ tạm về lại Khả dụng khi thanh toán thất bại hoặc hết hạn
	mux.HandleFunc("POST /api/v1/inventory/reservations/{orderId}/release", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("orderId")
		rec, err := repo.ReleaseOrderReservation(r.Context(), orderID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "RELEASE_RESERVATION_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Nhả giữ tồn kho về Khả dụng thành công", rec, nil)
	})

	// SC02, I08, SC12: the warehouse hands the parcel to the courier. Only warehouse staff and managers may do it,
	// and only in a warehouse their token covers. The warehouse is the one the reservation was made in;
	// it never comes from a client-supplied header or a hard-coded value.
	handover := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID string `json:"orderId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrderID == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "orderId is required for handover", nil)
			return
		}
		reservation, err := repo.GetReservation(r.Context(), req.OrderID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "HANDOVER_FAILED", err.Error(), nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, reservation.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		rec, err := repo.HandoverOrderShipment(r.Context(), req.OrderID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "HANDOVER_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Bàn giao vận chuyển và trừ tồn thực tế thành công (SC02)", rec, nil)
	}
	mux.Handle("POST /api/v1/inventory/shipments/handover",
		auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(http.HandlerFunc(handover))))

	return middleware.Chain(mux)
}

// Config is the typed start-up configuration of the inventory service.
type Config struct {
	Port        string
	DatabaseURL string
}

// loadConfig reads and validates the environment, including the mandatory JWT secrets (needed to verify
// access tokens). It also installs the secrets into pkg/auth. Every problem is reported in one error.
func loadConfig() (Config, error) {
	l := config.NewLoader()
	auth.LoadSecrets(l)
	cfg := Config{
		Port:        l.Optional("INVENTORY_WAREHOUSE_PORT", "8082"),
		DatabaseURL: l.Optional("DATABASE_URL_INVENTORY", "postgres://inventory_svc_user:inventory_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=inventory"),
	}
	return cfg, l.Err()
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[inventory-warehouse-service] %v", err)
	}
	port, pgURL := cfg.Port, cfg.DatabaseURL

	if migrate.EnabledOnStart() {
		if _, err := migrate.Up(context.Background(), pgURL, migrations.FS); err != nil {
			log.Fatalf("[inventory-warehouse-service] migrations failed: %v", err)
		}
	}

	pgPool, err := db.ConnectPostgres(context.Background(), pgURL)
	if err := dependency.CheckStartup("inventory-warehouse-service", "postgres", err); err != nil {
		log.Fatal(err)
	}
	if pgPool != nil {
		defer pgPool.Close()
	}

	repo := repository.NewInventoryRepository(pgPool)
	log.Printf("[inventory-warehouse-service] listening on :%s (schema: inventory, pgConnected: %v)", port, pgPool != nil)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, pgPool != nil)))
}
