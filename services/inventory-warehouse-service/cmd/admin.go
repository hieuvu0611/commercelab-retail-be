package main

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

const (
	defaultPageLimit  = 50
	maxPageLimit      = 200
	maxOpeningLines   = 500
	maxSkuIDLength    = 64
	maxReasonLength   = 500
	openingStockPath  = "POST /api/v1/admin/inventory/opening-stock"
	adminStockPath    = "GET /api/v1/admin/inventory/stock"
	adminMovementPath = "GET /api/v1/admin/inventory/movements"
)

// stockView is the staff-facing shape of one stock record (exact counts; never exposed publicly, D-12).
func stockView(st domain.StockItem) map[string]any {
	return map[string]any{
		"skuId": st.SkuID, "warehouseId": st.WarehouseID, "onHand": st.OnHand, "unsellable": st.Unsellable,
		"reserved": st.Reserved, "committed": st.Committed, "inTransit": st.InTransit,
		"available": st.Available(), "version": st.Version,
	}
}

func validationError(w http.ResponseWriter, r *http.Request, message string) {
	response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", message, nil)
}

// warehouseParam returns the mandatory ?warehouse= value after checking the caller's warehouse scope.
func warehouseParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	warehouseID := strings.TrimSpace(r.URL.Query().Get("warehouse"))
	if warehouseID == "" {
		validationError(w, r, "warehouse is required")
		return "", false
	}
	claims, _ := auth.ClaimsFrom(r.Context())
	if !auth.CanAccessWarehouse(claims, warehouseID) {
		auth.ForbiddenWarehouse(w, r)
		return "", false
	}
	return warehouseID, true
}

func pageParams(w http.ResponseWriter, r *http.Request) (page, limit int, ok bool) {
	page, limit = 1, defaultPageLimit
	q := r.URL.Query()
	var err error
	if v := q.Get("page"); v != "" {
		if page, err = strconv.Atoi(v); err != nil || page < 1 {
			validationError(w, r, "page must be a positive integer")
			return 0, 0, false
		}
	}
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxPageLimit {
			validationError(w, r, "limit must be between 1 and "+strconv.Itoa(maxPageLimit))
			return 0, 0, false
		}
	}
	return page, limit, true
}

// registerAdminRoutes adds the warehouse back-office API: opening stock, stock buckets and the movement history.
func registerAdminRoutes(mux *http.ServeMux, repo *repository.InventoryRepository) {
	manager := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("WAREHOUSE_MGR")(h))
	}
	warehouseStaff := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(h))
	}

	mux.Handle(openingStockPath, manager(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WarehouseID string               `json:"warehouseId"`
			Reason      string               `json:"reason"`
			Lines       []domain.ReserveLine `json:"lines"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		req.WarehouseID, req.Reason = strings.TrimSpace(req.WarehouseID), strings.TrimSpace(req.Reason)
		switch {
		case req.WarehouseID == "":
			validationError(w, r, "warehouseId is required")
			return
		case req.Reason == "" || len(req.Reason) > maxReasonLength:
			validationError(w, r, "reason is required (at most 500 characters)")
			return
		case len(req.Lines) == 0 || len(req.Lines) > maxOpeningLines:
			validationError(w, r, "lines must hold between 1 and 500 entries")
			return
		}
		for _, line := range req.Lines {
			if len(line.SkuID) > maxSkuIDLength {
				validationError(w, r, "skuId is too long")
				return
			}
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, req.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		err := repo.RecordOpeningStock(r.Context(), req.WarehouseID, claims.UserID, req.Reason, req.Lines)
		var exists *repository.OpeningStockExistsError
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
		case errors.As(err, &exists):
			response.Error(w, r, http.StatusConflict, "OPENING_STOCK_ALREADY_RECORDED", err.Error(), nil)
		case errors.Is(err, domain.ErrInvalidLines):
			validationError(w, r, "every line needs a skuId and a positive quantity")
		case err != nil:
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Opening stock could not be recorded", nil)
		default:
			response.JSON(w, http.StatusCreated, "Opening stock recorded", map[string]any{"warehouseId": req.WarehouseID, "lines": req.Lines}, nil)
		}
	}))

	mux.Handle(adminStockPath, warehouseStaff(func(w http.ResponseWriter, r *http.Request) {
		warehouseID, ok := warehouseParam(w, r)
		if !ok {
			return
		}
		items, err := repo.ListStock(r.Context(), warehouseID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		views := make([]map[string]any, 0, len(items))
		for _, st := range items {
			views = append(views, stockView(st))
		}
		response.JSON(w, http.StatusOK, "Stock retrieved", views, nil)
	}))

	mux.Handle(adminMovementPath, warehouseStaff(func(w http.ResponseWriter, r *http.Request) {
		warehouseID, ok := warehouseParam(w, r)
		if !ok {
			return
		}
		page, limit, ok := pageParams(w, r)
		if !ok {
			return
		}
		filter := repository.MovementFilter{WarehouseID: warehouseID, SkuID: strings.TrimSpace(r.URL.Query().Get("skuId")), Page: page, Limit: limit}
		items, total, err := repo.ListMovements(r.Context(), filter)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Movements could not be read", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Movements retrieved", items, &response.Metadata{
			Total: total, Page: page, Limit: limit, PageCount: int(math.Ceil(float64(total) / float64(limit))),
		})
	}))
}
