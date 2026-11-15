package fulfillmentsync

import (
	"context"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
)

func orderIn(t *testing.T, store orders.Store, id, status string) {
	t.Helper()
	o := orders.Order{OrderID: id, UserID: "u", QuoteID: "Q-" + id, Status: status, PaymentStatus: "CAPTURED", TotalPayableVND: 660000,
		Items: []orders.Item{{SkuID: "A", Quantity: 1}}}
	if _, _, err := store.Create(context.Background(), "key-"+id, "hash-"+id, o); err != nil {
		t.Fatal(err)
	}
}

func shipmentEvent(typ, orderID string) events.DomainEvent {
	return events.DomainEvent{EventID: "evt-" + typ + orderID, EventType: typ, AggregateType: "Shipment", AggregateID: "SHP-" + orderID,
		OccurredAt: time.Now(), Payload: map[string]any{"orderId": orderID}}
}

func status(t *testing.T, store orders.Store, id string) string {
	t.Helper()
	o, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return o.Status
}

func TestShipmentCreatedMovesAConfirmedOrderIntoFulfillment(t *testing.T) {
	store := orders.NewMemoryStore()
	orderIn(t, store, "O-1", "CONFIRMED")
	h := &Handler{Orders: store}
	for range 2 { // a repeated event changes nothing
		if err := h.Handle(context.Background(), shipmentEvent(events.EventShipmentCreated, "O-1")); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := store.Get(context.Background(), "O-1")
	if o.Status != "IN_FULFILLMENT" || o.PaymentStatus != "CAPTURED" || o.Version != 2 {
		t.Fatalf("order = %+v", o)
	}
}

func TestShipmentCreatedNeverMovesAnOrderBackwardsOrOutOfAClosedState(t *testing.T) {
	store := orders.NewMemoryStore()
	orderIn(t, store, "O-1", "DELIVERED")
	orderIn(t, store, "O-2", "FAILED_CLOSED")
	h := &Handler{Orders: store}
	for _, id := range []string{"O-1", "O-2"} {
		if err := h.Handle(context.Background(), shipmentEvent(events.EventShipmentCreated, id)); err != nil {
			t.Fatal(err)
		}
	}
	if status(t, store, "O-1") != "DELIVERED" || status(t, store, "O-2") != "FAILED_CLOSED" {
		t.Fatal("a state moved")
	}
}

func TestUnknownOrdersAndOtherEventsAreIgnored(t *testing.T) {
	h := &Handler{Orders: orders.NewMemoryStore()}
	if err := h.Handle(context.Background(), shipmentEvent(events.EventShipmentCreated, "O-404")); err != nil {
		t.Fatalf("unknown order: %v", err)
	}
	if err := h.Handle(context.Background(), shipmentEvent(events.EventOrderCreated, "O-404")); err != nil {
		t.Fatalf("other event: %v", err)
	}
}

func TestShipmentDeliveredCompletesAnOrderInFulfillmentOnce(t *testing.T) {
	store := orders.NewMemoryStore()
	orderIn(t, store, "O-1", "IN_FULFILLMENT")
	h := &Handler{Orders: store}
	for range 3 {
		if err := h.Handle(context.Background(), shipmentEvent(events.EventShipmentDelivered, "O-1")); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := store.Get(context.Background(), "O-1")
	if o.Status != "DELIVERED" || o.Version != 2 {
		t.Fatalf("order = %+v", o)
	}
}

func TestShipmentDeliveredDoesNotTouchOrdersThatNeverEnteredFulfillment(t *testing.T) {
	store := orders.NewMemoryStore()
	orderIn(t, store, "O-1", "FAILED_CLOSED")
	h := &Handler{Orders: store}
	if err := h.Handle(context.Background(), shipmentEvent(events.EventShipmentDelivered, "O-1")); err != nil {
		t.Fatal(err)
	}
	if status(t, store, "O-1") != "FAILED_CLOSED" {
		t.Fatal("a closed order was delivered")
	}
}

type fakeSettler struct{ calls []string }

func (f *fakeSettler) SettleCancel(_ context.Context, orderID string) error {
	f.calls = append(f.calls, orderID)
	return nil
}

func TestStoppedAndHandedOverEventsSettleAWaitingCancellation(t *testing.T) {
	settler := &fakeSettler{}
	h := &Handler{Orders: orders.NewMemoryStore(), Cancels: settler}
	for _, typ := range []string{events.EventShipmentStopped, events.EventShipmentHandedOver} {
		if err := h.Handle(context.Background(), shipmentEvent(typ, "O-1")); err != nil {
			t.Fatal(err)
		}
	}
	if len(settler.calls) != 2 || settler.calls[0] != "O-1" {
		t.Fatalf("settles = %v", settler.calls)
	}
	// Without a settler, or without an order id, the events are skipped instead of blocking the partition.
	if err := (&Handler{Orders: orders.NewMemoryStore()}).Handle(context.Background(), shipmentEvent(events.EventShipmentStopped, "O-1")); err != nil {
		t.Fatal(err)
	}
	noID := shipmentEvent(events.EventShipmentStopped, "")
	noID.Payload = map[string]any{}
	if err := h.Handle(context.Background(), noID); err != nil || len(settler.calls) != 2 {
		t.Fatalf("no order id: %v calls=%v", err, settler.calls)
	}
}
