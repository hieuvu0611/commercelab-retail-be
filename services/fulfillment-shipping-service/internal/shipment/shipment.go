// Package shipment is the fulfillment aggregate: one parcel per order, its pick list and its life from
// "awaiting pick" to delivery (B04). The state rules are pure; a Store persists the result.
package shipment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// Status of a shipment.
type Status string

const (
	StatusPendingPick        Status = "PENDING_PICK"
	StatusPicking            Status = "PICKING"
	StatusPacked             Status = "PACKED"
	StatusHandedOver         Status = "HANDED_OVER"
	StatusInTransit          Status = "IN_TRANSIT"
	StatusDelivered          Status = "DELIVERED"
	StatusDeliveryFailed     Status = "DELIVERY_FAILED"
	StatusReturnToSender     Status = "RETURN_TO_SENDER"
	StatusPartiallyDelivered Status = "PARTIALLY_DELIVERED"
	StatusPickBlocked        Status = "PICK_BLOCKED"
	// StatusStoppedForCancel: the order was cancelled before handover; the parcel can never be handed over (B05, SC06).
	StatusStoppedForCancel Status = "STOPPED_FOR_CANCEL"
)

var (
	// ErrNotFound means no shipment matches.
	ErrNotFound = errors.New("shipment not found")
	// ErrStaleVersion means the shipment changed since it was read, so nothing was stored.
	ErrStaleVersion = errors.New("shipment was changed by someone else")
	// ErrDuplicateEvent means the carrier event id was already applied, so nothing was stored.
	ErrDuplicateEvent = errors.New("carrier event was already applied")
	// ErrInvalid means a shipment cannot be built from the given data.
	ErrInvalid = errors.New("invalid shipment")
)

// Item is one line of the pick list.
type Item struct {
	SkuID             string `json:"skuId"`
	Quantity          int64  `json:"quantity"`
	PickedQuantity    int64  `json:"pickedQuantity"`
	DeliveredQuantity int64  `json:"deliveredQuantity"`
}

// Shipment is one parcel of an order. Version grows with every change (optimistic locking).
type Shipment struct {
	ShipmentID   string     `json:"shipmentId"`
	OrderID      string     `json:"orderId"`
	UserID       string     `json:"-"`
	WarehouseID        string     `json:"warehouseId"`
	ParcelIndex        int        `json:"parcelIndex,omitempty"`
	TotalParcels       int        `json:"totalParcels,omitempty"`
	PaymentMethod      string     `json:"paymentMethod,omitempty"`
	CodAmountToCollect int64      `json:"codAmountToCollect,omitempty"`
	Status             Status     `json:"status"`
	Items        []Item     `json:"items"`
	Version      int64      `json:"version"`
	HandedOverAt *time.Time `json:"handedOverAt,omitempty"`
	DeliveredAt  *time.Time `json:"deliveredAt,omitempty"`
	// StopRequestedAt is when the customer's cancellation asked the warehouse to stop (SC06); nil when nobody did.
	StopRequestedAt *time.Time `json:"stopRequestedAt,omitempty"`
	// ReturnWindowEndsAt is DeliveredAt plus ReturnWindow (P07); nil until delivered.
	ReturnWindowEndsAt *time.Time `json:"returnWindowEndsAt,omitempty"`
	FailureReason      string     `json:"failureReason,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
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
		ParcelIndex: 1, TotalParcels: 1,
		PaymentMethod: "PREPAID", CodAmountToCollect: 0,
		Status: StatusPendingPick, Items: merged, Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}

// NewWithCOD builds a parcel with payment method and COD collection amount specified.
func NewWithCOD(orderID, userID, warehouseID, paymentMethod string, codAmount int64, items []Item, now time.Time) (Shipment, error) {
	return NewParcel(orderID, userID, warehouseID, paymentMethod, codAmount, 1, 1, items, now)
}

// NewParcel builds one parcel of a single-parcel or multi-parcel order.
func NewParcel(orderID, userID, warehouseID, paymentMethod string, codAmount int64, parcelIndex, totalParcels int, items []Item, now time.Time) (Shipment, error) {
	s, err := New(orderID, userID, warehouseID, items, now)
	if err != nil {
		return Shipment{}, err
	}
	if totalParcels <= 1 {
		parcelIndex, totalParcels = 1, 1
	} else {
		if parcelIndex < 1 {
			parcelIndex = 1
		}
		s.ShipmentID = fmt.Sprintf("SHP-%s-%d", orderID, parcelIndex)
	}
	s.ParcelIndex = parcelIndex
	s.TotalParcels = totalParcels
	if paymentMethod == "" {
		paymentMethod = "PREPAID"
	}
	s.PaymentMethod = paymentMethod
	s.CodAmountToCollect = codAmount
	return s, nil
}

func (s Shipment) MarkReturnToSender(reason string, now time.Time) (Shipment, error) {
	if s.Status == StatusReturnToSender {
		return s, nil
	}
	s.Status = StatusReturnToSender
	s.FailureReason = reason
	s.Version++
	s.UpdatedAt = now.UTC()
	return s, nil
}

// ReturnWindow is how long after delivery a customer may request a return.
const ReturnWindow = 7 * 24 * time.Hour

// DefaultLimit is the page size of List when Filter.Limit is 0; MaxLimit caps it.
const (
	DefaultLimit = 50
	MaxLimit     = 100
)

// Filter selects shipments for List. Zero values mean "any"; Limit 0 means 50.
type Filter struct {
	Status        Status
	Warehouses    []string // only these warehouses
	AllWarehouses bool     // ignore Warehouses (a token scoped to ALL)
	Offset, Limit int
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
	// List returns shipments oldest first and the number of matches ignoring paging.
	List(ctx context.Context, f Filter) ([]Shipment, int, error)
	// Save stores s (status, version and line quantities) when the stored version is expectedVersion, together
	// with ev in one step; ErrStaleVersion when it is not, ErrNotFound when the shipment does not exist.
	Save(ctx context.Context, s Shipment, expectedVersion int64, ev ...events.DomainEvent) error
	// SaveCarrierEvent is Save plus recording the carrier's event id in the same step; ErrDuplicateEvent (and
	// nothing stored) when that id was applied before, so a repeated notice never dispatches or delivers twice.
	SaveCarrierEvent(ctx context.Context, s Shipment, expectedVersion int64, carrierEventID string, ev ...events.DomainEvent) error
	// SaveWith is the general form: Save with the carrier event id, an exception case and events, all in one step.
	SaveWith(ctx context.Context, s Shipment, expectedVersion int64, o SaveOptions) error
	// ListCases returns cases oldest first and the number of matches ignoring paging.
	ListCases(ctx context.Context, f CaseFilter) ([]Case, int, error)
}

// MultiParcelStore is an optional extension of Store returning all parcels of an order.
type MultiParcelStore interface {
	ListByOrder(ctx context.Context, orderID string) ([]Shipment, error)
}
