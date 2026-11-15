package shipment

import (
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/events"
)

// Case types and statuses.
const (
	CasePickShortage    = "PICK_SHORTAGE"
	CaseDeliveryFailed  = "DELIVERY_FAILED"
	CasePartialDelivery = "PARTIAL_DELIVERY"
	CaseOpen            = "OPEN"
)

// Case is an exception that Support or Operations must resolve.
type Case struct {
	CaseID      string `json:"caseId"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	ShipmentID  string `json:"shipmentId"`
	OrderID     string `json:"orderId"`
	WarehouseID string `json:"warehouseId"`
	SkuID       string `json:"skuId,omitempty"`
	Quantity    int64  `json:"quantity"`
	Reason      string `json:"reason"`
	// SkuBlocked is true while an open shortage case keeps the SKU off sale in that warehouse.
	SkuBlocked bool      `json:"skuBlocked"`
	CreatedAt  time.Time `json:"createdAt"`
}

// NewCase builds the open case of a shipment exception. The id is derived, so repeating the step opens nothing new.
func NewCase(s Shipment, caseType, skuID string, quantity int64, reason string, now time.Time) Case {
	id := "CASE-" + s.ShipmentID + "-" + caseType
	if skuID != "" {
		id += "-" + skuID
	}
	return Case{CaseID: id, Type: caseType, Status: CaseOpen, ShipmentID: s.ShipmentID, OrderID: s.OrderID, WarehouseID: s.WarehouseID,
		SkuID: skuID, Quantity: quantity, Reason: reason, SkuBlocked: caseType == CasePickShortage, CreatedAt: now.UTC()}
}

// CaseFilter selects cases for ListCases. Zero values mean "any"; Limit 0 means 50.
type CaseFilter struct {
	Status, Type  string
	Warehouses    []string
	AllWarehouses bool
	Offset, Limit int
}

// SaveOptions are the extras stored in the same step as a shipment change.
type SaveOptions struct {
	CarrierEventID string // dedupes a carrier notice (ErrDuplicateEvent)
	Case           *Case  // an exception case to open (a repeat of the same case id is ignored)
	Events         []events.DomainEvent
}
