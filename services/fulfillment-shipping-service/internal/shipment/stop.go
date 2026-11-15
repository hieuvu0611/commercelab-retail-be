package shipment

import "time"

// StopOutcome is the result of asking a shipment to stop because its order was cancelled.
type StopOutcome string

const (
	// StopStopped: nothing was picked or the parcel is already stopped; it can never be handed over.
	StopStopped StopOutcome = "STOPPED"
	// StopInProgress: the parcel is being picked or packed (a person must confirm the stop).
	StopInProgress StopOutcome = "IN_PROGRESS"
	// StopTooLate: the parcel was handed to the carrier or is beyond.
	StopTooLate StopOutcome = "TOO_LATE"
)

// StopForCancel stops a parcel that nobody started to pick (or whose pick was blocked by a shortage). A stopped
// parcel stays stopped; one that is being picked or packed is not changed here; one that left the warehouse
// cannot be stopped. The receiver is not changed.
func (s Shipment) StopForCancel(now time.Time) (Shipment, StopOutcome) {
	switch s.Status {
	case StatusStoppedForCancel:
		return s, StopStopped
	case StatusPendingPick, StatusPickBlocked:
		return s.with(StatusStoppedForCancel, append([]Item(nil), s.Items...), now), StopStopped
	case StatusPicking, StatusPacked:
		return s, StopInProgress
	}
	return s, StopTooLate
}
