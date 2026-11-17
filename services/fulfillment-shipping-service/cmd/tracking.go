package main

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

func (d Deps) record(r *http.Request, s shipment.Shipment, typ, detail string) {
	if d.Timeline == nil {
		return
	}
	if err := d.Timeline.Append(r.Context(), s.ShipmentID, s.OrderID, timeline.Entry{At: d.now(), Type: typ, Detail: detail}); err != nil {
		log.Printf("[fulfillment] timeline entry %s of shipment %s was not recorded: %v", typ, s.ShipmentID, err)
	}
}

var statusEntry = map[shipment.Status]string{
	shipment.StatusInTransit: timeline.TypeInTransit, shipment.StatusDelivered: timeline.TypeDelivered,
	shipment.StatusDeliveryFailed: timeline.TypeDeliveryFailed, shipment.StatusPartiallyDelivered: timeline.TypePartiallyDelivered,
}

type quantityView struct {
	SkuID      string `json:"skuId"`
	Ordered    int64  `json:"ordered"`
	Dispatched int64  `json:"dispatched"`
	Delivered  int64  `json:"delivered"`
	Returned   int64  `json:"returned"`
}

type entryView struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

type parcelTrackingView struct {
	ShipmentID         string         `json:"shipmentId"`
	ParcelIndex        int            `json:"parcelIndex"`
	TotalParcels       int            `json:"totalParcels"`
	Status             string         `json:"status"`
	CodAmountToCollect int64          `json:"codAmountToCollect,omitempty"`
	HandedOverAt       *time.Time     `json:"handedOverAt,omitempty"`
	DeliveredAt        *time.Time     `json:"deliveredAt,omitempty"`
	ReturnWindowEndsAt *time.Time     `json:"returnWindowEndsAt,omitempty"`
	Quantities         []quantityView `json:"quantities"`
	Timeline           []entryView    `json:"timeline"`
}

// trackingView is what a customer sees: no warehouse, version or staff detail.
type trackingView struct {
	OrderID            string               `json:"orderId"`
	ShipmentID         string               `json:"shipmentId"`
	PaymentMethod      string               `json:"paymentMethod,omitempty"`
	CodAmountToCollect int64                `json:"codAmountToCollect,omitempty"`
	Status             string               `json:"status"`
	HandedOverAt       *time.Time           `json:"handedOverAt,omitempty"`
	DeliveredAt        *time.Time           `json:"deliveredAt,omitempty"`
	ReturnWindowEndsAt *time.Time           `json:"returnWindowEndsAt,omitempty"`
	Quantities         []quantityView       `json:"quantities"`
	Timeline           []entryView          `json:"timeline"`
	Parcels            []parcelTrackingView `json:"parcels,omitempty"`
}

func customerMessage(e timeline.Entry) string {
	switch e.Type {
	case timeline.TypeCreated:
		return "The warehouse received your order"
	case timeline.TypePickingStarted:
		return "We started picking your items"
	case timeline.TypePickBlocked:
		return "We are checking the stock of an item in your order"
	case timeline.TypePacked:
		return "Your parcel is packed"
	case timeline.TypeHandedOver:
		return "Your parcel was handed to the carrier"
	case timeline.TypeInTransit:
		return "Your parcel is on its way"
	case timeline.TypeDelivered:
		return "Your parcel was delivered"
	case timeline.TypeDeliveryFailed:
		return "Delivery failed: " + e.Detail
	case timeline.TypePartiallyDelivered:
		return "Part of your order was delivered"
	case timeline.TypeStopRequested:
		return "We asked the warehouse to stop your parcel"
	case timeline.TypeStopped:
		return "Your order was cancelled before it left the warehouse"
	}
	return e.Type
}

// registerTrackingRoute serves the customer's tracking view of their own order.
func registerTrackingRoute(mux *http.ServeMux, d Deps) {
	mux.Handle("GET /api/v1/orders/{orderId}/tracking", auth.RequireAuth(auth.RequireRole("CUSTOMER")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		orderID := r.PathValue("orderId")
		var list []shipment.Shipment
		var err error
		if mp, ok := d.Shipments.(shipment.MultiParcelStore); ok {
			list, err = mp.ListByOrder(r.Context(), orderID)
		} else {
			var s shipment.Shipment
			s, err = d.Shipments.GetByOrder(r.Context(), orderID)
			if err == nil {
				list = []shipment.Shipment{s}
			}
		}
		if errors.Is(err, shipment.ErrNotFound) || len(list) == 0 || list[0].UserID != claims.UserID {
			response.Error(w, r, http.StatusNotFound, "TRACKING_NOT_FOUND", "No tracking is available for this order", nil)
			return
		}
		if err != nil {
			writeStoreError(w, r, err)
			return
		}

		if len(list) == 1 {
			s := list[0]
			entries := []timeline.Entry{}
			if d.Timeline != nil {
				if entries, err = d.Timeline.Get(r.Context(), s.ShipmentID); err != nil {
					writeStoreError(w, r, err)
					return
				}
			}
			response.JSON(w, http.StatusOK, "Tracking", trackingOf(s, entries), nil)
			return
		}

		view, err := multiTrackingOf(r, d, list)
		if err != nil {
			writeStoreError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Tracking", view, nil)
	}))))
}

func parcelTrackingOf(s shipment.Shipment, entries []timeline.Entry) parcelTrackingView {
	pIdx, pTotal := s.ParcelIndex, s.TotalParcels
	if pIdx < 1 {
		pIdx = 1
	}
	if pTotal < 1 {
		pTotal = 1
	}
	v := parcelTrackingView{
		ShipmentID:         s.ShipmentID,
		ParcelIndex:        pIdx,
		TotalParcels:       pTotal,
		Status:             string(s.Status),
		CodAmountToCollect: s.CodAmountToCollect,
		HandedOverAt:       s.HandedOverAt,
		DeliveredAt:        s.DeliveredAt,
		ReturnWindowEndsAt: s.ReturnWindowEndsAt,
		Quantities:         make([]quantityView, len(s.Items)),
		Timeline:           make([]entryView, len(entries)),
	}
	for i, it := range s.Items {
		q := quantityView{SkuID: it.SkuID, Ordered: it.Quantity, Delivered: it.DeliveredQuantity}
		if s.Dispatched() {
			q.Dispatched = it.Quantity
		}
		v.Quantities[i] = q
	}
	for i, e := range entries {
		v.Timeline[i] = entryView{At: e.At, Type: e.Type, Message: customerMessage(e)}
	}
	return v
}

func trackingOf(s shipment.Shipment, entries []timeline.Entry) trackingView {
	p := parcelTrackingOf(s, entries)
	return trackingView{
		OrderID: s.OrderID, ShipmentID: s.ShipmentID,
		PaymentMethod: s.PaymentMethod, CodAmountToCollect: s.CodAmountToCollect,
		Status: string(s.Status), HandedOverAt: s.HandedOverAt,
		DeliveredAt: s.DeliveredAt, ReturnWindowEndsAt: s.ReturnWindowEndsAt,
		Quantities: p.Quantities, Timeline: p.Timeline,
	}
}

func multiTrackingOf(r *http.Request, d Deps, list []shipment.Shipment) (trackingView, error) {
	parcels := make([]parcelTrackingView, 0, len(list))
	qtyBySku := map[string]quantityView{}
	var allTimeline []entryView
	var totalCOD int64
	deliveredCount := 0
	inTransitCount := 0
	for _, s := range list {
		entries := []timeline.Entry{}
		if d.Timeline != nil {
			var err error
			if entries, err = d.Timeline.Get(r.Context(), s.ShipmentID); err != nil {
				return trackingView{}, err
			}
		}
		pt := parcelTrackingOf(s, entries)
		parcels = append(parcels, pt)
		totalCOD += s.CodAmountToCollect
		if s.Status == shipment.StatusDelivered {
			deliveredCount++
		} else if s.Dispatched() {
			inTransitCount++
		}
		for _, q := range pt.Quantities {
			cur := qtyBySku[q.SkuID]
			cur.SkuID = q.SkuID
			cur.Ordered += q.Ordered
			cur.Dispatched += q.Dispatched
			cur.Delivered += q.Delivered
			qtyBySku[q.SkuID] = cur
		}
		allTimeline = append(allTimeline, pt.Timeline...)
	}
	sort.Slice(allTimeline, func(i, j int) bool { return allTimeline[i].At.Before(allTimeline[j].At) })
	var mergedQty []quantityView
	for _, q := range qtyBySku {
		mergedQty = append(mergedQty, q)
	}
	sort.Slice(mergedQty, func(i, j int) bool { return mergedQty[i].SkuID < mergedQty[j].SkuID })

	aggStatus := string(list[0].Status)
	if deliveredCount == len(list) {
		aggStatus = string(shipment.StatusDelivered)
	} else if deliveredCount > 0 {
		aggStatus = string(shipment.StatusPartiallyDelivered)
	} else if inTransitCount > 0 {
		aggStatus = string(shipment.StatusInTransit)
	}

	return trackingView{
		OrderID:            list[0].OrderID,
		ShipmentID:         list[0].ShipmentID,
		PaymentMethod:      list[0].PaymentMethod,
		CodAmountToCollect: totalCOD,
		Status:             aggStatus,
		Quantities:         mergedQty,
		Timeline:           allTimeline,
		Parcels:            parcels,
	}, nil
}
