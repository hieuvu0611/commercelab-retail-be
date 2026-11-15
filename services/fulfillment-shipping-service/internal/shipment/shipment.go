// Package shipment is the fulfillment aggregate: one parcel per order, its pick list and its life from
// "awaiting pick" to delivery (B04). The state rules are pure; a Store persists the result.
package shipment

import (
	"context"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// Status of a shipment.
type Status string

const (
	StatusPendingPick Status = "PENDING_PICK"
)

var (
	// ErrNotFound means no shipment matches.
	ErrNotFound = errors.New("shipment not found")
	// ErrInvalid means a shipment cannot be built from the given data.
	ErrInvalid = errors.New("invalid shipment")
)

// Item is one line of the pick list.
type Item struct {
	SkuID    string `json:"skuId"`
	Quantity int64  `json:"quantity"`
}

// Shipment is one parcel of an order. Version grows with every change (optimistic locking).
type Shipment struct {
	ShipmentID  string    `json:"shipmentId"`
	OrderID     string    `json:"orderId"`
	UserID      string    `json:"-"`
	WarehouseID string    `json:"warehouseId"`
	Status      Status    `json:"status"`
	Items       []Item    `json:"items"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ID is the shipment id of an order: derived, so a repeated event always maps to the same parcel.
func ID(orderID string) string { return "SHP-" + orderID }

// New builds the parcel of a confirmed order. Lines with the same SKU are merged.
func New(orderID, userID, warehouseID string, items []Item, now time.Time) (Shipment, error) {
	if orderID == "" || userID == "" || warehouseID == "" || len(items) == 0 {
		return Shipment{}, ErrInvalid
	}
	var merged []Item
	index := map[string]int{}
	for _, it := range items {
		if it.SkuID == "" || it.Quantity <= 0 {
			return Shipment{}, ErrInvalid
		}
		if i, ok := index[it.SkuID]; ok {
			merged[i].Quantity += it.Quantity
			continue
		}
		index[it.SkuID] = len(merged)
		merged = append(merged, it)
	}
	return Shipment{ShipmentID: ID(orderID), OrderID: orderID, UserID: userID, WarehouseID: warehouseID,
		Status: StatusPendingPick, Items: merged, Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}

// Store persists shipments. Implementations must be safe for concurrent use.
type Store interface {
	// Create stores s, the lines and ev in one step, and records eventID as processed. When the event was already
	// processed, or the order already has a shipment, it stores nothing and returns the existing shipment with
	// created=false, so a repeated or concurrent delivery yields one shipment and one set of events.
	Create(ctx context.Context, s Shipment, eventID string, ev ...events.DomainEvent) (stored Shipment, created bool, err error)
	// Get returns the shipment or ErrNotFound.
	Get(ctx context.Context, shipmentID string) (Shipment, error)
	// GetByOrder returns the shipment of an order or ErrNotFound.
	GetByOrder(ctx context.Context, orderID string) (Shipment, error)
}
