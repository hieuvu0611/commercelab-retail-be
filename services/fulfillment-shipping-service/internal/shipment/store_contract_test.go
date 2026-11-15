package shipment

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

func createdEvent(s Shipment) events.DomainEvent {
	return events.DomainEvent{EventID: "evt-created-" + s.ShipmentID, EventType: events.EventShipmentCreated, AggregateType: "Shipment", AggregateID: s.ShipmentID, OccurredAt: now, Payload: map[string]any{"orderId": s.OrderID}}
}

// runStoreContract holds for every Store; outboxTypes lists the event types stored for a shipment.
func runStoreContract(t *testing.T, store Store, uniq func(string) string, outboxTypes func(shipmentID string) []string) {
	ctx := context.Background()
	newShipment := func(order string) Shipment {
		s, err := New(order, "user-1", "WH-HCM-01", []Item{{SkuID: "SKU-A", Quantity: 2}, {SkuID: "SKU-B", Quantity: 1}}, now)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("create stores the parcel with its lines and one event", func(t *testing.T) {
		s := newShipment(uniq("O"))
		stored, created, err := store.Create(ctx, s, uniq("evt"), createdEvent(s))
		if err != nil || !created {
			t.Fatalf("create = %v, %v", created, err)
		}
		got, err := store.Get(ctx, stored.ShipmentID)
		if err != nil || got.OrderID != s.OrderID || got.UserID != "user-1" || got.WarehouseID != "WH-HCM-01" || len(got.Items) != 2 || got.Status != StatusPendingPick {
			t.Fatalf("get = %+v, %v", got, err)
		}
		byOrder, err := store.GetByOrder(ctx, s.OrderID)
		if err != nil || byOrder.ShipmentID != s.ShipmentID {
			t.Fatalf("by order = %+v, %v", byOrder, err)
		}
		if types := outboxTypes(s.ShipmentID); len(types) != 1 || types[0] != events.EventShipmentCreated {
			t.Fatalf("outbox = %v", types)
		}
	})

	t.Run("the same event twice creates one shipment and one event", func(t *testing.T) {
		s := newShipment(uniq("O"))
		evt := uniq("evt")
		if _, created, err := store.Create(ctx, s, evt, createdEvent(s)); err != nil || !created {
			t.Fatalf("first = %v, %v", created, err)
		}
		again, created, err := store.Create(ctx, s, evt, createdEvent(s))
		if err != nil || created || again.ShipmentID != s.ShipmentID {
			t.Fatalf("second = %+v, %v, %v", again, created, err)
		}
		if n := len(outboxTypes(s.ShipmentID)); n != 1 {
			t.Fatalf("outbox rows = %d", n)
		}
	})

	t.Run("another event id for the same order creates nothing", func(t *testing.T) {
		s := newShipment(uniq("O"))
		_, _, _ = store.Create(ctx, s, uniq("evt"), createdEvent(s))
		_, created, err := store.Create(ctx, s, uniq("evt"), createdEvent(s))
		if err != nil || created {
			t.Fatalf("created = %v, %v", created, err)
		}
	})

	t.Run("twenty concurrent deliveries create one shipment", func(t *testing.T) {
		s := newShipment(uniq("O"))
		evt := uniq("evt")
		var wg sync.WaitGroup
		var made atomic.Int64
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, created, err := store.Create(ctx, s, evt, createdEvent(s)); err == nil && created {
					made.Add(1)
				} else if err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if made.Load() != 1 || len(outboxTypes(s.ShipmentID)) != 1 {
			t.Fatalf("created %d times, outbox %v", made.Load(), outboxTypes(s.ShipmentID))
		}
	})

	t.Run("missing shipments are not found", func(t *testing.T) {
		if _, err := store.Get(ctx, "SHP-"+uniq("none")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get err = %v", err)
		}
		if _, err := store.GetByOrder(ctx, uniq("none")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("by order err = %v", err)
		}
	})
}
