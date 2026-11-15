package main

import (
	"net/http"
	"strconv"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

// reportShortage is the pick shortfall (B04): the parcel is blocked, an exception case opens for Support and
// Operations, and the SKU counts as blocked in that warehouse while the case is open. Dispatch is never reported.
func reportShortage(d Deps) func(http.ResponseWriter, *http.Request, shipment.Shipment) {
	return func(w http.ResponseWriter, r *http.Request, s shipment.Shipment) {
		var req struct {
			SkuID    string `json:"skuId"`
			Quantity int64  `json:"quantity"`
			Reason   string `json:"reason"`
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
		next, err := s.ReportShortage(req.SkuID, req.Quantity, req.Reason, d.now())
		if err != nil {
			writeStepError(w, r, err)
			return
		}
		c := shipment.NewCase(next, shipment.CasePickShortage, req.SkuID, req.Quantity, next.FailureReason, d.now())
		opts := shipment.SaveOptions{Case: &c, Events: nil}
		opts.Events = append(opts.Events, caseOpenedEvent(c, d))
		if err := d.Shipments.SaveWith(r.Context(), next, s.Version, opts); err != nil {
			writeStoreError(w, r, err)
			return
		}
		d.record(r, next, timeline.TypePickBlocked, next.FailureReason)
		response.JSON(w, http.StatusOK, "Shortage reported", viewOf(next), nil)
	}
}

// listCases serves the exception cases. Warehouse staff see their warehouses; delivery coordinators track
// delivery exceptions across all warehouses.
func listCases(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		q := r.URL.Query()
		f := shipment.CaseFilter{Status: q.Get("status"), Type: q.Get("type"), Warehouses: claims.WarehouseScopes, AllWarehouses: claims.HasRole("DELIVERY_COORD")}
		for _, scope := range claims.WarehouseScopes {
			f.AllWarehouses = f.AllWarehouses || scope == auth.AllWarehouses
		}
		if f.Status != "" && f.Status != shipment.CaseOpen {
			badParam(w, r, "status", "status must be OPEN")
			return
		}
		switch f.Type {
		case "", shipment.CasePickShortage, shipment.CaseDeliveryFailed, shipment.CasePartialDelivery:
		default:
			badParam(w, r, "type", "type must be PICK_SHORTAGE, DELIVERY_FAILED or PARTIAL_DELIVERY")
			return
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
		list, total, err := d.Shipments.ListCases(r.Context(), f)
		if err != nil {
			writeStoreError(w, r, err)
			return
		}
		if list == nil {
			list = []shipment.Case{}
		}
		response.JSON(w, http.StatusOK, "Cases", list,
			&response.Metadata{Total: int64(total), Page: page, PageCount: (total + limit - 1) / limit, Limit: limit})
	}
}
