package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// itemView is one line of the pick list.
type itemView struct {
	SkuID             string `json:"skuId"`
	Quantity          int64  `json:"quantity"`
	PickedQuantity    int64  `json:"pickedQuantity"`
	DeliveredQuantity int64  `json:"deliveredQuantity"`
}

// shipmentView is what staff see of a parcel.
type shipmentView struct {
	ShipmentID         string     `json:"shipmentId"`
	OrderID            string     `json:"orderId"`
	WarehouseID        string     `json:"warehouseId"`
	Status             string     `json:"status"`
	Version            int64      `json:"version"`
	Items              []itemView `json:"items"`
	HandedOverAt       *time.Time `json:"handedOverAt,omitempty"`
	StopRequestedAt    *time.Time `json:"stopRequestedAt,omitempty"`
	DeliveredAt        *time.Time `json:"deliveredAt,omitempty"`
	ReturnWindowEndsAt *time.Time `json:"returnWindowEndsAt,omitempty"`
	FailureReason      string     `json:"failureReason,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

func viewOf(s shipment.Shipment) shipmentView {
	v := shipmentView{ShipmentID: s.ShipmentID, OrderID: s.OrderID, WarehouseID: s.WarehouseID, Status: string(s.Status),
		Version: s.Version, HandedOverAt: s.HandedOverAt, StopRequestedAt: s.StopRequestedAt, DeliveredAt: s.DeliveredAt, ReturnWindowEndsAt: s.ReturnWindowEndsAt, FailureReason: s.FailureReason, Items: make([]itemView, len(s.Items)), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	for i, it := range s.Items {
		v.Items[i] = itemView{SkuID: it.SkuID, Quantity: it.Quantity, PickedQuantity: it.PickedQuantity, DeliveredQuantity: it.DeliveredQuantity}
	}
	return v
}

var knownStatuses = map[shipment.Status]bool{
	shipment.StatusPendingPick: true, shipment.StatusPicking: true, shipment.StatusPacked: true, shipment.StatusPickBlocked: true,
	shipment.StatusHandedOver: true, shipment.StatusInTransit: true, shipment.StatusDelivered: true,
	shipment.StatusDeliveryFailed: true, shipment.StatusPartiallyDelivered: true, shipment.StatusStoppedForCancel: true,
}

func badParam(w http.ResponseWriter, r *http.Request, name, msg string) {
	response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid parameters", map[string][]string{name: {msg}})
}

func badField(w http.ResponseWriter, r *http.Request, name, msg string) {
	response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request is invalid", map[string][]string{name: {msg}})
}

// registerStaffRoutes serves the warehouse screens: queue, pick list, scan and pack. Only warehouse staff and
// managers get in, and only for shipments of a warehouse their token covers.
func registerStaffRoutes(mux *http.ServeMux, d Deps) {
	staff := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR")(h))
	}
	mux.Handle("GET /api/v1/admin/shipments", staff(listShipments(d)))
	mux.Handle("GET /api/v1/admin/fulfillment-cases", auth.RequireAuth(auth.RequireRole("WAREHOUSE_STAFF", "WAREHOUSE_MGR", "DELIVERY_COORD")(listCases(d))))
	mux.Handle("GET /api/v1/admin/shipments/{shipmentId}", staff(withShipment(d, func(w http.ResponseWriter, r *http.Request, s shipment.Shipment) {
		response.JSON(w, http.StatusOK, "Shipment", viewOf(s), nil)
	})))
	mux.Handle("POST /api/v1/admin/shipments/{shipmentId}/scan", staff(withShipment(d, scanLine(d))))
	mux.Handle("POST /api/v1/admin/shipments/{shipmentId}/pack", staff(withShipment(d, packParcel(d))))
	mux.Handle("POST /api/v1/admin/shipments/{shipmentId}/report-shortage", staff(withShipment(d, reportShortage(d))))
	mux.Handle("POST /api/v1/admin/shipments/{shipmentId}/handover", staff(withShipment(d, handoverParcel(d))))
	mux.Handle("POST /api/v1/admin/shipments/{shipmentId}/confirm-stop", staff(withShipment(d, confirmStop(d))))
}

func listShipments(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		q := r.URL.Query()
		f := shipment.Filter{Status: shipment.Status(q.Get("status")), Warehouses: claims.WarehouseScopes}
		if f.Status != "" && !knownStatuses[f.Status] {
			badParam(w, r, "status", "status is not a shipment status")
			return
		}
		for _, scope := range claims.WarehouseScopes {
			f.AllWarehouses = f.AllWarehouses || scope == auth.AllWarehouses
		}
		page, limit := 1, defaultPageSize
		var err error
		if v := q.Get("page"); v != "" {
			if page, err = strconv.Atoi(v); err != nil || page < 1 {
				badParam(w, r, "page", "page must be a positive integer")
				return
			}
		}
		if v := q.Get("limit"); v != "" {
			if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxPageSize {
				badParam(w, r, "limit", "limit must be between 1 and 100")
				return
			}
		}
		f.Offset, f.Limit = (page-1)*limit, limit
		list, total, err := d.Shipments.List(r.Context(), f)
		if err != nil {
			writeStoreError(w, r, err)
			return
		}
		views := make([]shipmentView, len(list))
		for i, s := range list {
			views[i] = viewOf(s)
		}
		response.JSON(w, http.StatusOK, "Shipments", views,
			&response.Metadata{Total: int64(total), Page: page, PageCount: (total + limit - 1) / limit, Limit: limit})
	}
}

// withShipment loads the shipment of the path and refuses callers whose token does not cover its warehouse.
func withShipment(d Deps, next func(http.ResponseWriter, *http.Request, shipment.Shipment)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := d.Shipments.Get(r.Context(), r.PathValue("shipmentId"))
		if errors.Is(err, shipment.ErrNotFound) {
			response.Error(w, r, http.StatusNotFound, "SHIPMENT_NOT_FOUND", "Shipment not found", nil)
			return
		}
		if err != nil {
			writeStoreError(w, r, err)
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		if !auth.CanAccessWarehouse(claims, s.WarehouseID) {
			auth.ForbiddenWarehouse(w, r)
			return
		}
		next(w, r, s)
	}
}

// versionGuard answers 409 when the caller saw an older version than the stored one.
func versionGuard(w http.ResponseWriter, r *http.Request, s shipment.Shipment, seen *int64) bool {
	if seen != nil && *seen != s.Version {
		writeStale(w, r)
		return false
	}
	return true
}

func writeStale(w http.ResponseWriter, r *http.Request) {
	response.Error(w, r, http.StatusConflict, "STALE_VERSION", "The shipment was changed by someone else; reload it and try again", nil)
}

func scanLine(d Deps) func(http.ResponseWriter, *http.Request, shipment.Shipment) {
	return func(w http.ResponseWriter, r *http.Request, s shipment.Shipment) {
		var req struct {
			SkuID    string `json:"skuId"`
			Quantity int64  `json:"quantity"`
			Version  *int64 `json:"version"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		if req.SkuID == "" {
			badField(w, r, "skuId", "skuId is required")
			return
		}
		if !versionGuard(w, r, s, req.Version) {
			return
		}
		next, err := s.Scan(req.SkuID, req.Quantity, d.now())
		if !saveStep(w, r, d, s, next, err) {
			return
		}
		if s.Status == shipment.StatusPendingPick {
			d.record(r, next, timeline.TypePickingStarted, "")
		}
		response.JSON(w, http.StatusOK, "Line scanned", viewOf(next), nil)
	}
}

func packParcel(d Deps) func(http.ResponseWriter, *http.Request, shipment.Shipment) {
	return func(w http.ResponseWriter, r *http.Request, s shipment.Shipment) {
		var req struct {
			Version *int64 `json:"version"`
		}
		if !request.DecodeJSON(w, r, &req) {
			return
		}
		if !versionGuard(w, r, s, req.Version) {
			return
		}
		next, err := s.Pack(d.now())
		if !saveStep(w, r, d, s, next, err) {
			return
		}
		if next.Version != s.Version {
			d.record(r, next, timeline.TypePacked, "")
		}
		response.JSON(w, http.StatusOK, "Parcel packed", viewOf(next), nil)
	}
}

// saveStep stores next when the domain step succeeded and writes the error response otherwise. A step that
// changed nothing (a repeated pack) is not stored again.
func saveStep(w http.ResponseWriter, r *http.Request, d Deps, cur, next shipment.Shipment, stepErr error) bool {
	if stepErr != nil {
		writeStepError(w, r, stepErr)
		return false
	}
	if next.Version == cur.Version {
		return true
	}
	if err := d.Shipments.Save(r.Context(), next, cur.Version); err != nil {
		writeStoreError(w, r, err)
		return false
	}
	return true
}

func writeStepError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, shipment.ErrSkuNotOnPickList):
		response.Error(w, r, http.StatusUnprocessableEntity, "SKU_NOT_ON_PICK_LIST", err.Error(), nil)
	case errors.Is(err, shipment.ErrQuantityExceedsOrder):
		response.Error(w, r, http.StatusUnprocessableEntity, "QUANTITY_EXCEEDS_ORDER", err.Error(), nil)
	case errors.Is(err, shipment.ErrInvalidQuantity):
		badField(w, r, "quantity", "quantity must be a positive whole number")
	case errors.Is(err, shipment.ErrNotPickable):
		response.Error(w, r, http.StatusConflict, "SHIPMENT_NOT_PICKABLE", err.Error(), nil)
	case errors.Is(err, shipment.ErrInvalid):
		badField(w, r, "reason", "reason is required (at most 255 characters)")
	case errors.Is(err, shipment.ErrHandoverNotAllowed):
		response.Error(w, r, http.StatusConflict, "HANDOVER_NOT_ALLOWED", err.Error(), nil)
	case errors.Is(err, shipment.ErrNoStopRequest):
		response.Error(w, r, http.StatusConflict, "NO_STOP_REQUEST", err.Error(), nil)
	case errors.Is(err, shipment.ErrAlreadyHandedOver):
		response.Error(w, r, http.StatusConflict, "HANDOVER_ALREADY_DONE", err.Error(), nil)
	case errors.Is(err, shipment.ErrPickIncomplete):
		response.Error(w, r, http.StatusConflict, "PICK_INCOMPLETE", err.Error(), nil)
	default:
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "The request could not be processed", nil)
	}
}

func writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	case errors.Is(err, shipment.ErrStaleVersion):
		writeStale(w, r)
	case errors.Is(err, shipment.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "SHIPMENT_NOT_FOUND", "Shipment not found", nil)
	default:
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "The request could not be processed", nil)
	}
}
