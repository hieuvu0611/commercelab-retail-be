package eventbus

import (
	"context"
	"sync"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// HandlerFunc processes a published DomainEvent.
type HandlerFunc func(ctx context.Context, ev events.DomainEvent) error

// Bus provides an event dispatcher and outbox recorder for inter-service domain events.
type Bus struct {
	mu        sync.RWMutex
	handlers  map[string][]HandlerFunc
	published []events.DomainEvent
}

func New() *Bus {
	return &Bus{
		handlers: make(map[string][]HandlerFunc),
	}
}

func (b *Bus) Subscribe(eventType string, fn HandlerFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], fn)
}

func (b *Bus) Publish(ctx context.Context, eventID, eventType, aggregateType, aggregateID string, payload map[string]any) error {
	ev := events.DomainEvent{
		EventID:       eventID,
		EventType:     eventType,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		OccurredAt:    time.Now().UTC(),
		Payload:       payload,
	}
	b.mu.Lock()
	b.published = append(b.published, ev)
	subs := append([]HandlerFunc(nil), b.handlers[eventType]...)
	b.mu.Unlock()

	for _, fn := range subs {
		if err := fn(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bus) History() []events.DomainEvent {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cp := make([]events.DomainEvent, len(b.published))
	copy(cp, b.published)
	return cp
}
