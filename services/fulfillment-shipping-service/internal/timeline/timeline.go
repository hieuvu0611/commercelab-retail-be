// Package timeline keeps the history of a shipment in MongoDB (collection fulfillment_timelines), one document
// per shipment with its entries appended in order. It is a read model: Postgres stays the truth for state.
package timeline

import (
	"context"
	"sync"
	"time"
)

// Entry types.
const (
	TypeCreated            = "CREATED"
	TypePickingStarted     = "PICKING_STARTED"
	TypePickBlocked        = "PICK_BLOCKED"
	TypePacked             = "PACKED"
	TypeHandedOver         = "HANDED_OVER"
	TypeInTransit          = "IN_TRANSIT"
	TypeDelivered          = "DELIVERED"
	TypeDeliveryFailed     = "DELIVERY_FAILED"
	TypePartiallyDelivered = "PARTIALLY_DELIVERED"
)

// Entry is one thing that happened to a shipment. Detail holds an internal note (a reason); customers see only
// what the tracking view chooses to show.
type Entry struct {
	At     time.Time `json:"at" bson:"at"`
	Type   string    `json:"type" bson:"type"`
	Detail string    `json:"detail,omitempty" bson:"detail,omitempty"`
}

// Recorder stores and reads timelines. Implementations must be safe for concurrent use.
type Recorder interface {
	// Append adds an entry at the end of the shipment's timeline (creating it when needed).
	Append(ctx context.Context, shipmentID, orderID string, e Entry) error
	// Get returns the entries in the order they were appended (empty for an unknown shipment).
	Get(ctx context.Context, shipmentID string) ([]Entry, error)
}

// Memory is the Recorder for development and tests without MongoDB.
type Memory struct {
	mu      sync.Mutex
	entries map[string][]Entry
}

func NewMemory() *Memory { return &Memory{entries: map[string][]Entry{}} }

func (m *Memory) Append(_ context.Context, shipmentID, _ string, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[shipmentID] = append(m.entries[shipmentID], e)
	return nil
}

func (m *Memory) Get(_ context.Context, shipmentID string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Entry{}, m.entries[shipmentID]...), nil
}
