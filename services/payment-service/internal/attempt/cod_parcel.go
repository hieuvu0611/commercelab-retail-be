package attempt

import "time"

// CODParcel identifies one physical shipment's terminal cash outcome.
type CODParcel struct {
	ShipmentID   string
	TotalParcels int
	Collected    bool
	AmountVND    int64
}

func (p CODParcel) valid() bool {
	return p.ShipmentID != "" && p.TotalParcels >= 1 && p.TotalParcels <= 2 && p.AmountVND >= 0 && (p.Collected || p.AmountVND == 0)
}

func applyCODParcel(a Attempt, parcel CODParcel, previous *CODParcel, recorded int, now time.Time) (Attempt, Change, error) {
	if !parcel.valid() || a.PaymentMethod != "COD" {
		return a, Change{}, ErrInvalidOutcome
	}
	if previous != nil {
		if *previous == parcel || (previous.Collected && !parcel.Collected) {
			return a, Change{Replay: true}, nil
		}
		if previous.Collected || previous.TotalParcels != parcel.TotalParcels {
			return a, Change{}, ErrIdempotencyConflict
		}
	} else if a.Status != StatusCODPending {
		if a.Status == StatusCOLLECTED && a.CapturedAmountVND == a.AmountVND {
			return a, Change{Replay: true}, nil
		}
		return a, Change{}, ErrFinalOutcome
	}

	if recorded > parcel.TotalParcels || parcel.AmountVND > a.AmountVND-a.CapturedAmountVND {
		return a, Change{}, ErrInvalidOutcome
	}
	a.CapturedAmountVND += parcel.AmountVND
	if recorded == parcel.TotalParcels || a.CapturedAmountVND == a.AmountVND {
		a.Status, a.OutcomeAt = StatusCOLLECTED, &now
		if a.CapturedAmountVND == 0 {
			a.Status = StatusCODCancelled
		}
	}
	return a, Change{CapturedVND: parcel.AmountVND}, nil
}
