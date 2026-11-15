package shipment

import (
	"errors"
	"strings"
	"time"
)

// ErrNotDispatched means a carrier notice names a parcel that was never handed over.
var ErrNotDispatched = errors.New("the shipment was not handed over to the carrier")

// MaxFailureReason is the longest stored delivery failure reason.
const MaxFailureReason = 255

func (s Shipment) dispatched() bool {
	switch s.Status {
	case StatusHandedOver, StatusInTransit, StatusDelivered, StatusDeliveryFailed, StatusPartiallyDelivered:
		return true
	}
	return false
}

// MarkInTransit records that the carrier is moving the parcel. A parcel that is already further along stays as
// it is (late or repeated notice).
func (s Shipment) MarkInTransit(now time.Time) (Shipment, error) {
	switch s.Status {
	case StatusHandedOver:
		return s.with(StatusInTransit, append([]Item(nil), s.Items...), now), nil
	case StatusInTransit, StatusDelivered, StatusDeliveryFailed, StatusPartiallyDelivered:
		return s, nil
	}
	return Shipment{}, ErrNotDispatched
}

// MarkDelivered records delivery of every dispatched unit and starts the seven-day return window. A repeated
// notice keeps the first delivery time.
func (s Shipment) MarkDelivered(now time.Time) (Shipment, error) {
	if s.Status == StatusDelivered {
		return s, nil
	}
	if !s.dispatched() {
		return Shipment{}, ErrNotDispatched
	}
	items := append([]Item(nil), s.Items...)
	for i := range items {
		items[i].DeliveredQuantity = items[i].PickedQuantity
	}
	next := s.with(StatusDelivered, items, now)
	at, ends := now.UTC(), now.UTC().Add(ReturnWindow)
	next.DeliveredAt, next.ReturnWindowEndsAt, next.FailureReason = &at, &ends, ""
	return next, nil
}

// MarkDeliveryFailed records that the carrier could not deliver, with the reason. It does not refund anything
// or put stock back: the parcel is still out of the warehouse.
func (s Shipment) MarkDeliveryFailed(reason string, now time.Time) (Shipment, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > MaxFailureReason {
		return Shipment{}, ErrInvalid
	}
	switch s.Status {
	case StatusDeliveryFailed, StatusDelivered, StatusPartiallyDelivered:
		return s, nil
	case StatusHandedOver, StatusInTransit:
		next := s.with(StatusDeliveryFailed, append([]Item(nil), s.Items...), now)
		next.FailureReason = reason
		return next, nil
	}
	return Shipment{}, ErrNotDispatched
}
