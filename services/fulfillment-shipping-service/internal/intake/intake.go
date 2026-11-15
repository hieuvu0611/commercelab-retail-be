// Package intake turns order events into shipments (B04): a confirmed order becomes a parcel awaiting pick.
package intake

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
)

// AggregateShipment is the aggregate type of shipment events (topic commercelab.shipment.events).
const AggregateShipment = "Shipment"

// Intake handles order events. It is idempotent: a repeated event (same id or same order) changes nothing.
type Intake struct {
	Store shipment.Store
	Now   func() time.Time
}

type confirmedPayload struct {
	OrderID     string          `json:"orderId"`
	UserID      string          `json:"userId"`
	WarehouseID string          `json:"warehouseId"`
	Items       []shipment.Item `json:"items"`
}

// Handle implements eventconsumer.Handler. Events other than order.confirmed are ignored. A confirmed event
// that can never become a shipment (missing data) is logged by order id and skipped, because retrying it
// would block the partition forever; store failures are returned so the event is retried.
func (i *Intake) Handle(ctx context.Context, e events.DomainEvent) error {
	if e.EventType != events.EventOrderConfirmed {
		return nil
	}
	raw, err := json.Marshal(e.Payload)
	var p confirmedPayload
	if err == nil {
		err = json.Unmarshal(raw, &p)
	}
	if err != nil {
		log.Printf("[fulfillment] skipping %s of order %s: unreadable payload", e.EventType, e.AggregateID)
		return nil
	}
	s, err := shipment.New(p.OrderID, p.UserID, p.WarehouseID, p.Items, i.Now())
	if err != nil {
		log.Printf("[fulfillment] skipping %s of order %s: %v (warehouse %q, %d lines)", e.EventType, p.OrderID, err, p.WarehouseID, len(p.Items))
		return nil
	}
	items := make([]map[string]any, len(s.Items))
	for n, it := range s.Items {
		items[n] = map[string]any{"skuId": it.SkuID, "quantity": it.Quantity}
	}
	created := outbox.NewEvent(events.EventShipmentCreated, AggregateShipment, s.ShipmentID, i.Now(),
		map[string]any{"shipmentId": s.ShipmentID, "orderId": s.OrderID, "warehouseId": s.WarehouseID, "items": items})
	_, _, err = i.Store.Create(ctx, s, e.EventID, created)
	return err
}
