package shipment

import (
	"errors"
	"testing"
)

func TestStopForCancelByStatus(t *testing.T) {
	tests := []struct {
		status      Status
		wantStatus  Status
		wantOutcome StopOutcome
		wantBump    bool
		wantRequest bool
	}{
		{StatusPendingPick, StatusStoppedForCancel, StopStopped, true, false},
		{StatusPickBlocked, StatusStoppedForCancel, StopStopped, true, false},
		{StatusStoppedForCancel, StatusStoppedForCancel, StopStopped, false, false},
		{StatusPicking, StatusPicking, StopRequested, true, true},
		{StatusPacked, StatusPacked, StopRequested, true, true},
		{StatusHandedOver, StatusHandedOver, StopTooLate, false, false},
		{StatusInTransit, StatusInTransit, StopTooLate, false, false},
		{StatusDelivered, StatusDelivered, StopTooLate, false, false},
		{StatusDeliveryFailed, StatusDeliveryFailed, StopTooLate, false, false},
		{StatusPartiallyDelivered, StatusPartiallyDelivered, StopTooLate, false, false},
	}
	for _, tc := range tests {
		s := Shipment{ShipmentID: "SHP-O-1", Status: tc.status, Version: 4, Items: []Item{{SkuID: "A", Quantity: 1}}}
		next, outcome := s.StopForCancel(now)
		if next.Status != tc.wantStatus || outcome != tc.wantOutcome || (next.Version == 5) != tc.wantBump || (next.StopRequestedAt != nil) != tc.wantRequest {
			t.Errorf("%s: %s %s v%d request=%v, want %s %s bump=%v request=%v", tc.status, next.Status, outcome, next.Version, next.StopRequestedAt != nil,
				tc.wantStatus, tc.wantOutcome, tc.wantBump, tc.wantRequest)
		}
	}
}

func TestARepeatedStopRequestChangesNothing(t *testing.T) {
	s := Shipment{ShipmentID: "SHP-O-1", Status: StatusPacked, Version: 4, Items: []Item{{SkuID: "A", Quantity: 1, PickedQuantity: 1}}}
	first, _ := s.StopForCancel(now)
	second, outcome := first.StopForCancel(now.Add(1))
	if outcome != StopRequested || second.Version != first.Version || !second.StopRequestedAt.Equal(*first.StopRequestedAt) {
		t.Fatalf("second request = %+v %s, want the first one untouched", second, outcome)
	}
}

func TestAStoppedShipmentCanNeverBePickedPackedOrHandedOver(t *testing.T) {
	s := Shipment{ShipmentID: "SHP-O-1", Status: StatusStoppedForCancel, Version: 2, Items: []Item{{SkuID: "A", Quantity: 1}}}
	if _, err := s.Scan("A", 1, now); err != ErrNotPickable {
		t.Errorf("scan = %v", err)
	}
	if _, err := s.Pack(now); err != ErrNotPickable {
		t.Errorf("pack = %v", err)
	}
	if _, err := s.Handover(now); err != ErrHandoverNotAllowed {
		t.Errorf("handover = %v", err)
	}
}

func TestConfirmStopNeedsARequestAndStopsABusyParcel(t *testing.T) {
	for _, status := range []Status{StatusPicking, StatusPacked, StatusPickBlocked} {
		s := Shipment{ShipmentID: "SHP-O-1", Status: status, Version: 3, StopRequestedAt: &now, Items: []Item{{SkuID: "A", Quantity: 1}}}
		next, err := s.ConfirmStop(now)
		if err != nil || next.Status != StatusStoppedForCancel || next.Version != 4 {
			t.Errorf("%s: %+v %v", status, next, err)
		}
		if again, err := next.ConfirmStop(now); err != nil || again.Version != next.Version {
			t.Errorf("%s: confirming twice = %+v %v, want the same stopped parcel", status, again, err)
		}
	}
	if _, err := (Shipment{Status: StatusPacked}).ConfirmStop(now); !errors.Is(err, ErrNoStopRequest) {
		t.Errorf("no request = %v, want ErrNoStopRequest", err)
	}
	for _, status := range []Status{StatusHandedOver, StatusInTransit, StatusDelivered} {
		if _, err := (Shipment{Status: status, StopRequestedAt: &now}).ConfirmStop(now); !errors.Is(err, ErrAlreadyHandedOver) {
			t.Errorf("%s = %v, want ErrAlreadyHandedOver", status, err)
		}
	}
}

func TestHandoverStillWorksAfterAStopRequestBranchB(t *testing.T) {
	s := Shipment{ShipmentID: "SHP-O-1", Status: StatusPacked, Version: 3, StopRequestedAt: &now, Items: []Item{{SkuID: "A", Quantity: 1, PickedQuantity: 1}}}
	next, err := s.Handover(now)
	if err != nil || next.Status != StatusHandedOver {
		t.Fatalf("handover = %+v %v", next, err)
	}
}
