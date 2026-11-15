package outbox

import (
	"testing"
	"time"
)

func TestNewEventHasAnIdTheTimeAndNoSharedState(t *testing.T) {
	at := time.Date(2026, 11, 15, 17, 0, 0, 0, time.FixedZone("x", 7*3600))
	a := NewEvent("order.failed.v1", "Order", "O-1", at, map[string]any{"orderId": "O-1"})
	b := NewEvent("order.failed.v1", "Order", "O-1", at, nil)
	if a.EventID == b.EventID || a.EventID == "" || a.EventType != "order.failed.v1" || a.AggregateID != "O-1" || !a.OccurredAt.Equal(at) || a.OccurredAt.Location() != time.UTC {
		t.Fatalf("events = %+v %+v", a, b)
	}
}

func TestTopicForUsesOneTopicPerAggregate(t *testing.T) {
	if got := TopicFor("Order"); got != "commercelab.order.events" {
		t.Fatalf("TopicFor(Order) = %q", got)
	}
	if TopicFor("Payment") == TopicFor("Order") {
		t.Fatal("aggregates must not share a topic")
	}
}
