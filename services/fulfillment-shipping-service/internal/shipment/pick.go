package shipment

import (
	"errors"
	"time"
)

var (
	// ErrSkuNotOnPickList means the scanned SKU is not a line of the shipment.
	ErrSkuNotOnPickList = errors.New("the scanned SKU is not on this pick list")
	// ErrQuantityExceedsOrder means more units were scanned than the order asks for.
	ErrQuantityExceedsOrder = errors.New("the scanned quantity exceeds the ordered quantity")
	// ErrInvalidQuantity means the scanned quantity is not a positive number.
	ErrInvalidQuantity = errors.New("the scanned quantity must be positive")
	// ErrNotPickable means the shipment is no longer being picked (packed, handed over or stopped).
	ErrNotPickable = errors.New("the shipment is not being picked")
	// ErrPickIncomplete means a line is not fully picked, so the parcel cannot be packed.
	ErrPickIncomplete = errors.New("not every line is fully picked")
)

func (s Shipment) with(status Status, items []Item, now time.Time) Shipment {
	s.Status, s.Items, s.Version, s.UpdatedAt = status, items, s.Version+1, now.UTC()
	return s
}

// Scan records quantity units of skuID picked from the shelf. It verifies the SKU and never lets a line go
// past the ordered quantity. The receiver is not changed.
func (s Shipment) Scan(skuID string, quantity int64, now time.Time) (Shipment, error) {
	if s.Status != StatusPendingPick && s.Status != StatusPicking {
		return Shipment{}, ErrNotPickable
	}
	if quantity <= 0 {
		return Shipment{}, ErrInvalidQuantity
	}
	items := append([]Item(nil), s.Items...)
	for i := range items {
		if items[i].SkuID != skuID {
			continue
		}
		if items[i].PickedQuantity+quantity > items[i].Quantity {
			return Shipment{}, ErrQuantityExceedsOrder
		}
		items[i].PickedQuantity += quantity
		return s.with(StatusPicking, items, now), nil
	}
	return Shipment{}, ErrSkuNotOnPickList
}

// Pack closes the pick: every line must be fully picked. Packing a packed parcel changes nothing.
func (s Shipment) Pack(now time.Time) (Shipment, error) {
	if s.Status == StatusPacked {
		return s, nil
	}
	if s.Status != StatusPendingPick && s.Status != StatusPicking {
		return Shipment{}, ErrNotPickable
	}
	for _, it := range s.Items {
		if it.PickedQuantity != it.Quantity {
			return Shipment{}, ErrPickIncomplete
		}
	}
	return s.with(StatusPacked, append([]Item(nil), s.Items...), now), nil
}

// ErrHandoverNotAllowed means the parcel is not packed (or was stopped), so it cannot go to the carrier.
var ErrHandoverNotAllowed = errors.New("only a packed shipment can be handed over")

// Handover records that the carrier took the parcel. A repeated handover changes nothing.
func (s Shipment) Handover(now time.Time) (Shipment, error) {
	if s.Status == StatusHandedOver {
		return s, nil
	}
	if s.Status != StatusPacked {
		return Shipment{}, ErrHandoverNotAllowed
	}
	at := now.UTC()
	next := s.with(StatusHandedOver, append([]Item(nil), s.Items...), now)
	next.HandedOverAt = &at
	return next, nil
}
