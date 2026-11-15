package intake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/shipment"
	"github.com/hieuvu0611/commercelab-retail-be/services/fulfillment-shipping-service/internal/timeline"
)

var at = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func confirmed(eventID, orderID string, payload map[string]any) events.DomainEvent {
	base := map[string]any{"orderId": orderID, "userId": "user-1", "warehouseId": "WH-HN-01",
		"items": []any{map[string]any{"skuId": "SKU-A", "quantity": float64(2)}, map[string]any{"skuId": "SKU-B", "quantity": float64(1)}}}
	for k, v := range payload {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	return events.DomainEvent{EventID: eventID, EventType: events.EventOrderConfirmed, AggregateType: "Order", AggregateID: orderID, OccurredAt: at, Payload: base}
}

func newIntake() (*Intake, *shipment.MemoryStore) {
	store := shipment.NewMemoryStore()
	return &Intake{Store: store, Now: func() time.Time { return at }}, store
}

func TestConfirmedOrderCreatesOneShipmentInTheOrdersWarehouse(t *testing.T) {
	in, store := newIntake()
	if err := in.Handle(context.Background(), confirmed("evt-1", "O-1", nil)); err != nil {
		t.Fatal(err)
	}
	s, err := store.GetByOrder(context.Background(), "O-1")
	if err != nil || s.WarehouseID != "WH-HN-01" || s.UserID != "user-1" || s.Status != shipment.StatusPendingPick || len(s.Items) != 2 || s.Items[0].Quantity != 2 {
		t.Fatalf("shipment = %+v, %v", s, err)
	}
	if got := store.EventTypes(s.ShipmentID); len(got) != 1 || got[0] != events.EventShipmentCreated {
		t.Fatalf("events = %v", got)
	}
}

func TestTheSameOrderConfirmedTwiceCreatesOneShipment(t *testing.T) {
	in, store := newIntake()
	for _, id := range []string{"evt-1", "evt-1", "evt-2"} {
		if err := in.Handle(context.Background(), confirmed(id, "O-1", nil)); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.EventTypes("SHP-O-1"); len(got) != 1 {
		t.Fatalf("events = %v", got)
	}
}

func TestTheTimelineStartsOnceWhenTheShipmentIsCreated(t *testing.T) {
	in, _ := newIntake()
	tl := timeline.NewMemory()
	in.Timeline = tl
	for _, id := range []string{"evt-1", "evt-1", "evt-2"} {
		if err := in.Handle(context.Background(), confirmed(id, "O-1", nil)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := tl.Get(context.Background(), "SHP-O-1")
	if len(got) != 1 || got[0].Type != timeline.TypeCreated || !got[0].At.Equal(at) {
		t.Fatalf("timeline = %+v", got)
	}
}

func TestOtherEventTypesAreIgnored(t *testing.T) {
	in, store := newIntake()
	e := confirmed("evt-1", "O-1", nil)
	e.EventType = events.EventOrderFailed
	if err := in.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByOrder(context.Background(), "O-1"); !errors.Is(err, shipment.ErrNotFound) {
		t.Fatalf("shipment created for %s", e.EventType)
	}
}

func TestInvalidPayloadsAreSkippedNotRetried(t *testing.T) {
	for name, p := range map[string]map[string]any{
		"no warehouse": {"warehouseId": nil}, "no items": {"items": []any{}}, "no user": {"userId": nil},
		"zero quantity": {"items": []any{map[string]any{"skuId": "A", "quantity": float64(0)}}},
	} {
		in, store := newIntake()
		if err := in.Handle(context.Background(), confirmed("evt-1", "O-1", p)); err != nil {
			t.Errorf("%s: err = %v, want nil (permanent problem)", name, err)
		}
		if _, err := store.GetByOrder(context.Background(), "O-1"); !errors.Is(err, shipment.ErrNotFound) {
			t.Errorf("%s: shipment created", name)
		}
	}
}

type failingStore struct{ shipment.Store }

func (failingStore) Create(context.Context, shipment.Shipment, string, ...events.DomainEvent) (shipment.Shipment, bool, error) {
	return shipment.Shipment{}, false, dependency.Wrap(errors.New("down"))
}

func TestStoreFailuresAreReturnedSoTheEventIsRetried(t *testing.T) {
	in := &Intake{Store: failingStore{}, Now: func() time.Time { return at }}
	if err := in.Handle(context.Background(), confirmed("evt-1", "O-1", nil)); err == nil {
		t.Fatal("store failure was swallowed")
	}
}
