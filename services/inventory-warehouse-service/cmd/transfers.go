package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

func registerTransferRoutes(mux *http.ServeMux, repo *repository.InventoryRepository) {
	staff := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(h))
	}

	mux.Handle("POST /api/v1/admin/inventory/transfers", staff(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			FromWarehouseID string               `json:"fromWarehouseId"`
			ToWarehouseID   string               `json:"toWarehouseId"`
			Notes           string               `json:"notes"`
			Items           []domain.ReserveLine `json:"items"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		req.FromWarehouseID = strings.TrimSpace(req.FromWarehouseID)
		req.ToWarehouseID = strings.TrimSpace(req.ToWarehouseID)
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, req.FromWarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		trf, err := repo.CreateTransfer(r.Context(), req.FromWarehouseID, req.ToWarehouseID, claims.UserID, strings.TrimSpace(req.Notes), req.Items)
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
		case errors.Is(err, domain.ErrTransferInvalidInput), errors.Is(err, domain.ErrInvalidLines):
			validationError(w, r, err.Error())
		case err != nil:
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		default:
			response.JSON(w, http.StatusCreated, "Warehouse transfer created", trf, nil)
		}
	}))

	mux.Handle("GET /api/v1/admin/inventory/transfers", staff(func(w http.ResponseWriter, r *http.Request) {
		wh := strings.TrimSpace(r.URL.Query().Get("warehouse"))
		claims, _ := auth.ClaimsFrom(r.Context())
		if wh == "ALL" {
			wh = ""
		}
		warehouses := procurementWarehouseFilter(claims, wh)
		if (wh != "" && !auth.CanAccessWarehouse(claims, wh)) || (warehouses != nil && len(warehouses) == 0) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		list, err := repo.ListTransfers(r.Context(), warehouses)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		response.JSON(w, http.StatusOK, "Warehouse transfers retrieved", list, nil)
	}))

	mux.Handle("GET /api/v1/admin/inventory/transfers/{transferId}", staff(func(w http.ResponseWriter, r *http.Request) {
		trf, err := repo.GetTransfer(r.Context(), r.PathValue("transferId"))
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if errors.Is(err, domain.ErrTransferNotFound) {
			response.Error(w, r, http.StatusNotFound, "TRANSFER_NOT_FOUND", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, trf.FromWarehouseID) && !auth.CanAccessWarehouse(claims, trf.ToWarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		response.JSON(w, http.StatusOK, "Warehouse transfer retrieved", trf, nil)
	}))

	mux.Handle("POST /api/v1/admin/inventory/transfers/{transferId}/dispatch", staff(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("transferId")
		trf, err := repo.GetTransfer(r.Context(), id)
		if errors.Is(err, domain.ErrTransferNotFound) {
			response.Error(w, r, http.StatusNotFound, "TRANSFER_NOT_FOUND", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, trf.FromWarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		updated, err := repo.DispatchTransfer(r.Context(), id, claims.UserID)
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
		case errors.Is(err, domain.ErrInsufficientStock):
			response.Error(w, r, http.StatusConflict, "INSUFFICIENT_STOCK", err.Error(), nil)
		case errors.Is(err, domain.ErrTransferInvalidState), errors.Is(err, domain.ErrTransferInsufficientInTransit):
			response.Error(w, r, http.StatusConflict, "TRANSFER_INVALID_STATE", err.Error(), nil)
		case err != nil:
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		default:
			response.JSON(w, http.StatusOK, "Warehouse transfer dispatched (in transit)", updated, nil)
		}
	}))

	mux.Handle("POST /api/v1/admin/inventory/transfers/{transferId}/receive", staff(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("transferId")
		var req struct {
			Lines []domain.TransferReceiveLine `json:"lines"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		trf, err := repo.GetTransfer(r.Context(), id)
		if errors.Is(err, domain.ErrTransferNotFound) {
			response.Error(w, r, http.StatusNotFound, "TRANSFER_NOT_FOUND", err.Error(), nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, trf.ToWarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		updated, hasDiscrepancy, err := repo.ReceiveTransfer(r.Context(), id, claims.UserID, req.Lines)
		switch {
		case dependency.IsUnavailable(err):
			dependency.WriteUnavailable(w, r, err)
		case errors.Is(err, domain.ErrTransferReceiveMath), errors.Is(err, domain.ErrTransferInvalidInput):
			validationError(w, r, err.Error())
		case errors.Is(err, domain.ErrTransferInvalidState), errors.Is(err, domain.ErrTransferInsufficientInTransit):
			response.Error(w, r, http.StatusConflict, "TRANSFER_INVALID_STATE", err.Error(), nil)
		case err != nil:
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), nil)
		default:
			if hasDiscrepancy && updated.ExceptionCaseID == "" {
				if caseID := openTransferExceptionCase(r.Context(), updated); caseID != "" {
					_ = repo.SetTransferExceptionCase(r.Context(), updated.TransferID, caseID)
					updated.ExceptionCaseID = caseID
				}
			}
			response.JSON(w, http.StatusOK, "Warehouse transfer received", updated, nil)
		}
	}))
}

func openTransferExceptionCase(ctx context.Context, trf *domain.WarehouseTransfer) string {
	base := strings.TrimRight(os.Getenv("BACKOFFICE_SERVICE_URL"), "/")
	if base == "" {
		return ""
	}
	var parts []string
	for _, it := range trf.Items {
		if it.MissingQty > 0 || it.DamagedQty > 0 {
			parts = append(parts, fmt.Sprintf("%s (good=%d, damaged=%d, missing=%d / %d)",
				it.SkuID, it.ReceivedGoodQty, it.DamagedQty, it.MissingQty, it.Quantity))
		}
	}
	payload := map[string]any{
		"referenceId": trf.TransferID,
		"category":    "TRANSFER_DISCREPANCY",
		"summary":     fmt.Sprintf("SC09 transfer discrepancy %s (%s -> %s): %s", trf.TransferID, trf.FromWarehouseID, trf.ToWarehouseID, strings.Join(parts, "; ")),
	}
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, base+"/api/v1/backoffice/exception-cases", bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := config.EnvOrDefault("INTERNAL_SERVICE_TOKEN", ""); tok != "" {
		req.Header.Set(internalauth.Header, tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ""
	}
	var body struct {
		Data struct {
			CaseID string `json:"caseId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return ""
	}
	return body.Data.CaseID
}
