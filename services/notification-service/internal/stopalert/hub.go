// Package stopalert pushes the urgent stop-packing alert of a cancellation to the warehouse screens (SC06). It
// listens to shipment events, keeps the open alerts of every warehouse and fans each change out to the screens
// subscribed to that warehouse only. Alerts carry ids and a fixed message, never personal data.
package stopalert

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// bufferSize is how many messages a screen may fall behind before it is dropped (it reconnects and receives the
// open alerts again).
const bufferSize = 32

// seenCapacity bounds the event ids remembered to drop redeliveries.
const seenCapacity = 2048

// Alert is a stop request the warehouse has not resolved yet.
type Alert struct {
	Type        string `json:"type"` // WAREHOUSE_STOP_SIGNAL
	ShipmentID  string `json:"shipmentId"`
	OrderID     string `json:"orderId"`
	WarehouseID string `json:"warehouseId"`
	RequestedAt string `json:"requestedAt"`
	Message     string `json:"message"`
}

// Resolution tells the screens an alert is closed: the parcel was stopped, or handed over first (SC06).
type Resolution struct {
	Type        string `json:"type"` // WAREHOUSE_STOP_RESOLVED
	ShipmentID  string `json:"shipmentId"`
	OrderID     string `json:"orderId"`
	WarehouseID string `json:"warehouseId"`
	Outcome     string `json:"outcome"` // STOPPED or HANDED_OVER
	ResolvedAt  string `json:"resolvedAt"`
}

// Message is one server-sent event: Event is the SSE event name, ID its id, Data its JSON payload.
type Message struct {
	Event string
	ID    string
	Data  any
}

// Subscription is one connected screen. Initial holds the alerts that were open when it connected; C then
// delivers changes and is closed when the screen is dropped for being too slow.
type Subscription struct {
	C         <-chan Message
	Initial   []Message
	hub       *Hub
	ch        chan Message
	warehouse string
}

// Close ends the subscription. It is safe to call more than once.
func (s *Subscription) Close() { s.hub.unsubscribe(s) }

type openAlert struct {
	alert Alert
	id    string
}

// Hub fans alerts out per warehouse. It is safe for concurrent use.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*Subscription]struct{}
	open map[string]map[string]openAlert // warehouse -> shipment -> alert
	seen map[string]bool
	ring []string
	now  func() time.Time
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[*Subscription]struct{}{}, open: map[string]map[string]openAlert{}, seen: map[string]bool{},
		now: func() time.Time { return time.Now().UTC() }}
}

// Subscribe registers a screen of a warehouse.
func (h *Hub) Subscribe(warehouseID string) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Message, bufferSize)
	sub := &Subscription{C: ch, hub: h, ch: ch, warehouse: warehouseID}
	if h.subs[warehouseID] == nil {
		h.subs[warehouseID] = map[*Subscription]struct{}{}
	}
	h.subs[warehouseID][sub] = struct{}{}
	for _, o := range h.open[warehouseID] {
		sub.Initial = append(sub.Initial, Message{Event: "stop_requested", ID: o.id, Data: o.alert})
	}
	return sub
}

// Subscribers is the number of connected screens of a warehouse (tests, metrics).
func (h *Hub) Subscribers(warehouseID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[warehouseID])
}

func (h *Hub) unsubscribe(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drop(s)
}

// drop removes and closes a subscription. The caller holds the lock.
func (h *Hub) drop(s *Subscription) {
	if _, ok := h.subs[s.warehouse][s]; !ok {
		return
	}
	delete(h.subs[s.warehouse], s)
	close(s.ch)
}

// remember reports whether the event id is new. The caller holds the lock.
func (h *Hub) remember(id string) bool {
	if h.seen[id] {
		return false
	}
	h.seen[id] = true
	h.ring = append(h.ring, id)
	if len(h.ring) > seenCapacity {
		delete(h.seen, h.ring[0])
		h.ring = h.ring[1:]
	}
	return true
}

// broadcast sends m to every screen of the warehouse; a screen that cannot keep up is dropped. The caller holds the lock.
func (h *Hub) broadcast(warehouseID string, m Message) {
	for s := range h.subs[warehouseID] {
		select {
		case s.ch <- m:
		default:
			h.drop(s)
		}
	}
}

// HandleEvent implements eventconsumer.Handler for the shipment topic. Events it does not care about, or that
// cannot be read, are skipped (returning an error would only block the stream); it never fails.
func (h *Hub) HandleEvent(_ context.Context, e events.DomainEvent) error {
	shipmentID, _ := e.Payload["shipmentId"].(string)
	orderID, _ := e.Payload["orderId"].(string)
	warehouseID, _ := e.Payload["warehouseId"].(string)
	switch e.EventType {
	case events.EventShipmentStopRequested, events.EventShipmentStopped, events.EventShipmentHandedOver:
	default:
		return nil
	}
	if shipmentID == "" || warehouseID == "" {
		log.Printf("[notification] skipping %s: no shipment or warehouse", e.EventType)
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.remember(e.EventID) {
		return nil
	}
	if e.EventType == events.EventShipmentStopRequested {
		if _, exists := h.open[warehouseID][shipmentID]; exists {
			return nil // one open alert per parcel
		}
		requestedAt, _ := e.Payload["requestedAt"].(string)
		if requestedAt == "" {
			requestedAt = h.now().Format(time.RFC3339)
		}
		alert := Alert{Type: "WAREHOUSE_STOP_SIGNAL", ShipmentID: shipmentID, OrderID: orderID, WarehouseID: warehouseID, RequestedAt: requestedAt,
			Message: "The customer cancelled order " + orderID + ". Stop packing shipment " + shipmentID + " and confirm the stop before handover."}
		if h.open[warehouseID] == nil {
			h.open[warehouseID] = map[string]openAlert{}
		}
		h.open[warehouseID][shipmentID] = openAlert{alert: alert, id: e.EventID}
		h.broadcast(warehouseID, Message{Event: "stop_requested", ID: e.EventID, Data: alert})
		return nil
	}
	if _, exists := h.open[warehouseID][shipmentID]; !exists {
		return nil // the parcel was stopped before anybody had to act, or the alert is already closed
	}
	delete(h.open[warehouseID], shipmentID)
	outcome := "STOPPED"
	if e.EventType == events.EventShipmentHandedOver {
		outcome = "HANDED_OVER"
	}
	h.broadcast(warehouseID, Message{Event: "stop_resolved", ID: e.EventID, Data: Resolution{Type: "WAREHOUSE_STOP_RESOLVED", ShipmentID: shipmentID,
		OrderID: orderID, WarehouseID: warehouseID, Outcome: outcome, ResolvedAt: h.now().Format(time.RFC3339)}})
	return nil
}
