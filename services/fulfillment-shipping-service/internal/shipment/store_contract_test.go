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

	t.Run("save is a compare-and-set on the version and keeps pick progress", func(t *testing.T) {
		s := newShipment(uniq("O"))
		stored, _, _ := store.Create(ctx, s, uniq("evt"), createdEvent(s))
		picked, err := stored.Scan("SKU-A", 2, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(ctx, picked, stored.Version); err != nil {
			t.Fatal(err)
		}
		got, _ := store.Get(ctx, s.ShipmentID)
		if got.Version != 2 || got.Status != StatusPicking || got.Items[0].PickedQuantity != 2 {
			t.Fatalf("saved = %+v", got)
		}
		if err := store.Save(ctx, picked, stored.Version); !errors.Is(err, ErrStaleVersion) {
			t.Fatalf("stale save err = %v", err)
		}
		ghost := newShipment(uniq("O"))
		if err := store.Save(ctx, ghost, 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ghost save err = %v", err)
		}
	})

	t.Run("a handover is stored with its time and its event", func(t *testing.T) {
		s := newShipment(uniq("O"))
		stored, _, _ := store.Create(ctx, s, uniq("evt"), createdEvent(s))
		cur := stored
		for _, step := range []func(Shipment) (Shipment, error){
			func(x Shipment) (Shipment, error) { return x.Scan("SKU-A", 2, now) },
			func(x Shipment) (Shipment, error) { return x.Scan("SKU-B", 1, now) },
			func(x Shipment) (Shipment, error) { return x.Pack(now) },
			func(x Shipment) (Shipment, error) { return x.Handover(now) },
		} {
			next, err := step(cur)
			if err != nil {
				t.Fatal(err)
			}
			var ev []events.DomainEvent
			if next.Status == StatusHandedOver {
				ev = append(ev, events.DomainEvent{EventID: uniq("evt-h"), EventType: events.EventShipmentHandedOver, AggregateType: "Shipment", AggregateID: s.ShipmentID, OccurredAt: now, Payload: map[string]any{}})
			}
			if err := store.Save(ctx, next, cur.Version, ev...); err != nil {
				t.Fatal(err)
			}
			cur = next
		}
		got, _ := store.Get(ctx, s.ShipmentID)
		if got.Status != StatusHandedOver || got.HandedOverAt == nil || !got.HandedOverAt.Equal(now) {
			t.Fatalf("stored = %+v", got)
		}
		if types := outboxTypes(s.ShipmentID); len(types) != 2 || types[1] != events.EventShipmentHandedOver {
			t.Fatalf("outbox = %v", types)
		}
	})

	t.Run("a carrier event id is applied once, with the delivery fields stored", func(t *testing.T) {
		s := newShipment(uniq("O"))
		stored, _, _ := store.Create(ctx, s, uniq("evt"), createdEvent(s))
		cur := stored
		for _, step := range []func(Shipment) (Shipment, error){
			func(x Shipment) (Shipment, error) { return x.Scan("SKU-A", 2, now) },
			func(x Shipment) (Shipment, error) { return x.Scan("SKU-B", 1, now) },
			func(x Shipment) (Shipment, error) { return x.Pack(now) },
			func(x Shipment) (Shipment, error) { return x.Handover(now) },
		} {
			next, _ := step(cur)
			if err := store.Save(ctx, next, cur.Version); err != nil {
				t.Fatal(err)
			}
			cur = next
		}
		delivered, _ := cur.MarkDelivered(now)
		carrierID := uniq("carrier")
		if err := store.SaveCarrierEvent(ctx, delivered, cur.Version, carrierID); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCarrierEvent(ctx, delivered, cur.Version, carrierID); !errors.Is(err, ErrDuplicateEvent) {
			t.Fatalf("repeat err = %v", err)
		}
		got, _ := store.Get(ctx, s.ShipmentID)
		if got.Status != StatusDelivered || got.DeliveredAt == nil || got.ReturnWindowEndsAt == nil || !got.ReturnWindowEndsAt.Equal(now.Add(ReturnWindow)) || got.Items[0].DeliveredQuantity != 2 {
			t.Fatalf("stored = %+v", got)
		}
		// A failed save does not burn the carrier event id.
		failedID := uniq("carrier")
		if err := store.SaveCarrierEvent(ctx, delivered, 1, failedID); !errors.Is(err, ErrStaleVersion) {
			t.Fatalf("stale err = %v", err)
		}
		if err := store.SaveCarrierEvent(ctx, delivered, delivered.Version, failedID); errors.Is(err, ErrDuplicateEvent) {
			t.Fatal("a rolled back save consumed the carrier event id")
		}
	})

	t.Run("a case is stored with the change, listed by scope, and opened once", func(t *testing.T) {
		wh := uniq("WH")
		s, _ := New(uniq("O"), "u", wh, []Item{{SkuID: "SKU-A", Quantity: 2}}, now)
		stored, _, _ := store.Create(ctx, s, uniq("evt"), createdEvent(s))
		blocked, err := stored.ReportShortage("SKU-A", 1, "shelf empty", now)
		if err != nil {
			t.Fatal(err)
		}
		c := NewCase(blocked, CasePickShortage, "SKU-A", 1, "shelf empty", now)
		if err := store.SaveWith(ctx, blocked, stored.Version, SaveOptions{Case: &c}); err != nil {
			t.Fatal(err)
		}
		got, _ := store.Get(ctx, s.ShipmentID)
		if got.Status != StatusPickBlocked || got.FailureReason != "shelf empty" {
			t.Fatalf("stored = %+v", got)
		}
		list, total, err := store.ListCases(ctx, CaseFilter{Warehouses: []string{wh}})
		if err != nil || total != 1 || list[0].CaseID != c.CaseID || list[0].Type != CasePickShortage || !list[0].SkuBlocked || list[0].Quantity != 1 {
			t.Fatalf("cases = %+v (%d), %v", list, total, err)
		}
		if _, total, _ := store.ListCases(ctx, CaseFilter{Warehouses: nil}); total != 0 {
			t.Fatalf("no scope listed %d cases", total)
		}
		if _, total, _ := store.ListCases(ctx, CaseFilter{Warehouses: []string{wh}, Type: CaseDeliveryFailed}); total != 0 {
			t.Fatalf("type filter listed %d cases", total)
		}
		// The same case id again opens nothing new.
		if err := store.SaveWith(ctx, blocked, blocked.Version, SaveOptions{Case: &c}); err != nil {
			t.Fatal(err)
		}
		if _, total, _ := store.ListCases(ctx, CaseFilter{Warehouses: []string{wh}}); total != 1 {
			t.Fatalf("%d cases after a repeat", total)
		}
	})

	t.Run("twenty concurrent saves of one version store exactly one", func(t *testing.T) {
		s := newShipment(uniq("O"))
		stored, _, _ := store.Create(ctx, s, uniq("evt"), createdEvent(s))
		next, _ := stored.Scan("SKU-B", 1, now)
		var wg sync.WaitGroup
		var won atomic.Int64
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := store.Save(ctx, next, stored.Version); err == nil {
					won.Add(1)
				} else if !errors.Is(err, ErrStaleVersion) {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d saves won", won.Load())
		}
	})

	t.Run("list filters by status and warehouse scope and pages", func(t *testing.T) {
		wh := uniq("WH")
		mk := func(wh string) Shipment {
			s, _ := New(uniq("O"), "u", wh, []Item{{SkuID: "A", Quantity: 1}}, now)
			_, _, _ = store.Create(ctx, s, uniq("evt"), createdEvent(s))
			return s
		}
		a, b := mk(wh), mk(wh)
		mk(uniq("WH-OTHER"))
		got, total, err := store.List(ctx, Filter{Warehouses: []string{wh}})
		if err != nil || total != 2 || len(got) != 2 {
			t.Fatalf("scoped list = %d/%d, %v", len(got), total, err)
		}
		if _, total, _ := store.List(ctx, Filter{Warehouses: nil}); total != 0 {
			t.Fatalf("no scope listed %d", total)
		}
		page, total, _ := store.List(ctx, Filter{Warehouses: []string{wh}, Limit: 1, Offset: 1})
		if total != 2 || len(page) != 1 {
			t.Fatalf("page = %d/%d", len(page), total)
		}
		picked, _ := a.Scan("A", 1, now)
		_ = store.Save(ctx, picked, 1)
		onlyPicking, total, _ := store.List(ctx, Filter{Warehouses: []string{wh}, Status: StatusPicking})
		if total != 1 || onlyPicking[0].ShipmentID != a.ShipmentID || b.ShipmentID == a.ShipmentID {
			t.Fatalf("status filter = %+v", onlyPicking)
		}
		if _, total, _ := store.List(ctx, Filter{AllWarehouses: true}); total < 3 {
			t.Fatalf("all warehouses listed %d", total)
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
