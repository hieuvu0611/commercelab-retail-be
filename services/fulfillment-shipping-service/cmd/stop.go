package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

// maxStopRetries bounds how often a stop is retried after losing a version race to another change.
const maxStopRetries = 5

// registerInternalRoutes serves what only other services may call. The order service asks here whether the
// parcel of a cancelled order can still be stopped (B05, SC06).
func registerInternalRoutes(mux *http.ServeMux, d Deps) {
	mux.Handle("POST /api/v1/shipments/internal/orders/{orderId}/stop", internalauth.Require(http.HandlerFunc(stopForCancel(d))))
}

type stopLine struct {
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
}

type stopView struct {
	Result         string `json:"result"`
	ShipmentID     string `json:"shipmentId"`
	ShipmentStatus string `json:"shipmentStatus"`
}

func stoppedEvent(d Deps, s shipment.Shipment) events.DomainEvent {
	return outbox.NewEvent(events.EventShipmentStopped, aggregateShipment, s.ShipmentID, d.now(),
		map[string]any{"shipmentId": s.ShipmentID, "orderId": s.OrderID, "warehouseId": s.WarehouseID})
}

// stopForCancel stops the parcel of an order that was cancelled: STOPPED when nothing was picked (the parcel can
// never be handed over any more), IN_PROGRESS while it is being picked or packed, TOO_LATE once it left. When the
// parcel does not exist yet (the confirmed event is still on its way) a stopped parcel is created from the
// order's details, so the late event cannot start a pick for a cancelled order. Repeating it changes nothing.
func stopForCancel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID      string     `json:"userId"`
			WarehouseID string     `json:"warehouseId"`
			Items       []stopLine `json:"items"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		orderID := r.PathValue("orderId")
		s, err := d.Shipments.GetByOrder(r.Context(), orderID)
		if errors.Is(err, shipment.ErrNotFound) {
			s, err = createStopped(r, d, orderID, req.UserID, req.WarehouseID, req.Items)
			if err != nil {
				writeStopError(w, r, err)
				return
			}
		} else if err != nil {
			writeStoreError(w, r, err)
			return
		}
		for attempt := 0; attempt < maxStopRetries; attempt++ {
			next, outcome := s.StopForCancel(d.now())
			if outcome != shipment.StopStopped || next.Version == s.Version {
				writeStop(w, r, outcome, s)
				return
			}
			err := d.Shipments.Save(r.Context(), next, s.Version, stoppedEvent(d, next))
			if err == nil {
				d.record(r, next, timeline.TypeStopped, "")
				writeStop(w, r, outcome, next)
				return
			}
			if !errors.Is(err, shipment.ErrStaleVersion) {
				writeStoreError(w, r, err)
				return
			}
			if s, err = d.Shipments.GetByOrder(r.Context(), orderID); err != nil {
				writeStoreError(w, r, err)
				return
			}
		}
		writeStale(w, r)
	}
}

// errBadStopDetails marks a stop request for an unknown parcel that lacks the order's details.
var errBadStopDetails = errors.New("the order's details are required to stop a parcel that does not exist yet")

// createStopped stores the stopped placeholder; when the real parcel appeared in the meantime it is returned.
func createStopped(r *http.Request, d Deps, orderID, userID, warehouseID string, lines []stopLine) (shipment.Shipment, error) {
	items := make([]shipment.Item, len(lines))
	for i, l := range lines {
		items[i] = shipment.Item{SkuID: strings.TrimSpace(l.SkuID), Quantity: l.Quantity}
	}
	s, err := shipment.New(orderID, strings.TrimSpace(userID), strings.TrimSpace(warehouseID), items, d.now())
	if err != nil {
		return shipment.Shipment{}, errBadStopDetails
	}
	s.Status = shipment.StatusStoppedForCancel
	stored, created, err := d.Shipments.Create(r.Context(), s, "stop-"+orderID, stoppedEvent(d, s))
	if err == nil && created {
		d.record(r, stored, timeline.TypeStopped, "")
	}
	return stored, err
}

func writeStopError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errBadStopDetails) {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields",
			map[string][]string{"items": {"userId, warehouseId and at least one item with a SKU and a positive quantity are required"}})
		return
	}
	writeStoreError(w, r, err)
}

func writeStop(w http.ResponseWriter, r *http.Request, outcome shipment.StopOutcome, s shipment.Shipment) {
	response.JSON(w, http.StatusOK, "Stop request evaluated", stopView{Result: string(outcome), ShipmentID: s.ShipmentID, ShipmentStatus: string(s.Status)}, nil)
}
