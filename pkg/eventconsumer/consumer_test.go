package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

func TestDecodeReadsADomainEvent(t *testing.T) {
	e, err := decode([]byte(`{"eventId":"evt-1","eventType":"order.confirmed.v1","aggregateId":"O-1","payload":{"orderId":"O-1"}}`))
	if err != nil || e.EventID != "evt-1" || e.Payload["orderId"] != "O-1" {
		t.Fatalf("decode = %+v, %v", e, err)
	}
}

func TestDecodeRejectsMalformedAndIdlessEvents(t *testing.T) {
	for _, raw := range []string{`not json`, `{"eventType":"x"}`, `{"eventId":"e"}`} {
		if _, err := decode([]byte(raw)); err == nil {
			t.Errorf("decode(%q) accepted a bad event", raw)
		}
	}
}

func TestHandleRetriesUntilTheHandlerSucceeds(t *testing.T) {
	calls := 0
	h := func(context.Context, events.DomainEvent) error {
		calls++
		if calls < 3 {
			return errors.New("store down")
		}
		return nil
	}
	if !handleWithRetry(context.Background(), h, events.DomainEvent{EventID: "e", EventType: "t"}, time.Millisecond) || calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestHandleStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := func(context.Context, events.DomainEvent) error { return errors.New("down") }
	if handleWithRetry(ctx, h, events.DomainEvent{EventID: "e", EventType: "t"}, time.Millisecond) {
		t.Fatal("reported success after cancel")
	}
}
