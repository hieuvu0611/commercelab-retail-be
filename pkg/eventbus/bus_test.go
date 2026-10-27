package eventbus

import (
	"context"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

func TestPublishAndSubscribeDomainEvent(t *testing.T) {
	bus := New()
	var received events.DomainEvent

	bus.Subscribe(events.EventOrderCreated, func(ctx context.Context, ev events.DomainEvent) error {
		received = ev
		return nil
	})

	err := bus.Publish(context.Background(), "evt-001", events.EventOrderCreated, "ORDER", "O-001", map[string]any{
		"totalPayableVnd": int64(660000),
	})
	if err != nil {
		t.Fatalf("unexpected publish error: %v", err)
	}
	if received.EventID != "evt-001" || received.AggregateID != "O-001" {
		t.Fatalf("unexpected received event: %+v", received)
	}
	if len(bus.History()) != 1 {
		t.Fatalf("expected 1 event in history, got %d", len(bus.History()))
	}
}
