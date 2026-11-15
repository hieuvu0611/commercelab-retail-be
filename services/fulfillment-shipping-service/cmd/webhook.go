package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
)

// Carrier notice types.
const (
	noticeInTransit      = "IN_TRANSIT"
	noticeDelivered      = "DELIVERED"
	noticeDeliveryFailed = "DELIVERY_FAILED"
)

type noticeBody struct {
	EventID    string `json:"eventId"`
	ShipmentID string `json:"shipmentId"`
	Type       string `json:"type"`
	Reason     string `json:"reason"`
	OccurredAt string `json:"occurredAt"` // informational; the service uses its own clock
}

// noticeResult is the webhook answer. Applied=false with Duplicate=true means the event id was seen before;
// Applied=false alone means the notice was late (the parcel is already further along).
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
			response.JSON(w, http.StatusOK, "Notice ignored", noticeResult{ShipmentID: s.ShipmentID, Status: string(s.Status)}, nil)
			return
		}
		var ev []events.DomainEvent
		if next.Status == shipment.StatusDelivered {
			ev = append(ev, deliveredEvent(next, d))
		}
		err = d.Shipments.SaveCarrierEvent(r.Context(), next, s.Version, n.EventID, ev...)
		switch {
		case errors.Is(err, shipment.ErrStaleVersion):
			continue // another change got in first: look again
		case errors.Is(err, shipment.ErrDuplicateEvent):
			response.JSON(w, http.StatusOK, "Notice already applied", noticeResult{ShipmentID: s.ShipmentID, Status: string(s.Status), Duplicate: true}, nil)
		case err != nil:
			writeStoreError(w, r, err)
		default:
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
	default:
		return s.MarkDeliveryFailed(n.Reason, now)
	}
}

func deliveredEvent(s shipment.Shipment, d Deps) events.DomainEvent {
	items := make([]map[string]any, len(s.Items))
	for i, it := range s.Items {
		items[i] = map[string]any{"skuId": it.SkuID, "quantity": it.DeliveredQuantity}
	}
	return outbox.NewEvent(events.EventShipmentDelivered, aggregateShipment, s.ShipmentID, d.now(), map[string]any{
		"shipmentId": s.ShipmentID, "orderId": s.OrderID, "warehouseId": s.WarehouseID, "items": items,
		"deliveredAt": s.DeliveredAt, "returnWindowEndsAt": s.ReturnWindowEndsAt,
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
