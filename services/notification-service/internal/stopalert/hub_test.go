package stopalert

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

func stopRequested(id, shipment, order, warehouse string) events.DomainEvent {
	return events.DomainEvent{EventID: id, EventType: events.EventShipmentStopRequested, AggregateType: "Shipment", AggregateID: shipment,
		Payload: map[string]any{"shipmentId": shipment, "orderId": order, "warehouseId": warehouse, "requestedAt": "2026-11-15T10:00:00Z"}}
}

func resolved(id, typ, shipment, order, warehouse string) events.DomainEvent {
	return events.DomainEvent{EventID: id, EventType: typ, AggregateType: "Shipment", AggregateID: shipment,
		Payload: map[string]any{"shipmentId": shipment, "orderId": order, "warehouseId": warehouse}}
}

func next(t *testing.T, sub *Subscription) Message {
	t.Helper()
	select {
	case m, ok := <-sub.C:
		if !ok {
			t.Fatal("the subscription was closed")
		}
		return m
	case <-time.After(time.Second):
		t.Fatal("no message")
	}
	return Message{}
}

func none(t *testing.T, sub *Subscription) {
	t.Helper()
	select {
	case m := <-sub.C:
		t.Fatalf("unexpected message %+v", m)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestAStopRequestReachesOnlyTheShipmentsWarehouse(t *testing.T) {
	h := NewHub()
	hcm, hn := h.Subscribe("WH-HCM-01"), h.Subscribe("WH-HN-01")
	defer hcm.Close()
	defer hn.Close()
	if err := h.HandleEvent(context.Background(), stopRequested("evt-1", "SHP-O-1", "O-1", "WH-HCM-01")); err != nil {
		t.Fatal(err)
	}
	m := next(t, hcm)
	if m.Event != "stop_requested" || m.ID != "evt-1" {
		t.Fatalf("message = %+v", m)
	}
	alert, ok := m.Data.(Alert)
	if !ok || alert.Type != "WAREHOUSE_STOP_SIGNAL" || alert.ShipmentID != "SHP-O-1" || alert.OrderID != "O-1" || alert.WarehouseID != "WH-HCM-01" || alert.RequestedAt == "" || alert.Message == "" {
		t.Fatalf("alert = %+v", m.Data)
	}
	none(t, hn)
}

func TestARepeatedEventAlertsOnce(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe("WH-HCM-01")
	defer sub.Close()
	for i := 0; i < 3; i++ {
		_ = h.HandleEvent(context.Background(), stopRequested("evt-1", "SHP-O-1", "O-1", "WH-HCM-01"))
	}
	next(t, sub)
	none(t, sub)
	// A new event id for the same shipment is the same open alert, not a second one.
	_ = h.HandleEvent(context.Background(), stopRequested("evt-2", "SHP-O-1", "O-1", "WH-HCM-01"))
	none(t, sub)
}

func TestALateSubscriberSeesTheOpenAlertsOfItsWarehouseOnly(t *testing.T) {
	h := NewHub()
	_ = h.HandleEvent(context.Background(), stopRequested("evt-1", "SHP-O-1", "O-1", "WH-HCM-01"))
	_ = h.HandleEvent(context.Background(), stopRequested("evt-2", "SHP-O-2", "O-2", "WH-HN-01"))
	sub := h.Subscribe("WH-HCM-01")
	defer sub.Close()
	if len(sub.Initial) != 1 || sub.Initial[0].Event != "stop_requested" || sub.Initial[0].Data.(Alert).ShipmentID != "SHP-O-1" {
		t.Fatalf("initial = %+v", sub.Initial)
	}
}

func TestResolutionClosesTheAlertAndTellsTheWarehouse(t *testing.T) {
	for typ, outcome := range map[string]string{events.EventShipmentStopped: "STOPPED", events.EventShipmentHandedOver: "HANDED_OVER"} {
		h := NewHub()
		sub := h.Subscribe("WH-HCM-01")
		_ = h.HandleEvent(context.Background(), stopRequested("evt-1", "SHP-O-1", "O-1", "WH-HCM-01"))
		next(t, sub)
		_ = h.HandleEvent(context.Background(), resolved("evt-2", typ, "SHP-O-1", "O-1", "WH-HCM-01"))
		m := next(t, sub)
		r, ok := m.Data.(Resolution)
		if m.Event != "stop_resolved" || !ok || r.Outcome != outcome || r.ShipmentID != "SHP-O-1" || r.Type != "WAREHOUSE_STOP_RESOLVED" {
			t.Fatalf("%s: message = %+v", typ, m)
		}
		sub.Close()
		if late := h.Subscribe("WH-HCM-01"); len(late.Initial) != 0 {
			t.Fatalf("%s: a resolved alert is still open: %+v", typ, late.Initial)
		}
	}
}

func TestResolutionWithoutAnOpenAlertIsIgnored(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe("WH-HCM-01")
	defer sub.Close()
	// The immediate stop of an unpicked parcel (case 2) never raised an alert, so nothing resolves.
	_ = h.HandleEvent(context.Background(), resolved("evt-1", events.EventShipmentStopped, "SHP-O-1", "O-1", "WH-HCM-01"))
	none(t, sub)
}

func TestUnreadableOrOtherEventsAreSkipped(t *testing.T) {
	h := NewHub()
	sub := h.Subscribe("WH-HCM-01")
	defer sub.Close()
	bad := stopRequested("evt-1", "", "O-1", "WH-HCM-01")
	noWarehouse := stopRequested("evt-2", "SHP-O-1", "O-1", "")
	other := stopRequested("evt-3", "SHP-O-1", "O-1", "WH-HCM-01")
	other.EventType = events.EventShipmentCreated
	for _, e := range []events.DomainEvent{bad, noWarehouse, other} {
		if err := h.HandleEvent(context.Background(), e); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
	}
	none(t, sub)
}

func TestASlowSubscriberIsDroppedWithoutBlockingOthers(t *testing.T) {
	h := NewHub()
	slow, fast := h.Subscribe("WH-HCM-01"), h.Subscribe("WH-HCM-01")
	defer fast.Close()
	for i := 0; i < bufferSize+5; i++ {
		_ = h.HandleEvent(context.Background(), stopRequested(fmt.Sprintf("evt-%d", i), fmt.Sprintf("SHP-O-%d", i), fmt.Sprintf("O-%d", i), "WH-HCM-01"))
		select { // the fast one keeps up
		case <-fast.C:
		case <-time.After(time.Second):
			t.Fatal("the fast subscriber starved")
		}
	}
	closed := false
	for i := 0; i < bufferSize+5 && !closed; i++ {
		select {
		case _, ok := <-slow.C:
			closed = !ok
		default:
			t.Fatal("the slow subscriber was neither fed nor closed")
		}
	}
	if !closed {
		t.Fatal("the slow subscriber was not dropped")
	}
	slow.Close() // closing a dropped subscription is harmless
}

func TestConcurrentSubscribersAndEventsAreRaceFree(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			sub := h.Subscribe("WH-HCM-01")
			for j := 0; j < 5; j++ {
				select {
				case <-sub.C:
				case <-time.After(5 * time.Millisecond):
				}
			}
			sub.Close()
		}()
		go func() {
			defer wg.Done()
			_ = h.HandleEvent(context.Background(), stopRequested(fmt.Sprintf("evt-%d", i), fmt.Sprintf("SHP-O-%d", i), fmt.Sprintf("O-%d", i), "WH-HCM-01"))
			_ = h.HandleEvent(context.Background(), resolved(fmt.Sprintf("evt-r-%d", i), events.EventShipmentStopped, fmt.Sprintf("SHP-O-%d", i), fmt.Sprintf("O-%d", i), "WH-HCM-01"))
		}()
	}
	wg.Wait()
	if n := h.Subscribers("WH-HCM-01"); n != 0 {
		t.Fatalf("subscribers left = %d", n)
	}
}
