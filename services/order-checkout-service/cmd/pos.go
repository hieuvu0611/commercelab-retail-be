package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
)

const roleWarehouseStaff = "WAREHOUSE_STAFF"

func registerPOSRoutes(mux *http.ServeMux, svc *checkout.Service) {
	posAuth := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole(roleWarehouseStaff, roleWarehouseMgr, roleOpsMgr)(h))
	}
	mux.Handle("POST /api/v1/admin/pos/sales", posAuth(createPOSSale(svc)))
	mux.Handle("GET /api/v1/admin/pos/sales", posAuth(listPOSSales(svc)))
}

func canAccessPOSWarehouse(claims *auth.Claims, warehouseID string) bool {
	if claims == nil || warehouseID == "" {
		return false
	}
	if auth.CanAccessWarehouse(claims, warehouseID) {
		return true
	}
	return claims.HasRole(roleOpsMgr) && len(claims.WarehouseScopes) == 0
}

func posWarehouseFilter(claims *auth.Claims) (ids []string, limited bool) {
	if claims == nil {
		return nil, true
	}
	for _, s := range claims.WarehouseScopes {
		if s == auth.AllWarehouses {
			return nil, false
		}
	}
	if claims.HasRole(roleOpsMgr) && len(claims.WarehouseScopes) == 0 {
		return nil, false
	}
	return claims.WarehouseScopes, true
}

func writeWarehouseScopeDenied(w http.ResponseWriter, r *http.Request) {
	response.Error(w, r, http.StatusForbidden, "WAREHOUSE_SCOPE_DENIED", "Warehouse access is denied for this store", nil)
}

func createPOSSale(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			response.Error(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "The Idempotency-Key header is required", nil)
			return
		}
		if !idempotencyKeyPattern.MatchString(key) {
			response.Error(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "The Idempotency-Key must be 1 to 128 characters: letters, digits, . _ : -", nil)
			return
		}

		var body struct {
			WarehouseID     string                  `json:"warehouseId"`
			PaymentMethod   string                  `json:"paymentMethod"`
			CustomerNote    string                  `json:"customerNote"`
			CashReceivedVND int64                   `json:"cashReceivedVnd"`
			Items           []checkout.POSItemInput `json:"items"`
		}
		if !request.DecodeJSON(w, r, &body) {
			return
		}

		warehouseID := strings.TrimSpace(body.WarehouseID)
		paymentMethod := strings.ToUpper(strings.TrimSpace(body.PaymentMethod))
		fieldErrors := map[string][]string{}
		if warehouseID == "" {
			fieldErrors["warehouseId"] = []string{"warehouseId is required"}
		}
		if paymentMethod != checkout.PaymentMethodPOSCash && paymentMethod != checkout.PaymentMethodPOSCard {
			fieldErrors["paymentMethod"] = []string{"paymentMethod must be POS_CASH or POS_CARD"}
		}
		if len(body.Items) == 0 {
			fieldErrors["items"] = []string{"items must contain at least one item"}
		}
		if body.CashReceivedVND < 0 {
			fieldErrors["cashReceivedVnd"] = []string{"cashReceivedVnd must not be negative"}
		}
		if len(fieldErrors) > 0 {
			response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", fieldErrors)
			return
		}

		seen := make(map[string]struct{}, len(body.Items))
		for _, it := range body.Items {
			sku := strings.TrimSpace(it.SkuID)
			if sku == "" || it.Quantity < 1 {
				response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "Each item requires a skuId and quantity >= 1", nil)
				return
			}
			if _, dup := seen[sku]; dup {
				response.Error(w, r, http.StatusBadRequest, "DUPLICATE_SKU", "Duplicate skuId in POS sale items: "+sku, nil)
				return
			}
			seen[sku] = struct{}{}
		}

		claims, _ := auth.ClaimsFrom(r.Context())
		if !canAccessPOSWarehouse(claims, warehouseID) {
			writeWarehouseScopeDenied(w, r)
			return
		}

		receipt, replayed, err := svc.CreatePOSSale(r.Context(), checkout.POSSaleInput{
			CashierID:       claims.UserID,
			IdempotencyKey:  key,
			WarehouseID:     warehouseID,
			PaymentMethod:   paymentMethod,
			CustomerNote:    body.CustomerNote,
			CashReceivedVND: body.CashReceivedVND,
			Items:           body.Items,
		})
		if err != nil {
			writePOSError(w, r, err)
			return
		}

		status, message := http.StatusCreated, "POS sale recorded"
		if replayed {
			status, message = http.StatusOK, "POS sale already recorded with this Idempotency-Key"
		}
		response.JSON(w, status, message, receipt, nil)
	}
}

func listPOSSales(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := defaultPageSize
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				badParam(w, r, "limit", "limit must be a positive integer")
				return
			}
			if parsed > maxPageSize {
				parsed = maxPageSize
			}
			limit = parsed
		}

		claims, _ := auth.ClaimsFrom(r.Context())
		scope, limited := posWarehouseFilter(claims)
		if wh := strings.TrimSpace(r.URL.Query().Get("warehouseId")); wh != "" {
			if !canAccessPOSWarehouse(claims, wh) {
				writeWarehouseScopeDenied(w, r)
				return
			}
			scope, limited = []string{wh}, true
		}

		if limited && len(scope) == 0 {
			response.JSON(w, http.StatusOK, "POS sales", []checkout.POSSaleSummary{}, pageMeta(0, 1, limit))
			return
		}

		list, total, err := svc.ListPOSSales(r.Context(), scope, limit)
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "POS sales", list, pageMeta(total, 1, limit))
	}
}

func writePOSError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, orders.ErrIdempotencyConflict):
		response.Error(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "This Idempotency-Key was already used for a different POS sale", nil)
	case errors.Is(err, checkout.ErrDuplicateSKU):
		response.Error(w, r, http.StatusBadRequest, "DUPLICATE_SKU", "Duplicate skuId in POS sale items", nil)
	case errors.Is(err, checkout.ErrInsufficientCash):
		response.Error(w, r, http.StatusBadRequest, "INSUFFICIENT_CASH_TENDERED", "Cash tendered is less than the sale total", nil)
	case errors.Is(err, checkout.ErrInvalidPOSRequest):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", nil)
	case errors.Is(err, checkout.ErrInvalidSKU):
		response.Error(w, r, http.StatusUnprocessableEntity, "INVALID_SKU", "One or more SKUs are unknown or not orderable", nil)
	case errors.Is(err, checkout.ErrOutOfStock):
		response.Error(w, r, http.StatusConflict, "INSUFFICIENT_STOCK", "Insufficient stock in the selected warehouse", nil)
	case errors.Is(err, checkout.ErrInventoryUnavailable):
		response.Error(w, r, http.StatusBadGateway, "INVENTORY_UNAVAILABLE", "Inventory service failed to complete stock deduction", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[pos] POS sale failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
