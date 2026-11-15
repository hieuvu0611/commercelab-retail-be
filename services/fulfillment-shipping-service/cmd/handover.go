package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/inventory"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

// aggregateShipment is the aggregate type of shipment events.
const aggregateShipment = "Shipment"

// handoverParcel is the courier scan (SC02): inventory takes the stock out of the shipment's warehouse once, and
// then the shipment is recorded as handed over. A repeated request returns the stored result and never calls
// inventory again.
func handoverParcel(d Deps) func(http.ResponseWriter, *http.Request, shipment.Shipment) {
	return func(w http.ResponseWriter, r *http.Request, s shipment.Shipment) {
		var req struct {
			Version *int64 `json:"version"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		if s.Status == shipment.StatusHandedOver {
			response.JSON(w, http.StatusOK, "Shipment handed over", viewOf(s), nil)
			return
		}
		if !versionGuard(w, r, s, req.Version) {
			return
		}
		next, err := s.Handover(d.now())
		if err != nil {
			writeStepError(w, r, err)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err := d.Stock.Handover(r.Context(), bearer, s.OrderID, s.WarehouseID); err != nil {
			writeInventoryError(w, r, err)
			return
		}
		items := make([]map[string]any, len(next.Items))
		for i, it := range next.Items {
			items[i] = map[string]any{"skuId": it.SkuID, "quantity": it.PickedQuantity}
		}
		ev := outbox.NewEvent(events.EventShipmentHandedOver, aggregateShipment, next.ShipmentID, d.now(),
			map[string]any{"shipmentId": next.ShipmentID, "orderId": next.OrderID, "warehouseId": next.WarehouseID, "items": items})
		if err := d.Shipments.Save(r.Context(), next, s.Version, ev); err != nil {
			writeStoreError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Shipment handed over", viewOf(next), nil)
	}
}

func writeInventoryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, inventory.ErrForbidden):
		response.Error(w, r, http.StatusForbidden, "FORBIDDEN_WAREHOUSE_SCOPE", "Warehouse access is forbidden", nil)
	case errors.Is(err, inventory.ErrRejected):
		response.Error(w, r, http.StatusConflict, "INVENTORY_HANDOVER_REJECTED", "Inventory could not hand this order over", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		response.Error(w, r, http.StatusBadGateway, "INVENTORY_UNAVAILABLE", "Inventory did not answer; the shipment was not handed over", nil)
	}
}
