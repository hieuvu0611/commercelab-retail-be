package shipment

import (
	"errors"
	"time"
)

// StopOutcome is the result of asking a shipment to stop because its order was cancelled.
type StopOutcome string

const (
	// StopStopped: nothing was picked or the parcel is already stopped; it can never be handed over.
	StopStopped StopOutcome = "STOPPED"
	// StopRequested: the parcel is being picked or packed; the warehouse must confirm the stop (SC06).
	StopRequested StopOutcome = "STOP_REQUESTED"
	// StopTooLate: the parcel was handed to the carrier or is beyond.
	StopTooLate StopOutcome = "TOO_LATE"
)

var (
	// ErrNoStopRequest means the shipment has no cancellation asking the warehouse to stop.
	ErrNoStopRequest = errors.New("no cancellation is waiting for this shipment to stop")
	// ErrAlreadyHandedOver means the parcel left the warehouse first, so it can no longer be stopped.
	ErrAlreadyHandedOver = errors.New("the parcel was already handed over")
)

// StopForCancel handles the stop request of a cancelled order. A parcel nobody started to pick (or whose pick
// was blocked by a shortage) is stopped at once. A parcel being picked or packed records the request and waits
// for the warehouse (ConfirmStop) or the handover, whichever the version lock lets in first. A repeated request
// changes nothing; a parcel that left the warehouse cannot be stopped. The receiver is not changed.
func (s Shipment) StopForCancel(now time.Time) (Shipment, StopOutcome) {
	switch s.Status {
	case StatusStoppedForCancel:
		return s, StopStopped
	case StatusPendingPick, StatusPickBlocked:
		return s.with(StatusStoppedForCancel, append([]Item(nil), s.Items...), now), StopStopped
	case StatusPicking, StatusPacked:
		if s.StopRequestedAt != nil {
			return s, StopRequested
		}
		at := now.UTC()
		next := s.with(s.Status, append([]Item(nil), s.Items...), now)
		next.StopRequestedAt = &at
		return next, StopRequested
	}
	return s, StopTooLate
}

// ConfirmStop is the warehouse's confirmation that the parcel is stopped: it can never be handed over after it.
// It needs a pending request and a parcel that did not leave. Confirming a stopped parcel again changes nothing.
func (s Shipment) ConfirmStop(now time.Time) (Shipment, error) {
	switch s.Status {
	case StatusStoppedForCancel:
		return s, nil
	case StatusHandedOver, StatusInTransit, StatusDelivered, StatusDeliveryFailed, StatusPartiallyDelivered:
		return Shipment{}, ErrAlreadyHandedOver
	}
	if s.StopRequestedAt == nil {
		return Shipment{}, ErrNoStopRequest
	}
	return s.with(StatusStoppedForCancel, append([]Item(nil), s.Items...), now), nil
}

// PrepareSplitStop records readiness without stopping the parcel or changing committed stock.
func (s Shipment) PrepareSplitStop(now time.Time, confirm bool) Shipment {
	pending := s.Status == StatusPendingPick || s.Status == StatusPickBlocked
	if s.StopRequestedAt != nil && (s.StopConfirmedAt != nil || (!pending && !confirm)) {
		return s
	}
	at := now.UTC()
	next := s.with(s.Status, s.Items, now)
	if next.StopRequestedAt == nil {
		next.StopRequestedAt = &at
	}
	if pending || confirm {
		next.StopConfirmedAt = &at
	}
	return next
}

func (s Shipment) ClearSplitStop(now time.Time) Shipment {
	if s.StopRequestedAt == nil && s.StopConfirmedAt == nil {
		return s
	}
	next := s.with(s.Status, s.Items, now)
	next.StopRequestedAt, next.StopConfirmedAt = nil, nil
	return next
}
