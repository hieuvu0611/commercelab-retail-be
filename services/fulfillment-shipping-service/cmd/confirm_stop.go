package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/inventory"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

// confirmStop is the warehouse's confirmation that it stopped a parcel a cancellation asked it to stop (SC06).
// Inventory releases the committed stock first: that step and the handover exclude each other on the
// reservation, so when the handover got there first this answers 409 HANDOVER_ALREADY_DONE and nothing changes;
// when the release wins, a later handover is refused by inventory before it takes effect. Only then the
// shipment is stored as STOPPED_FOR_CANCEL (version compare-and-set). Repeating a confirmation changes nothing.
func confirmStop(d Deps) func(http.ResponseWriter, *http.Request, shipment.Shipment) {
	return func(w http.ResponseWriter, r *http.Request, s shipment.Shipment) {
		var req struct {
			Version *int64 `json:"version"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		if s.Status == shipment.StatusStoppedForCancel {
			response.JSON(w, http.StatusOK, "Shipment stopped", viewOf(s), nil)
			return
		}
		if !versionGuard(w, r, s, req.Version) {
			return
		}
		if s.TotalParcels > 1 {
			if _, err := s.ConfirmStop(d.now()); err != nil {
				writeStepError(w, r, err)
				return
			}
			next, outcome, err := stopSplitOrder(r, d, s.OrderID, s.ShipmentID)
			switch {
			case err != nil:
				writeStoreError(w, r, err)
			case outcome == shipment.StopTooLate:
				writeStopStockError(w, r, inventory.ErrStockLeft)
			case outcome == shipment.StopRequested:
				response.JSON(w, http.StatusOK, "Stop recorded; waiting for remaining parcels", viewOf(next), nil)
			default:
				response.JSON(w, http.StatusOK, "Shipment stopped", viewOf(next), nil)
			}
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		for attempt := 0; attempt < maxStopRetries; attempt++ {
			next, err := s.ConfirmStop(d.now())
			if err != nil {
				writeStepError(w, r, err)
				return
			}
			if err := d.Stock.ReleaseCommitted(r.Context(), bearer, s.OrderID, s.WarehouseID); err != nil {
				writeStopStockError(w, r, err)
				return
			}
			err = d.Shipments.Save(r.Context(), next, s.Version, stoppedEvent(d, next))
			if err == nil {
				d.record(r, next, timeline.TypeStopped, "")
				response.JSON(w, http.StatusOK, "Shipment stopped", viewOf(next), nil)
				return
			}
			// The release is idempotent, so after a lost version race (a scan, a pack) the parcel is read again.
			if !errors.Is(err, shipment.ErrStaleVersion) || req.Version != nil {
				writeStoreError(w, r, err)
				return
			}
			if s, err = d.Shipments.Get(r.Context(), s.ShipmentID); err != nil {
				writeStoreError(w, r, err)
				return
			}
		}
		writeStale(w, r)
	}
}

func writeStopStockError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, inventory.ErrStockLeft):
		response.Error(w, r, http.StatusConflict, "HANDOVER_ALREADY_DONE", "The parcel was already handed over, so it cannot be stopped", nil)
	case errors.Is(err, inventory.ErrForbidden):
		response.Error(w, r, http.StatusForbidden, "FORBIDDEN_WAREHOUSE_SCOPE", "Warehouse access is forbidden", nil)
	case errors.Is(err, inventory.ErrRejected):
		response.Error(w, r, http.StatusConflict, "INVENTORY_RELEASE_REJECTED", "Inventory could not release this order's stock", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		response.Error(w, r, http.StatusBadGateway, "INVENTORY_UNAVAILABLE", "Inventory did not answer; the shipment was not stopped", nil)
	}
}
