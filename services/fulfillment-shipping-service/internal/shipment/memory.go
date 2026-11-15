package shipment

import (
	"context"
	"sync"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// MemoryStore is the Store for development and tests without Postgres.
type MemoryStore struct {
	mu        sync.Mutex
	byID      map[string]Shipment
	processed map[string]bool
	events    map[string][]events.DomainEvent
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[string]Shipment{}, processed: map[string]bool{}, events: map[string][]events.DomainEvent{}}
}

func clone(s Shipment) Shipment {
	s.Items = append([]Item(nil), s.Items...)
	return s
}

func (m *MemoryStore) Create(_ context.Context, s Shipment, eventID string, ev ...events.DomainEvent) (Shipment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.byID[s.ShipmentID]; ok {
		m.processed[eventID] = true
		return clone(existing), false, nil
	}
	m.processed[eventID] = true
	m.byID[s.ShipmentID] = clone(s)
	m.events[s.ShipmentID] = append(m.events[s.ShipmentID], ev...)
	return clone(s), true, nil
}

func (m *MemoryStore) Get(_ context.Context, shipmentID string) (Shipment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[shipmentID]
	if !ok {
		return Shipment{}, ErrNotFound
	}
	return clone(s), nil
}

func (m *MemoryStore) GetByOrder(ctx context.Context, orderID string) (Shipment, error) {
	return m.Get(ctx, ID(orderID))
}

// EventTypes lists the event types stored for a shipment (tests).
func (m *MemoryStore) EventTypes(shipmentID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.events[shipmentID] {
		out = append(out, e.EventType)
	}
	return out
}
