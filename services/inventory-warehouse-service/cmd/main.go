package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/migrate"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/redact"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/telemetry"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/migrations"
)

// serverOptions are the runtime settings of the HTTP handler.
type serverOptions struct{ lowStockThreshold int64 }

// ServerOption changes a runtime setting of NewServer.
type ServerOption func(*serverOptions)

// WithLowStockThreshold sets the largest available quantity that is still reported as LOW.
func WithLowStockThreshold(n int64) ServerOption {
	return func(o *serverOptions) { o.lowStockThreshold = n }
}

func NewServer(repo *repository.InventoryRepository, pgConnected bool, opts ...ServerOption) http.Handler {
	options := serverOptions{lowStockThreshold: domain.DefaultLowStockThreshold}
	for _, opt := range opts {
		opt(&options)
	}
	mux := http.NewServeMux()

	mux.Handle("GET /metrics", telemetry.DefaultRegistry().Handler())

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, "inventory-warehouse-service healthy", map[string]any{
			"service":        "inventory-warehouse-service",
			"postgresSchema": "inventory",
			"mongoColl":      "wh_receiving_inspections",
			"pgConnected":    pgConnected,
		}, nil)
	})

	// Exact counts are for warehouse staff only; customers get states from the availability endpoint.
	mux.Handle("GET /api/v1/inventory/{skuId}", auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skuID := r.PathValue("skuId")
		warehouseID := r.URL.Query().Get("warehouseId")
		if warehouseID == "" {
			warehouseID = domain.DefaultWarehouseID
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, warehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
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
		response.JSON(w, http.StatusOK, "Lấy thông tin tồn kho thành công", stockView(st), nil)
	}))))

	mux.HandleFunc("GET /api/v1/inventory/availability", availabilityHandler(repo, options.lowStockThreshold))
	mux.Handle("GET /api/v1/inventory/availability/breakdown", internalauth.Require(availabilityBreakdownHandler(repo)))

	// P03, SC02, SC03: Atomic 10-minute stock reservation across 1 or 2 warehouses
	mux.Handle("POST /api/v1/inventory/reservations", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID     string                       `json:"orderId"`
			WarehouseID string                       `json:"warehouseId"`
			Lines       []domain.ReserveLine         `json:"lines"`
			Allocations []domain.WarehouseAllocation `json:"allocations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrderID == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Dữ liệu giữ tồn không hợp lệ", nil)
			return
		}
		if len(req.Allocations) > 0 {
			recs, err := repo.ReserveAllocations(r.Context(), req.OrderID, req.Allocations)
			if dependency.IsUnavailable(err) {
				dependency.WriteUnavailable(w, r, err)
				return
			}
			if errors.Is(err, repository.ErrWarehouseRequired) || errors.Is(err, domain.ErrInvalidLines) {
				response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", err.Error(), nil)
				return
			}
			if err != nil {
				response.Error(w, r, http.StatusConflict, "INSUFFICIENT_STOCK", err.Error(), nil)
				return
			}
			response.JSON(w, http.StatusCreated, "Giữ hàng 10 phút thành công (P03)", recs, nil)
			return
		}
		rec, err := repo.ReserveForOrder(r.Context(), req.OrderID, req.WarehouseID, req.Lines)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, repository.ErrWarehouseRequired) || errors.Is(err, domain.ErrInvalidLines) {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "INSUFFICIENT_STOCK", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Giữ hàng 10 phút thành công (P03)", rec, nil)
	})))

	// SC02: Chuyển Giữ tạm -> Cam kết chưa xuất khi thu tiền thành công
	mux.Handle("POST /api/v1/inventory/reservations/{orderId}/commit", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})))

	// P05, SC05: payment is unknown, so keep the stock for 30 minutes (from the reservation) instead of 10.
	mux.Handle("POST /api/v1/inventory/reservations/{orderId}/exception-hold", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, err := repo.PlaceExceptionHold(r.Context(), r.PathValue("orderId"))
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
		case errors.Is(err, repository.ErrReservationNotFound):
			response.Error(w, r, http.StatusNotFound, "RESERVATION_NOT_FOUND", err.Error(), nil)
		case errors.Is(err, repository.ErrReservationExpired):
			response.Error(w, r, http.StatusConflict, "RESERVATION_EXPIRED", err.Error(), nil)
		case err != nil:
			response.Error(w, r, http.StatusConflict, "EXCEPTION_HOLD_FAILED", err.Error(), nil)
		default:
			response.JSON(w, http.StatusOK, "Reservation held for 30 minutes (P05)", rec, nil)
		}
	})))

	// SC05: Nhả Giữ tạm về lại Khả dụng khi thanh toán thất bại hoặc hết hạn
	mux.Handle("POST /api/v1/inventory/reservations/{orderId}/release", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})))

	// B05: a paid order is cancelled before handover, so its committed stock goes back to available. It cannot
	// succeed once the stock was handed over (that is the other side of SC06), and repeating it changes nothing.
	mux.Handle("POST /api/v1/inventory/reservations/{orderId}/release-committed", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, err := repo.ReleaseCommittedReservation(r.Context(), r.PathValue("orderId"), domain.SystemActor)
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
		case errors.Is(err, repository.ErrReservationNotFound):
			response.Error(w, r, http.StatusNotFound, "RESERVATION_NOT_FOUND", "No stock reservation exists for this order", nil)
		case err != nil:
			response.Error(w, r, http.StatusConflict, "RELEASE_COMMITTED_FAILED", "The committed stock cannot be released: it was handed over or is not committed", nil)
		default:
			response.JSON(w, http.StatusOK, "Committed stock released", rec, nil)
		}
	})))

	// SC02, I08, SC12: the warehouse hands the parcel to the courier. Only warehouse staff and managers may do it,
	// and only in a warehouse their token covers. The warehouse is the one the reservation was made in;
	// it never comes from a client-supplied header or a hard-coded value.
	executeHandover := func(w http.ResponseWriter, r *http.Request, requireStaffScope, requireBodyWarehouse bool) {
		var req struct {
			OrderID string `json:"orderId"`
			// WarehouseID is optional for staff handover (inferred from reservation); when provided,
			// it must match the warehouse the stock was reserved in.
			WarehouseID string `json:"warehouseId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrderID == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "orderId is required for handover", nil)
			return
		}
		if requireBodyWarehouse && strings.TrimSpace(req.WarehouseID) == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "warehouseId is required for internal handover", nil)
			return
		}
		reservation, err := repo.GetReservationForWarehouse(r.Context(), req.OrderID, req.WarehouseID)
		if errors.Is(err, repository.ErrReservationNotFound) && req.WarehouseID != "" {
			if other, otherErr := repo.GetReservation(r.Context(), req.OrderID); otherErr == nil && other != nil {
				response.Error(w, r, http.StatusConflict, "WAREHOUSE_MISMATCH", "The order's stock is reserved in a different warehouse", nil)
				return
			}
		}
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "HANDOVER_FAILED", err.Error(), nil)
			return
		}
		if requireStaffScope {
			claims, _ := auth.ClaimsFrom(r.Context())
			if !auth.CanAccessWarehouse(claims, reservation.WarehouseID) {
				auth.ForbiddenWarehouse(w, r)
				return
			}
		}
		if req.WarehouseID != "" && req.WarehouseID != reservation.WarehouseID {
			response.Error(w, r, http.StatusConflict, "WAREHOUSE_MISMATCH", "The order's stock is reserved in a different warehouse", nil)
			return
		}
		rec, err := repo.HandoverOrderShipmentForWarehouse(r.Context(), req.OrderID, reservation.WarehouseID)
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
	handover := func(w http.ResponseWriter, r *http.Request) {
		executeHandover(w, r, true, false)
	}
	internalHandover := func(w http.ResponseWriter, r *http.Request) {
		executeHandover(w, r, false, true)
	}
	mux.Handle("POST /api/v1/inventory/shipments/handover",
		auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(http.HandlerFunc(handover))))
	mux.Handle("POST /api/v1/inventory/internal/shipments/handover",
		internalauth.Require(http.HandlerFunc(internalHandover)))

	// SC06: the warehouse confirms it stopped a parcel for a cancellation. Staff only, in the warehouse the stock
	// is reserved in. It and the handover are mutually exclusive on the reservation, so only one of them wins.
	stop := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID     string `json:"orderId"`
			WarehouseID string `json:"warehouseId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrderID == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "orderId is required to stop a shipment", nil)
			return
		}
		reservation, err := repo.GetReservationForWarehouse(r.Context(), req.OrderID, req.WarehouseID)
		if errors.Is(err, repository.ErrReservationNotFound) && req.WarehouseID != "" {
			if other, otherErr := repo.GetReservation(r.Context(), req.OrderID); otherErr == nil && other != nil {
				response.Error(w, r, http.StatusConflict, "WAREHOUSE_MISMATCH", "The order's stock is reserved in a different warehouse", nil)
				return
			}
		}
		if errors.Is(err, repository.ErrReservationNotFound) && req.WarehouseID != "" {
			if other, otherErr := repo.GetReservation(r.Context(), req.OrderID); otherErr == nil && other != nil {
				response.Error(w, r, http.StatusConflict, "WAREHOUSE_MISMATCH", "The order's stock is reserved in a different warehouse", nil)
				return
			}
		}
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
			return
		case errors.Is(err, repository.ErrReservationNotFound):
			response.Error(w, r, http.StatusNotFound, "RESERVATION_NOT_FOUND", "No stock reservation exists for this order", nil)
			return
		case err != nil:
			response.Error(w, r, http.StatusConflict, "RELEASE_COMMITTED_FAILED", "The committed stock cannot be released", nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, reservation.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		if req.WarehouseID != "" && req.WarehouseID != reservation.WarehouseID {
			response.Error(w, r, http.StatusConflict, "WAREHOUSE_MISMATCH", "The order's stock is reserved in a different warehouse", nil)
			return
		}
		rec, err := repo.ReleaseCommittedForWarehouse(r.Context(), req.OrderID, reservation.WarehouseID, claims.UserID)
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
		case err != nil:
			response.Error(w, r, http.StatusConflict, "RELEASE_COMMITTED_FAILED", "The committed stock cannot be released: it was handed over or is not committed", nil)
		default:
			response.JSON(w, http.StatusOK, "Committed stock released", rec, nil)
		}
	}
	mux.Handle("POST /api/v1/inventory/shipments/stop",
		auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(http.HandlerFunc(stop))))

	// SC07: Kho nhận hàng trả -> OnHand +qty, Unsellable +qty, Available giữ nguyên
	mux.Handle("POST /api/v1/inventory/returns/receive", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WarehouseID string `json:"warehouseId"`
			SkuID       string `json:"skuId"`
			Quantity    int64  `json:"quantity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SkuID == "" || req.Quantity <= 0 {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "skuId and positive quantity are required", nil)
			return
		}
		st, err := repo.ReceiveReturnedItem(r.Context(), req.WarehouseID, req.SkuID, req.Quantity, r.Header.Get("Idempotency-Key"))
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "RECEIVE_RETURN_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Kho nhận hàng trả: OnHand +qty, Unsellable +qty (SC07)", stockView(st), nil)
	})))

	// SC07: Thẩm định QC đạt -> Unsellable -qty, Available +qty
	mux.Handle("POST /api/v1/inventory/returns/qc-pass", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WarehouseID string `json:"warehouseId"`
			SkuID       string `json:"skuId"`
			Quantity    int64  `json:"quantity"`
		}
		key := r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SkuID == "" || req.Quantity < 0 || (req.Quantity == 0 && key == "") {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "skuId and a valid QC pass quantity are required", nil)
			return
		}
		st, err := repo.ApproveReturnQC(r.Context(), req.WarehouseID, req.SkuID, req.Quantity, key)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "QC_PASS_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "QC đạt: Chuyển Unsellable sang Available (SC07)", stockView(st), nil)
	})))

	// SC07: Thẩm định QC hỏng -> giữ nguyên trong Unsellable để cách ly/tiêu hủy
	mux.Handle("POST /api/v1/inventory/returns/qc-fail", internalauth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WarehouseID string `json:"warehouseId"`
			SkuID       string `json:"skuId"`
			Quantity    int64  `json:"quantity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SkuID == "" || req.Quantity <= 0 {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "skuId and positive quantity are required", nil)
			return
		}
		st, ok, err := repo.GetStock(r.Context(), req.WarehouseID, req.SkuID)
		if err != nil || !ok {
			response.Error(w, r, http.StatusNotFound, "SKU_NOT_FOUND", "Không tìm thấy tồn kho cho SKU "+req.SkuID, nil)
			return
		}
		response.JSON(w, http.StatusOK, "QC không đạt: Hàng giữ trong Unsellable cách ly (SC07)", stockView(st), nil)
	})))

	registerAdminRoutes(mux, repo)
	registerProcurementRoutes(mux, repo)
	registerTransferRoutes(mux, repo)

	return middleware.Chain(mux)
}

const defaultExpirySweepInterval = 30 * time.Second

// Config is the typed start-up configuration of the inventory service.
type Config struct {
	Port              string
	DatabaseURL       string
	LowStockThreshold int64
	ExpirySweepEvery  time.Duration
}

// loadConfig reads and validates the environment, including the mandatory JWT secrets (needed to verify
// access tokens). It also installs the secrets into pkg/auth. Every problem is reported in one error.
func loadConfig() (Config, error) {
	l := config.NewLoader()
	auth.LoadSecrets(l)
	internalauth.LoadToken(l)
	cfg := Config{
		Port:        l.Optional("INVENTORY_WAREHOUSE_PORT", "8082"),
		DatabaseURL: l.Optional("DATABASE_URL_INVENTORY", "postgres://inventory_svc_user:inventory_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=inventory"),
	}
	cfg.LowStockThreshold = domain.DefaultLowStockThreshold
	if raw := l.Optional("INVENTORY_LOW_STOCK_THRESHOLD", ""); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return cfg, errors.New("INVENTORY_LOW_STOCK_THRESHOLD must be a non-negative integer")
		}
		cfg.LowStockThreshold = n
	}
	cfg.ExpirySweepEvery = defaultExpirySweepInterval
	if raw := l.Optional("INVENTORY_EXPIRY_SWEEP_INTERVAL", ""); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return cfg, errors.New("INVENTORY_EXPIRY_SWEEP_INTERVAL must be a positive duration such as 30s")
		}
		cfg.ExpirySweepEvery = d
	}
	return cfg, l.Err()
}

func main() {
	redact.InstallDefaultLogger()
	tracer := telemetry.Configure("inventory-warehouse-service", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	defer func() { _ = tracer.Shutdown(context.Background()) }()
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
	go repo.RunExpiryWorker(context.Background(), cfg.ExpirySweepEvery)
	log.Printf("[inventory-warehouse-service] listening on :%s (schema: inventory, pgConnected: %v)", port, pgPool != nil)
	log.Fatal(http.ListenAndServe(":"+port, NewServer(repo, pgPool != nil, WithLowStockThreshold(cfg.LowStockThreshold))))
}
