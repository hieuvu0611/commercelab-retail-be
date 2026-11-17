package main

import (
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/inventory-warehouse-service/internal/repository"
)

const maxAvailabilitySkus = 100

func parseSkuIdsParam(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var skuIDs []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(r.URL.Query().Get("skuIds"), ",") {
		sku := strings.TrimSpace(raw)
		if sku == "" || seen[sku] {
			continue
		}
		if len(sku) > maxSkuIDLength {
			validationError(w, r, "skuIds contains an invalid SKU id")
			return nil, false
		}
		seen[sku] = true
		skuIDs = append(skuIDs, sku)
	}
	if len(skuIDs) == 0 || len(skuIDs) > maxAvailabilitySkus {
		validationError(w, r, "skuIds must hold between 1 and 100 SKU ids")
		return nil, false
	}
	return skuIDs, true
}

// availabilityHandler answers the public, state-only availability batch (D-12): never counts.
// A SKU without a stock record is OUT. Passing warehouse=ALL aggregates across all warehouses.
func availabilityHandler(repo *repository.InventoryRepository, lowThreshold int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		skuIDs, ok := parseSkuIdsParam(w, r)
		if !ok {
			return
		}
		warehouseID := strings.TrimSpace(r.URL.Query().Get("warehouse"))
		if warehouseID == "" {
			warehouseID = domain.DefaultWarehouseID
		}
		available, err := repo.AvailableBySku(r.Context(), warehouseID, skuIDs)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Availability could not be read", nil)
			return
		}
		type item struct {
			SkuID string `json:"skuId"`
			State string `json:"state"`
		}
		items := make([]item, 0, len(skuIDs))
		for _, sku := range skuIDs {
			items = append(items, item{SkuID: sku, State: domain.AvailabilityState(available[sku], lowThreshold)})
		}
		response.JSON(w, http.StatusOK, "Availability retrieved", map[string]any{"warehouseId": warehouseID, "items": items}, nil)
	}
}

// availabilityBreakdownHandler answers internal checkout allocation queries with per-warehouse available counts.
func availabilityBreakdownHandler(repo *repository.InventoryRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		skuIDs, ok := parseSkuIdsParam(w, r)
		if !ok {
			return
		}
		breakdown, err := repo.AvailableBreakdown(r.Context(), skuIDs)
		if dependency.IsUnavailable(err) {
			dependency.WriteUnavailable(w, r, err)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Availability breakdown could not be read", nil)
			return
		}
		response.JSON(w, http.StatusOK, "Availability breakdown retrieved", map[string]any{
			"warehouses": breakdown,
		}, nil)
	}
}
