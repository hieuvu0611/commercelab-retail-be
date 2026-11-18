// Package intake turns order events into shipments (B04): a confirmed order becomes 1 or 2 parcels awaiting pick.
package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/outbox"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

// AggregateShipment is the aggregate type of shipment events (topic commercelab.shipment.events).
const AggregateShipment = "Shipment"

// Intake handles order events. It is idempotent: a repeated event (same id or same order/parcel) changes nothing.
type Intake struct {
	Store shipment.Store
	Now   func() time.Time
	// Timeline records the CREATED entry (optional; a failure is logged and never fails the event).
	Timeline timeline.Recorder
}

type parcelSpec struct {
	ParcelSeq          int             `json:"parcelSeq"`
	TotalParcels       int             `json:"totalParcels"`
	WarehouseID        string          `json:"warehouseId"`
	CodAmountToCollect int64           `json:"codAmountToCollect"`
	Items              []shipment.Item `json:"items"`
}

type confirmedPayload struct {
	OrderID            string          `json:"orderId"`
	UserID             string          `json:"userId"`
	WarehouseID        string          `json:"warehouseId"`
	PaymentMethod      string          `json:"paymentMethod"`
	CodAmountToCollect int64           `json:"codAmountToCollect"`
	TotalPayableVND    int64           `json:"totalPayableVnd"`
	Items              []shipment.Item `json:"items"`
	Parcels            []parcelSpec    `json:"parcels"`
}

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

	if len(p.Parcels) > 1 {
		total := len(p.Parcels)
		for idx, spec := range p.Parcels {
			seq := spec.ParcelSeq
			if seq <= 0 {
				seq = idx + 1
			}
			s, err := shipment.NewParcel(p.OrderID, p.UserID, spec.WarehouseID, p.PaymentMethod, spec.CodAmountToCollect, seq, total, spec.Items, i.Now())
			if err != nil {
				log.Printf("[fulfillment] skipping %s parcel %d of order %s: %v", e.EventType, seq, p.OrderID, err)
				continue
			}
			if err := i.createOne(ctx, s, fmt.Sprintf("%s#%d", e.EventID, seq)); err != nil {
				return err
			}
		}
		return nil
	}

	codAmount := p.CodAmountToCollect
	if p.PaymentMethod == "COD" && codAmount <= 0 {
		codAmount = p.TotalPayableVND
	}
	s, err := shipment.NewWithCOD(p.OrderID, p.UserID, p.WarehouseID, p.PaymentMethod, codAmount, p.Items, i.Now())
	if err != nil {
		log.Printf("[fulfillment] skipping %s of order %s: %v (warehouse %q, %d lines)", e.EventType, p.OrderID, err, p.WarehouseID, len(p.Items))
		return nil
	}
	return i.createOne(ctx, s, e.EventID)
}

func (i *Intake) createOne(ctx context.Context, s shipment.Shipment, eventID string) error {
	items := make([]map[string]any, len(s.Items))
	for n, it := range s.Items {
		items[n] = map[string]any{"skuId": it.SkuID, "quantity": it.Quantity}
	}
	created := outbox.NewEvent(events.EventShipmentCreated, AggregateShipment, s.ShipmentID, i.Now(),
		map[string]any{
			"shipmentId": s.ShipmentID, "orderId": s.OrderID, "warehouseId": s.WarehouseID,
			"parcelIndex": s.ParcelIndex, "totalParcels": s.TotalParcels, "items": items,
			"paymentMethod": s.PaymentMethod, "codAmountToCollect": s.CodAmountToCollect,
		})
	_, isNew, err := i.Store.Create(ctx, s, eventID, created)
	if err == nil && isNew && i.Timeline != nil {
		if tlErr := i.Timeline.Append(ctx, s.ShipmentID, s.OrderID, timeline.Entry{At: i.Now(), Type: timeline.TypeCreated}); tlErr != nil {
			log.Printf("[fulfillment] timeline entry for shipment %s was not recorded: %v", s.ShipmentID, tlErr)
		}
	}
	return err
}
