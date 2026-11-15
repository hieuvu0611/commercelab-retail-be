package shipment

import (
	"context"
	"slices"
	"sort"
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

func (m *MemoryStore) List(_ context.Context, f Filter) ([]Shipment, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Shipment
	for _, s := range m.byID {
		if f.Status != "" && s.Status != f.Status {
			continue
		}
		if !f.AllWarehouses && !slices.Contains(f.Warehouses, s.WarehouseID) {
			continue
		}
		all = append(all, clone(s))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ShipmentID < all[j].ShipmentID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	total := len(all)
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	from := min(max(f.Offset, 0), total)
	return all[from:min(from+limit, total)], total, nil
}

func (m *MemoryStore) Save(_ context.Context, s Shipment, expectedVersion int64, ev ...events.DomainEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.byID[s.ShipmentID]
	if !ok {
		return ErrNotFound
	}
	if cur.Version != expectedVersion {
		return ErrStaleVersion
	}
	m.byID[s.ShipmentID] = clone(s)
	m.events[s.ShipmentID] = append(m.events[s.ShipmentID], ev...)
	return nil
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
