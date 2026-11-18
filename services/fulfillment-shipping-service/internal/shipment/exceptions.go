package shipment

import (
	"strings"
	"time"
)

func validReason(reason string) (string, bool) {
	reason = strings.TrimSpace(reason)
	return reason, reason != "" && len(reason) <= MaxFailureReason
}

// ReportShortage records that quantity units of skuID cannot be found on the shelf. The parcel is blocked: it
// can no longer be packed or handed over, so dispatch is never reported for it.
func (s Shipment) ReportShortage(skuID string, quantity int64, reason string, now time.Time) (Shipment, error) {
	if s.StopConfirmedAt != nil || (s.Status != StatusPendingPick && s.Status != StatusPicking) {
		return Shipment{}, ErrNotPickable
	}
	reason, ok := validReason(reason)
	if !ok {
		return Shipment{}, ErrInvalid
	}
	for _, it := range s.Items {
		if it.SkuID != skuID {
			continue
		}
		if quantity <= 0 {
			return Shipment{}, ErrInvalidQuantity
		}
		if quantity > it.Quantity-it.PickedQuantity {
			return Shipment{}, ErrQuantityExceedsOrder
		}
		next := s.with(StatusPickBlocked, append([]Item(nil), s.Items...), now)
		next.FailureReason = reason
		return next, nil
	}
	return Shipment{}, ErrSkuNotOnPickList
}

// MarkPartiallyDelivered records the units the carrier delivered when it could not deliver everything. The
// parcel is not complete and the return window does not start; a later full delivery completes it.
func (s Shipment) MarkPartiallyDelivered(delivered []Item, now time.Time) (Shipment, error) {
	if s.Status == StatusPartiallyDelivered {
		return s, nil
	}
	if s.Status != StatusHandedOver && s.Status != StatusInTransit && s.Status != StatusDeliveryFailed {
		if s.dispatched() {
			return s, nil // already complete
		}
		return Shipment{}, ErrNotDispatched
	}
	items := append([]Item(nil), s.Items...)
	var units, total int64
	for _, d := range delivered {
		found := false
		for i := range items {
			if items[i].SkuID != d.SkuID {
				continue
			}
			if d.Quantity < 0 || d.Quantity > items[i].PickedQuantity {
				return Shipment{}, ErrQuantityExceedsOrder
			}
			items[i].DeliveredQuantity, found = d.Quantity, true
		}
		if !found {
			return Shipment{}, ErrSkuNotOnPickList
		}
	}
	for _, it := range items {
		units, total = units+it.DeliveredQuantity, total+it.PickedQuantity
	}
	if units == 0 || units >= total {
		return Shipment{}, ErrInvalidQuantity // nothing delivered is a failure, everything is a delivery
	}
	return s.with(StatusPartiallyDelivered, items, now), nil
}
