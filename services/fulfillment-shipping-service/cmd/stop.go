package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/inventory"
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

type stopParcel struct {
	ParcelSeq    int             `json:"parcelSeq"`
	WarehouseID  string          `json:"warehouseId"`
	CodAmountVND int64           `json:"codAmountVnd"`
	Items        []shipment.Item `json:"items"`
}

type orderStockReleaser interface {
	ReleaseOrderCommitted(context.Context, string) error
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

func stopRequestedEvent(d Deps, s shipment.Shipment) events.DomainEvent {
	return outbox.NewEvent(events.EventShipmentStopRequested, aggregateShipment, s.ShipmentID, d.now(), map[string]any{
		"shipmentId": s.ShipmentID, "orderId": s.OrderID, "warehouseId": s.WarehouseID, "requestedAt": s.StopRequestedAt.UTC().Format(time.RFC3339),
	})
}

// stopForCancel stops the parcel of an order that was cancelled: STOPPED when nothing was picked (the parcel can
// never be handed over any more), STOP_REQUESTED while it is being picked or packed (the request is stored and
// announced once; the warehouse confirms it or hands the parcel over), TOO_LATE once it left. When the
// parcel does not exist yet (the confirmed event is still on its way) a stopped parcel is created from the
// order's details, so the late event cannot start a pick for a cancelled order. Repeating it changes nothing.
func stopForCancel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID        string       `json:"userId"`
			WarehouseID   string       `json:"warehouseId"`
			Items         []stopLine   `json:"items"`
			Parcels       []stopParcel `json:"parcels"`
			PaymentMethod string       `json:"paymentMethod"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		orderID := r.PathValue("orderId")
		list, err := loadOrderShipments(r, d, orderID)
		if err != nil && !errors.Is(err, shipment.ErrNotFound) {
			writeStoreError(w, r, err)
			return
		}
		if len(req.Parcels) > 1 && len(list) < len(req.Parcels) {
			if err := ensureSplitParcels(r, d, orderID, req.UserID, req.PaymentMethod, req.Parcels); err != nil {
				writeStopError(w, r, err)
				return
			}
			list, err = loadOrderShipments(r, d, orderID)
			if err != nil {
				writeStoreError(w, r, err)
				return
			}
		}
		if len(list) > 1 || (len(list) == 1 && list[0].TotalParcels > 1) {
			s, outcome, err := stopSplitOrder(r, d, orderID, "")
			if err != nil {
				writeStoreError(w, r, err)
				return
			}
			writeStop(w, r, outcome, s)
			return
		}
		if err != nil && req.WarehouseID == "MULTI" {
			writeStopError(w, r, errBadStopDetails)
			return
		}
		if errors.Is(err, shipment.ErrNotFound) {
			s, err := createStopped(r, d, orderID, req.UserID, req.WarehouseID, req.Items)
			if err != nil {
				writeStopError(w, r, err)
				return
			}
			list = []shipment.Shipment{s}
		} else if err != nil {
			writeStoreError(w, r, err)
			return
		}

		stopped, outcome, ok := stopOneParcel(w, r, d, list[0])
		if ok {
			writeStop(w, r, outcome, stopped)
		}
	}
}

func ensureSplitParcels(r *http.Request, d Deps, orderID, userID, method string, parcels []stopParcel) error {
	if len(parcels) != 2 {
		return errBadStopDetails
	}
	pending := make([]shipment.Shipment, 0, len(parcels))
	for _, parcel := range parcels {
		s, err := shipment.NewParcel(orderID, userID, parcel.WarehouseID, method, parcel.CodAmountVND,
			parcel.ParcelSeq, len(parcels), parcel.Items, d.now())
		if err != nil {
			return errBadStopDetails
		}
		pending = append(pending, s)
	}
	if pending[0].ShipmentID == pending[1].ShipmentID || pending[0].WarehouseID == pending[1].WarehouseID {
		return errBadStopDetails
	}
	for _, s := range pending {
		createdEvent := outbox.NewEvent(events.EventShipmentCreated, aggregateShipment, s.ShipmentID, d.now(),
			map[string]any{"shipmentId": s.ShipmentID, "orderId": s.OrderID, "warehouseId": s.WarehouseID})
		stored, created, err := d.Shipments.Create(r.Context(), s, "stop-intake-"+s.ShipmentID, createdEvent)
		if err != nil {
			return err
		}
		if created {
			d.record(r, stored, timeline.TypeCreated, "")
		}
	}
	return nil
}

func stopSplitOrder(r *http.Request, d Deps, orderID, confirmedID string) (shipment.Shipment, shipment.StopOutcome, error) {
	store, ok := d.Shipments.(shipment.SplitStopStore)
	stock, canRelease := d.Stock.(orderStockReleaser)
	if !ok || !canRelease {
		return shipment.Shipment{}, "", errors.New("atomic split-order stop is not configured")
	}
	for attempt := 0; attempt < maxStopRetries; attempt++ {
		list, err := loadOrderShipments(r, d, orderID)
		if err != nil {
			return shipment.Shipment{}, "", err
		}
		result, outcome, err := updateSplitStop(r, d, store, stock, list, confirmedID)
		if !errors.Is(err, shipment.ErrStaleVersion) {
			return result, outcome, err
		}
	}
	return shipment.Shipment{}, "", shipment.ErrStaleVersion
}

func updateSplitStop(r *http.Request, d Deps, store shipment.SplitStopStore, stock orderStockReleaser, list []shipment.Shipment, confirmedID string) (shipment.Shipment, shipment.StopOutcome, error) {
	now := d.now()
	result := list[0]
	allReady := len(list) == result.TotalParcels
	allStopped := allReady
	tooLate := false
	needsPreparation := false
	for _, s := range list {
		if s.ShipmentID == confirmedID {
			result = s
		}
		_, outcome := s.StopForCancel(now)
		if outcome == shipment.StopTooLate {
			result, tooLate = s, true
			break
		}
		allStopped = allStopped && outcome == shipment.StopStopped && s.Status == shipment.StatusStoppedForCancel
		next := s.PrepareSplitStop(now, s.ShipmentID == confirmedID)
		needsPreparation = needsPreparation || (s.Status != shipment.StatusStoppedForCancel && next.Version != s.Version)
		allReady = allReady && (s.Status == shipment.StatusStoppedForCancel || next.StopConfirmedAt != nil)
	}
	if allStopped && !tooLate {
		return result, shipment.StopStopped, nil
	}
	persistReadiness := allReady && !tooLate && needsPreparation
	if allReady && !tooLate && !persistReadiness {
		err := stock.ReleaseOrderCommitted(r.Context(), result.OrderID)
		if errors.Is(err, inventory.ErrStockLeft) {
			tooLate = true
		} else if err != nil {
			return result, "", fmt.Errorf("release split order stock: %w", err)
		}
	}

	outcome := shipment.StopRequested
	if tooLate {
		outcome = shipment.StopTooLate
	} else if allReady && !persistReadiness {
		outcome = shipment.StopStopped
	}
	updates := make([]shipment.StopUpdate, 0, len(list))
	for _, s := range list {
		if s.Status == shipment.StatusStoppedForCancel {
			continue
		}
		var update shipment.StopUpdate
		update.ExpectedVersion = s.Version
		if tooLate {
			update.Shipment = s.ClearSplitStop(now)
		} else {
			next := s.PrepareSplitStop(now, s.ShipmentID == confirmedID)
			if outcome == shipment.StopStopped {
				stopped, err := next.ConfirmStop(now)
				if err != nil {
					return result, outcome, err
				}
				next = stopped
				update.Events = []events.DomainEvent{stoppedEvent(d, next)}
			} else if s.StopRequestedAt == nil && next.StopConfirmedAt == nil {
				update.Events = []events.DomainEvent{stopRequestedEvent(d, next)}
			}
			update.Shipment = next
		}
		if update.Shipment.Version != s.Version {
			updates = append(updates, update)
			if !tooLate && (confirmedID == "" || confirmedID == s.ShipmentID) {
				result = update.Shipment
			}
		}
	}
	if len(updates) == 0 {
		return result, outcome, nil
	}
	if err := store.SaveStopUpdates(r.Context(), updates); err != nil {
		return result, outcome, err
	}
	for _, update := range updates {
		for _, event := range update.Events {
			if event.EventType == events.EventShipmentStopped {
				d.record(r, update.Shipment, timeline.TypeStopped, "")
			} else {
				d.record(r, update.Shipment, timeline.TypeStopRequested, "")
			}
		}
	}
	if persistReadiness {
		// Recheck the persisted readiness before inventory arbitrates against a concurrent handover.
		return result, outcome, shipment.ErrStaleVersion
	}
	return result, outcome, nil
}

func loadOrderShipments(r *http.Request, d Deps, orderID string) ([]shipment.Shipment, error) {
	if mp, ok := d.Shipments.(shipment.MultiParcelStore); ok {
		return mp.ListByOrder(r.Context(), orderID)
	}
	s, err := d.Shipments.GetByOrder(r.Context(), orderID)
	if err != nil {
		return nil, err
	}
	return []shipment.Shipment{s}, nil
}

func stopOneParcel(w http.ResponseWriter, r *http.Request, d Deps, s shipment.Shipment) (shipment.Shipment, shipment.StopOutcome, bool) {
	for attempt := 0; attempt < maxStopRetries; attempt++ {
		next, outcome := s.StopForCancel(d.now())
		if next.Version == s.Version {
			return s, outcome, true
		}
		ev, entry := stoppedEvent(d, next), timeline.TypeStopped
		if outcome == shipment.StopRequested {
			ev, entry = stopRequestedEvent(d, next), timeline.TypeStopRequested
		}
		err := d.Shipments.Save(r.Context(), next, s.Version, ev)
		if err == nil {
			d.record(r, next, entry, "")
			return next, outcome, true
		}
		if !errors.Is(err, shipment.ErrStaleVersion) {
			writeStoreError(w, r, err)
			return shipment.Shipment{}, "", false
		}
		var reloadErr error
		if s, reloadErr = d.Shipments.Get(r.Context(), s.ShipmentID); reloadErr != nil {
			writeStoreError(w, r, reloadErr)
			return shipment.Shipment{}, "", false
		}
	}
	writeStale(w, r)
	return shipment.Shipment{}, "", false
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
