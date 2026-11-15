package main

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

// record appends a timeline entry. The timeline is a read model: a failure is logged and never fails the change
// that was already stored.
func (d Deps) record(r *http.Request, s shipment.Shipment, typ, detail string) {
	if d.Timeline == nil {
		return
	}
	if err := d.Timeline.Append(r.Context(), s.ShipmentID, s.OrderID, timeline.Entry{At: d.now(), Type: typ, Detail: detail}); err != nil {
		log.Printf("[fulfillment] timeline entry %s of shipment %s was not recorded: %v", typ, s.ShipmentID, err)
	}
}

// statusEntry maps a status reached by a carrier notice to its timeline entry type.
var statusEntry = map[shipment.Status]string{
	shipment.StatusInTransit: timeline.TypeInTransit, shipment.StatusDelivered: timeline.TypeDelivered,
	shipment.StatusDeliveryFailed: timeline.TypeDeliveryFailed, shipment.StatusPartiallyDelivered: timeline.TypePartiallyDelivered,
}

type quantityView struct {
	SkuID      string `json:"skuId"`
	Ordered    int64  `json:"ordered"`
	Dispatched int64  `json:"dispatched"`
	Delivered  int64  `json:"delivered"`
	Returned   int64  `json:"returned"` // returns arrive with the returns phase; always 0 before that
}

type entryView struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

// trackingView is what a customer sees: no warehouse, version or staff detail.
type trackingView struct {
	OrderID            string         `json:"orderId"`
	ShipmentID         string         `json:"shipmentId"`
	Status             string         `json:"status"`
	HandedOverAt       *time.Time     `json:"handedOverAt,omitempty"`
	DeliveredAt        *time.Time     `json:"deliveredAt,omitempty"`
	ReturnWindowEndsAt *time.Time     `json:"returnWindowEndsAt,omitempty"`
	Quantities         []quantityView `json:"quantities"`
	Timeline           []entryView    `json:"timeline"`
}

func customerMessage(e timeline.Entry) string {
	switch e.Type {
	case timeline.TypeCreated:
		return "The warehouse received your order"
	case timeline.TypePickingStarted:
		return "We started picking your items"
	case timeline.TypePickBlocked:
		return "We are checking the stock of an item in your order" // the internal reason is not shown
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
	}
	return e.Type
}

// registerTrackingRoute serves the customer's tracking view of their own order.
func registerTrackingRoute(mux *http.ServeMux, d Deps) {
	mux.Handle("GET /api/v1/orders/{orderId}/tracking", auth.RequireAuth(auth.RequireRole("CUSTOMER")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFrom(r.Context())
		s, err := d.Shipments.GetByOrder(r.Context(), r.PathValue("orderId"))
		if errors.Is(err, shipment.ErrNotFound) || (err == nil && s.UserID != claims.UserID) {
			response.Error(w, r, http.StatusNotFound, "TRACKING_NOT_FOUND", "No tracking is available for this order", nil)
			return
		}
		if err != nil {
			writeStoreError(w, r, err)
			return
		}
		entries := []timeline.Entry{}
		if d.Timeline != nil {
			if entries, err = d.Timeline.Get(r.Context(), s.ShipmentID); err != nil {
				writeStoreError(w, r, err)
				return
			}
		}
		response.JSON(w, http.StatusOK, "Tracking", trackingOf(s, entries), nil)
	}))))
}

func trackingOf(s shipment.Shipment, entries []timeline.Entry) trackingView {
	v := trackingView{OrderID: s.OrderID, ShipmentID: s.ShipmentID, Status: string(s.Status), HandedOverAt: s.HandedOverAt,
		DeliveredAt: s.DeliveredAt, ReturnWindowEndsAt: s.ReturnWindowEndsAt,
		Quantities: make([]quantityView, len(s.Items)), Timeline: make([]entryView, len(entries))}
	for i, it := range s.Items {
		q := quantityView{SkuID: it.SkuID, Ordered: it.Quantity, Delivered: it.DeliveredQuantity}
		if s.Dispatched() {
			q.Dispatched = it.PickedQuantity
		}
		v.Quantities[i] = q
	}
	for i, e := range entries {
		v.Timeline[i] = entryView{At: e.At, Type: e.Type, Message: customerMessage(e)}
	}
	return v
}
