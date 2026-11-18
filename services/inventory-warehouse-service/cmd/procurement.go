package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

func extractRoles(claims *auth.Claims) []string {
	if claims == nil {
		return nil
	}
	if len(claims.Roles) > 0 {
		return claims.Roles
	}
	if claims.Role != "" {
		return []string{claims.Role}
	}
	return nil
}

// procurementWarehouseFilter uses nil for all warehouses and an empty slice for no access.
func procurementWarehouseFilter(claims *auth.Claims, requested string) []string {
	if requested != "" {
		return []string{requested}
	}
	if claims == nil {
		return []string{}
	}
	for _, scope := range claims.WarehouseScopes {
		if scope == auth.AllWarehouses {
			return nil
		}
	}
	return append([]string{}, claims.WarehouseScopes...)
}

func requireProcurementWarehouse(w http.ResponseWriter, r *http.Request, warehouseID string, found bool, err error) bool {
	if dependency.IsUnavailable(err) {
		dependency.WriteUnavailable(w, r, err)
		return false
	}
	if err != nil {
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve warehouse record", nil)
		return false
	}
	if !found {
		response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Warehouse record not found", nil)
		return false
	}
	claims, _ := auth.ClaimsFrom(r.Context())
	if !auth.CanAccessWarehouse(claims, warehouseID) {
		auth.ForbiddenWarehouse(w, r)
		return false
	}
	return true
}

// registerProcurementRoutes adds suppliers, PR, and PO management APIs (B02 Stage 2, P01, P10, SC09).
func registerProcurementRoutes(mux *http.ServeMux, repo *repository.InventoryRepository) {
	staffOrMgr := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("PROCUREMENT_MGR", "WAREHOUSE_MGR", "WAREHOUSE_STAFF", "OPS_MGR", "OPS_DIRECTOR", "FINANCE_MGR", "ADMIN")(h))
	}
	procurementCreator := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("PROCUREMENT_MGR", "WAREHOUSE_MGR", "OPS_MGR", "ADMIN")(h))
	}
	approver := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("PROCUREMENT_MGR", "OPS_MGR", "OPS_DIRECTOR", "FINANCE_MGR", "ADMIN")(h))
	}

	// GET /api/v1/admin/procurement/suppliers
	mux.Handle("GET /api/v1/admin/procurement/suppliers", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		list, err := repo.ListSuppliers(r.Context())
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve suppliers", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Suppliers retrieved", list, nil)
	}))

	// POST /api/v1/admin/procurement/suppliers
	mux.Handle("POST /api/v1/admin/procurement/suppliers", procurementCreator(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SupplierID   string `json:"supplierId"`
			Name         string `json:"name"`
			Code         string `json:"code"`
			ContactEmail string `json:"contactEmail"`
			Phone        string `json:"phone"`
			Address      string `json:"address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Code) == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Supplier name and code are required", nil)
			return
		}
		supplier, err := repo.CreateSupplier(r.Context(), domain.Supplier{
			SupplierID:   strings.TrimSpace(req.SupplierID),
			Name:         strings.TrimSpace(req.Name),
			Code:         strings.TrimSpace(req.Code),
			ContactEmail: strings.TrimSpace(req.ContactEmail),
			Phone:        strings.TrimSpace(req.Phone),
			Address:      strings.TrimSpace(req.Address),
			Active:       true,
		})
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "SUPPLIER_CREATE_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Supplier created successfully", supplier, nil)
	}))

	// GET /api/v1/admin/procurement/orders
	mux.Handle("GET /api/v1/admin/procurement/orders", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		warehouseID := strings.TrimSpace(r.URL.Query().Get("warehouseId"))
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		claims, _ := auth.ClaimsFrom(r.Context())
		if warehouseID != "" && !auth.CanAccessWarehouse(claims, warehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		list, err := repo.ListPurchaseOrders(r.Context(), procurementWarehouseFilter(claims, warehouseID), status)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve purchase orders", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Purchase orders retrieved", list, nil)
	}))

	// GET /api/v1/admin/procurement/orders/{poId}
	mux.Handle("GET /api/v1/admin/procurement/orders/{poId}", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		poID := r.PathValue("poId")
		po, ok, err := repo.GetPurchaseOrder(r.Context(), poID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		if !ok {
			response.Error(w, r, http.StatusNotFound, "PO_NOT_FOUND", "Purchase order not found", nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, po.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		response.JSON(w, http.StatusOK, "Purchase order retrieved", po, nil)
	}))

	// POST /api/v1/admin/procurement/orders
	mux.Handle("POST /api/v1/admin/procurement/orders", procurementCreator(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			POID        string                     `json:"poId"`
			SupplierID  string                     `json:"supplierId"`
			WarehouseID string                     `json:"warehouseId"`
			Notes       string                     `json:"notes"`
			Lines       []domain.PurchaseOrderLine `json:"lines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SupplierID) == "" {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "supplierId and valid lines are required", nil)
			return
		}
		if req.WarehouseID == "" {
			req.WarehouseID = domain.DefaultWarehouseID
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, req.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		po, err := repo.CreatePurchaseOrder(r.Context(), domain.PurchaseOrder{
			POID:        strings.TrimSpace(req.POID),
			SupplierID:  strings.TrimSpace(req.SupplierID),
			WarehouseID: req.WarehouseID,
			CreatedBy:   claims.UserID,
			Notes:       strings.TrimSpace(req.Notes),
			Lines:       req.Lines,
		})
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, domain.ErrInvalidPOLines) {
			response.Error(w, r, http.StatusBadRequest, "INVALID_LINES", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusConflict, "CREATE_PO_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Purchase order created", po, nil)
	}))

	// POST /api/v1/admin/procurement/orders/{poId}/approve
	mux.Handle("POST /api/v1/admin/procurement/orders/{poId}/approve", approver(func(w http.ResponseWriter, r *http.Request) {
		poID := r.PathValue("poId")
		claims, _ := auth.ClaimsFrom(r.Context())
		roles := extractRoles(claims)

		target, found, lookupErr := repo.GetPurchaseOrder(r.Context(), poID)
		if !requireProcurementWarehouse(w, r, target.WarehouseID, found, lookupErr) {
			return
		}
		po, err := repo.ApprovePurchaseOrder(r.Context(), poID, claims.UserID, roles)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		switch {
		case errors.Is(err, domain.ErrPurchaseOrderNotFound):
			response.Error(w, r, http.StatusNotFound, "PO_NOT_FOUND", err.Error(), nil)
		case errors.Is(err, domain.ErrSelfApprovalRejected):
			response.Error(w, r, http.StatusForbidden, "MAKER_CHECKER_VIOLATION", err.Error(), nil)
		case errors.Is(err, domain.ErrThresholdExceeded):
			response.Error(w, r, http.StatusForbidden, "THRESHOLD_EXCEEDED", err.Error(), nil)
		case errors.Is(err, domain.ErrInvalidPOState):
			response.Error(w, r, http.StatusConflict, "INVALID_PO_STATE", err.Error(), nil)
		case err != nil:
			response.Error(w, r, http.StatusConflict, "APPROVAL_FAILED", err.Error(), nil)
		default:
			response.JSON(w, http.StatusOK, "Purchase order approved (P10 Maker != Checker verified)", po, nil)
		}
	}))

	// POST /api/v1/admin/procurement/orders/{poId}/reject
	mux.Handle("POST /api/v1/admin/procurement/orders/{poId}/reject", approver(func(w http.ResponseWriter, r *http.Request) {
		poID := r.PathValue("poId")
		claims, _ := auth.ClaimsFrom(r.Context())
		var req struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		target, found, lookupErr := repo.GetPurchaseOrder(r.Context(), poID)
		if !requireProcurementWarehouse(w, r, target.WarehouseID, found, lookupErr) {
			return
		}
		po, err := repo.RejectPurchaseOrder(r.Context(), poID, claims.UserID, req.Reason)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		switch {
		case errors.Is(err, domain.ErrPurchaseOrderNotFound):
			response.Error(w, r, http.StatusNotFound, "PO_NOT_FOUND", err.Error(), nil)
		case errors.Is(err, domain.ErrInvalidPOState):
			response.Error(w, r, http.StatusConflict, "INVALID_PO_STATE", err.Error(), nil)
		case err != nil:
			response.Error(w, r, http.StatusConflict, "REJECT_FAILED", err.Error(), nil)
		default:
			response.JSON(w, http.StatusOK, "Purchase order rejected", po, nil)
		}
	}))

	// POST /api/v1/admin/inventory/receiving/inspections (SC09 inbound receiving & QC)
	whReceivingStaff := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR", "ADMIN")(h))
	}
	mux.Handle("POST /api/v1/admin/inventory/receiving/inspections", whReceivingStaff(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			POID  string                           `json:"poId"`
			Notes string                           `json:"notes"`
			Lines []domain.ReceivingInspectionLine `json:"lines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.POID) == "" || len(req.Lines) == 0 {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "poId and non-empty inspection lines are required", nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		po, found, lookupErr := repo.GetPurchaseOrder(r.Context(), strings.TrimSpace(req.POID))
		if !requireProcurementWarehouse(w, r, po.WarehouseID, found, lookupErr) {
			return
		}
		insp, err := repo.RecordReceivingInspection(r.Context(), strings.TrimSpace(req.POID), claims.UserID, strings.TrimSpace(req.Notes), req.Lines)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, domain.ErrPurchaseOrderNotFound) {
			response.Error(w, r, http.StatusNotFound, "PO_NOT_FOUND", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "RECEIVING_QC_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Inbound goods inspected and received into warehouse (SC09)", insp, nil)
	}))

	// GET /api/v1/admin/inventory/receiving/inspections
	mux.Handle("GET /api/v1/admin/inventory/receiving/inspections", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		warehouseID := strings.TrimSpace(r.URL.Query().Get("warehouseId"))
		poID := strings.TrimSpace(r.URL.Query().Get("poId"))
		claims, _ := auth.ClaimsFrom(r.Context())
		if warehouseID != "" && !auth.CanAccessWarehouse(claims, warehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		list, err := repo.ListReceivingInspections(r.Context(), procurementWarehouseFilter(claims, warehouseID), poID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve receiving inspections", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Receiving inspections retrieved", list, nil)
	}))

	// POST /api/v1/admin/inventory/supplier-returns
	mux.Handle("POST /api/v1/admin/inventory/supplier-returns", whReceivingStaff(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ReturnID    string                      `json:"returnId"`
			SupplierID  string                      `json:"supplierId"`
			WarehouseID string                      `json:"warehouseId"`
			POID        string                      `json:"poId"`
			Notes       string                      `json:"notes"`
			Lines       []domain.SupplierReturnLine `json:"lines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SupplierID) == "" || len(req.Lines) == 0 {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "supplierId and non-empty return lines are required", nil)
			return
		}
		if req.WarehouseID == "" {
			req.WarehouseID = domain.DefaultWarehouseID
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, req.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		ret, err := repo.CreateSupplierReturn(r.Context(), domain.SupplierReturn{
			ReturnID:    strings.TrimSpace(req.ReturnID),
			SupplierID:  strings.TrimSpace(req.SupplierID),
			WarehouseID: req.WarehouseID,
			POID:        strings.TrimSpace(req.POID),
			CreatedBy:   claims.UserID,
			Notes:       strings.TrimSpace(req.Notes),
			Lines:       req.Lines,
		})
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "CREATE_SUPPLIER_RETURN_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Supplier return dossier created", ret, nil)
	}))

	// POST /api/v1/admin/inventory/supplier-returns/{returnId}/dispatch
	mux.Handle("POST /api/v1/admin/inventory/supplier-returns/{returnId}/dispatch", whReceivingStaff(func(w http.ResponseWriter, r *http.Request) {
		returnID := r.PathValue("returnId")
		claims, _ := auth.ClaimsFrom(r.Context())
		var req struct {
			TrackingNumber string `json:"trackingNumber"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		target, found, lookupErr := repo.GetSupplierReturn(r.Context(), returnID)
		if !requireProcurementWarehouse(w, r, target.WarehouseID, found, lookupErr) {
			return
		}
		ret, err := repo.DispatchSupplierReturn(r.Context(), returnID, claims.UserID, strings.TrimSpace(req.TrackingNumber))
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, domain.ErrInsufficientStock) {
			response.Error(w, r, http.StatusConflict, "INSUFFICIENT_UNSELLABLE_STOCK", "Insufficient unsellable stock available for supplier return", nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "DISPATCH_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Supplier return dispatched: unsellable stock reduced (I01 verified)", ret, nil)
	}))

	// GET /api/v1/admin/inventory/supplier-returns
	mux.Handle("GET /api/v1/admin/inventory/supplier-returns", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		warehouseID := strings.TrimSpace(r.URL.Query().Get("warehouseId"))
		supplierID := strings.TrimSpace(r.URL.Query().Get("supplierId"))
		claims, _ := auth.ClaimsFrom(r.Context())
		if warehouseID != "" && !auth.CanAccessWarehouse(claims, warehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		list, err := repo.ListSupplierReturns(r.Context(), procurementWarehouseFilter(claims, warehouseID), supplierID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve supplier returns", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Supplier returns retrieved", list, nil)
	}))

	// GET /api/v1/admin/inventory/supplier-returns/{returnId}
	mux.Handle("GET /api/v1/admin/inventory/supplier-returns/{returnId}", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		returnID := r.PathValue("returnId")
		ret, ok, err := repo.GetSupplierReturn(r.Context(), returnID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		if !ok {
			response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Supplier return not found", nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, ret.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		response.JSON(w, http.StatusOK, "Supplier return retrieved", ret, nil)
	}))

	// POST /api/v1/admin/inventory/stocktakes
	mux.Handle("POST /api/v1/admin/inventory/stocktakes", whReceivingStaff(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WarehouseID string `json:"warehouseId"`
			Notes       string `json:"notes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.WarehouseID == "" {
			req.WarehouseID = domain.DefaultWarehouseID
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, req.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		stk, err := repo.CreateStocktake(r.Context(), req.WarehouseID, claims.UserID, strings.TrimSpace(req.Notes))
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "CREATE_STOCKTAKE_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusCreated, "Stocktake session started", stk, nil)
	}))

	// GET /api/v1/admin/inventory/stocktakes
	mux.Handle("GET /api/v1/admin/inventory/stocktakes", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		warehouseID := strings.TrimSpace(r.URL.Query().Get("warehouseId"))
		claims, _ := auth.ClaimsFrom(r.Context())
		if warehouseID != "" && !auth.CanAccessWarehouse(claims, warehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		list, err := repo.ListStocktakes(r.Context(), procurementWarehouseFilter(claims, warehouseID))
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve stocktakes", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Stocktakes retrieved", list, nil)
	}))

	// GET /api/v1/admin/inventory/stocktakes/{stocktakeId}
	mux.Handle("GET /api/v1/admin/inventory/stocktakes/{stocktakeId}", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		stocktakeID := r.PathValue("stocktakeId")
		stk, ok, err := repo.GetStocktake(r.Context(), stocktakeID)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		if !ok {
			response.Error(w, r, http.StatusNotFound, "NOT_FOUND", "Stocktake not found", nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, stk.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		response.JSON(w, http.StatusOK, "Stocktake retrieved", stk, nil)
	}))

	// POST /api/v1/admin/inventory/stocktakes/{stocktakeId}/count
	// Submits counted stock; if shortage, immediately blocks sales for discrepancy (I01)
	mux.Handle("POST /api/v1/admin/inventory/stocktakes/{stocktakeId}/count", whReceivingStaff(func(w http.ResponseWriter, r *http.Request) {
		stocktakeID := r.PathValue("stocktakeId")
		var req struct {
			Lines []domain.StocktakeLine `json:"lines"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Lines) == 0 {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Non-empty count lines are required", nil)
			return
		}
		target, found, lookupErr := repo.GetStocktake(r.Context(), stocktakeID)
		if !requireProcurementWarehouse(w, r, target.WarehouseID, found, lookupErr) {
			return
		}
		stk, adjustments, err := repo.SubmitStocktakeCount(r.Context(), stocktakeID, req.Lines)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, domain.ErrStocktakeNotInProgress) {
			response.Error(w, r, http.StatusConflict, "STOCKTAKE_NOT_IN_PROGRESS", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "SUBMIT_COUNT_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Physical counts recorded (discrepancy sales blocked under I01)", map[string]any{
			"stocktake":   stk,
			"adjustments": adjustments,
		}, nil)
	}))

	// GET /api/v1/admin/inventory/adjustments
	mux.Handle("GET /api/v1/admin/inventory/adjustments", staffOrMgr(func(w http.ResponseWriter, r *http.Request) {
		warehouseID := strings.TrimSpace(r.URL.Query().Get("warehouseId"))
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		claims, _ := auth.ClaimsFrom(r.Context())
		if warehouseID != "" && !auth.CanAccessWarehouse(claims, warehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		list, err := repo.ListStockAdjustments(r.Context(), procurementWarehouseFilter(claims, warehouseID), status)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to retrieve adjustments", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Adjustments retrieved", list, nil)
	}))

	// POST /api/v1/admin/inventory/adjustments/{adjustmentId}/decide
	adjustmentApprover := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("WAREHOUSE_MGR", "SUPPLY_CHAIN_MGR", "OPS_MGR", "FINANCE_MGR", "ADMIN")(h))
	}
	mux.Handle("POST /api/v1/admin/inventory/adjustments/{adjustmentId}/decide", adjustmentApprover(func(w http.ResponseWriter, r *http.Request) {
		adjustmentID := r.PathValue("adjustmentId")
		claims, _ := auth.ClaimsFrom(r.Context())
		roles := extractRoles(claims)

		var req struct {
			Action string `json:"action"` // APPROVE or REJECT
			Note   string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Action != "APPROVE" && req.Action != "REJECT") {
			response.Error(w, r, http.StatusBadRequest, "INVALID_PAYLOAD", "Action must be APPROVE or REJECT", nil)
			return
		}

		warehouseID, found, lookupErr := repo.AdjustmentWarehouse(r.Context(), adjustmentID)
		if !requireProcurementWarehouse(w, r, warehouseID, found, lookupErr) {
			return
		}
		adj, err := repo.DecideStockAdjustment(r.Context(), adjustmentID, claims.UserID, roles, req.Action, strings.TrimSpace(req.Note))
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, domain.ErrAdjustmentNotPending) {
			response.Error(w, r, http.StatusConflict, "ADJUSTMENT_NOT_PENDING", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusForbidden, "ADJUSTMENT_DECISION_FAILED", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Adjustment decision processed (P10 Maker != Checker verified)", adj, nil)
	}))
}
