package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

const (
	signatureHeader = "X-Signature"
	maxWebhookBytes = 64 << 10
	maxIDLength     = 64
	maxStaleRetries = 5
	maxNoticeItems  = 100
)

// Carrier notice types.
const (
	noticeInTransit      = "IN_TRANSIT"
	noticeDelivered      = "DELIVERED"
	noticeDeliveryFailed = "DELIVERY_FAILED"
	noticePartial        = "PARTIALLY_DELIVERED"
)

type noticeBody struct {
	EventID    string `json:"eventId"`
	ShipmentID string `json:"shipmentId"`
	Type       string `json:"type"`
	Reason     string `json:"reason"`
	// Items are the delivered units of a PARTIALLY_DELIVERED notice.
	Items []struct {
		SkuID    string `json:"skuId"`
		Quantity int64  `json:"quantity"`
	} `json:"items"`
	OccurredAt string `json:"occurredAt"` // informational; the service uses its own clock
}

// noticeResult is the webhook answer. Applied=false with Duplicate=true means the notice changed nothing: its
// event id was seen before, or the parcel is already at (or past) the state it reports.
type noticeResult struct {
	ShipmentID string `json:"shipmentId"`
	Status     string `json:"status"`
	Applied    bool   `json:"applied"`
	Duplicate  bool   `json:"duplicate"`
}

func registerWebhookRoutes(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("POST /api/v1/shipments/webhooks/carrier", carrierWebhook(d))
}

// carrierWebhook receives the carrier's signed notice. The signature is checked before the body is read, and an
// event id that was already applied changes nothing, so a repeated notice never delivers twice.
func carrierWebhook(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
		if err != nil {
			response.Error(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body is too large", nil)
			return
		}
		if !validSignature(d.WebhookSecret, raw, r.Header.Get(signatureHeader)) {
			response.Error(w, r, http.StatusUnauthorized, "INVALID_SIGNATURE", "The webhook signature is missing or invalid", nil)
			return
		}
		var n noticeBody
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		valid := dec.Decode(&n) == nil && n.EventID != "" && len(n.EventID) <= maxIDLength && n.ShipmentID != "" && len(n.ShipmentID) <= maxIDLength
		switch n.Type {
		case noticeInTransit, noticeDelivered:
		case noticeDeliveryFailed:
			valid = valid && strings.TrimSpace(n.Reason) != "" && len(n.Reason) <= shipment.MaxFailureReason
		case noticePartial:
			valid = valid && len(n.Items) > 0 && len(n.Items) <= maxNoticeItems
		default:
			valid = false
		}
		if !valid {
			response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The webhook payload is invalid", nil)
			return
		}
		applyNotice(w, r, d, n)
	}
}

func applyNotice(w http.ResponseWriter, r *http.Request, d Deps, n noticeBody) {
	for range maxStaleRetries {
		s, err := d.Shipments.Get(r.Context(), n.ShipmentID)
		if errors.Is(err, shipment.ErrNotFound) {
			response.Error(w, r, http.StatusNotFound, "SHIPMENT_NOT_FOUND", "Shipment not found", nil)
			return
		}
		if err != nil {
			writeStoreError(w, r, err)
			return
		}
		next, err := transition(s, n, d.now())
		if errors.Is(err, shipment.ErrNotDispatched) {
			response.Error(w, r, http.StatusConflict, "SHIPMENT_NOT_DISPATCHED", "The shipment was not handed over to the carrier", nil)
			return
		}
		if err != nil {
			response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The webhook payload is invalid", nil)
			return
		}
		if next.Version == s.Version {
			response.JSON(w, http.StatusOK, "Notice ignored", noticeResult{ShipmentID: s.ShipmentID, Status: string(s.Status), Duplicate: true}, nil)
			return
		}
		opts := changeOptions(next, d)
		opts.CarrierEventID = n.EventID
		err = d.Shipments.SaveWith(r.Context(), next, s.Version, opts)
		switch {
		case errors.Is(err, shipment.ErrStaleVersion):
			continue // another change got in first: look again
		case errors.Is(err, shipment.ErrDuplicateEvent):
			response.JSON(w, http.StatusOK, "Notice already applied", noticeResult{ShipmentID: s.ShipmentID, Status: string(s.Status), Duplicate: true}, nil)
		case err != nil:
			writeStoreError(w, r, err)
		default:
			detail := ""
			if next.Status == shipment.StatusDeliveryFailed {
				detail = next.FailureReason
			}
			d.record(r, next, statusEntry[next.Status], detail)
			response.JSON(w, http.StatusOK, "Notice applied", noticeResult{ShipmentID: next.ShipmentID, Status: string(next.Status), Applied: true}, nil)
		}
		return
	}
	writeStale(w, r)
}

func transition(s shipment.Shipment, n noticeBody, now time.Time) (shipment.Shipment, error) {
	switch n.Type {
	case noticeInTransit:
		return s.MarkInTransit(now)
	case noticeDelivered:
		return s.MarkDelivered(now)
	case noticePartial:
		delivered := make([]shipment.Item, len(n.Items))
		for i, it := range n.Items {
			delivered[i] = shipment.Item{SkuID: it.SkuID, Quantity: it.Quantity}
		}
		return s.MarkPartiallyDelivered(delivered, now)
	default:
		return s.MarkDeliveryFailed(n.Reason, now)
	}
}

// changeOptions lists what a status change stores besides the shipment: the delivered event, or the exception
// case (and its event) for a failed or partial delivery.
func changeOptions(next shipment.Shipment, d Deps) shipment.SaveOptions {
	var o shipment.SaveOptions
	switch next.Status {
	case shipment.StatusDelivered:
		o.Events = append(o.Events, deliveredEvent(next, d))
		if next.PaymentMethod == "COD" {
			o.Events = append(o.Events, outbox.NewEvent(events.EventPaymentCODCollected, aggregateShipment, next.ShipmentID, d.now(), map[string]any{
				"orderId": next.OrderID, "shipmentId": next.ShipmentID, "amountVnd": next.CodAmountToCollect, "totalParcels": max(1, next.TotalParcels),
			}))
		}
	case shipment.StatusDeliveryFailed:
		c := shipment.NewCase(next, shipment.CaseDeliveryFailed, "", 0, next.FailureReason, d.now())
		o.Case, o.Events = &c, append(o.Events, caseOpenedEvent(c, d))
		if next.PaymentMethod == "COD" {
			o.Events = append(o.Events, outbox.NewEvent(events.EventPaymentCODCancelled, aggregateShipment, next.ShipmentID, d.now(), map[string]any{
				"orderId":      next.OrderID,
				"shipmentId":   next.ShipmentID,
				"reason":       next.FailureReason,
				"totalParcels": max(1, next.TotalParcels),
			}))
		}
	case shipment.StatusPartiallyDelivered:
		var missing, total int64
		for _, it := range next.Items {
			missing, total = missing+it.PickedQuantity-it.DeliveredQuantity, total+it.PickedQuantity
		}
		c := shipment.NewCase(next, shipment.CasePartialDelivery, "", missing, fmt.Sprintf("%d of %d units were not delivered", missing, total), d.now())
		o.Case, o.Events = &c, append(o.Events, caseOpenedEvent(c, d))
	}
	return o
}

func caseOpenedEvent(c shipment.Case, d Deps) events.DomainEvent {
	return outbox.NewEvent(events.EventExceptionCaseOpened, aggregateShipment, c.ShipmentID, d.now(), map[string]any{
		"caseId": c.CaseID, "caseType": c.Type, "reason": c.Reason, "shipmentId": c.ShipmentID, "orderId": c.OrderID,
		"warehouseId": c.WarehouseID, "skuId": c.SkuID, "quantity": c.Quantity,
	})
}

func deliveredEvent(s shipment.Shipment, d Deps) events.DomainEvent {
	items := make([]map[string]any, len(s.Items))
	for i, it := range s.Items {
		items[i] = map[string]any{"skuId": it.SkuID, "quantity": it.DeliveredQuantity}
	}
	return outbox.NewEvent(events.EventShipmentDelivered, aggregateShipment, s.ShipmentID, d.now(), map[string]any{
		"shipmentId": s.ShipmentID, "orderId": s.OrderID, "warehouseId": s.WarehouseID, "items": items,
		"deliveredAt": s.DeliveredAt, "returnWindowEndsAt": s.ReturnWindowEndsAt,
		"paymentMethod": s.PaymentMethod, "codAmountToCollect": s.CodAmountToCollect,
	})
}

// validSignature checks hex(HMAC-SHA256(secret, body)) in constant time. An empty secret never validates.
func validSignature(secret string, body []byte, got string) bool {
	if secret == "" || got == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte(strings.ToLower(got)), []byte(hex.EncodeToString(mac.Sum(nil))))
}
