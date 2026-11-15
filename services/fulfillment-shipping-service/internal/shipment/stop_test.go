package shipment

import "testing"

func TestStopForCancelByStatus(t *testing.T) {
	tests := []struct {
		status      Status
		wantStatus  Status
		wantOutcome StopOutcome
		wantBump    bool
	}{
		{StatusPendingPick, StatusStoppedForCancel, StopStopped, true},
		{StatusPickBlocked, StatusStoppedForCancel, StopStopped, true},
		{StatusStoppedForCancel, StatusStoppedForCancel, StopStopped, false},
		{StatusPicking, StatusPicking, StopInProgress, false},
		{StatusPacked, StatusPacked, StopInProgress, false},
		{StatusHandedOver, StatusHandedOver, StopTooLate, false},
		{StatusInTransit, StatusInTransit, StopTooLate, false},
		{StatusDelivered, StatusDelivered, StopTooLate, false},
		{StatusDeliveryFailed, StatusDeliveryFailed, StopTooLate, false},
		{StatusPartiallyDelivered, StatusPartiallyDelivered, StopTooLate, false},
	}
	for _, tc := range tests {
		s := Shipment{ShipmentID: "SHP-O-1", Status: tc.status, Version: 4, Items: []Item{{SkuID: "A", Quantity: 1}}}
		next, outcome := s.StopForCancel(now)
		if next.Status != tc.wantStatus || outcome != tc.wantOutcome || (next.Version == 5) != tc.wantBump {
			t.Errorf("%s: %s %s v%d, want %s %s bump=%v", tc.status, next.Status, outcome, next.Version, tc.wantStatus, tc.wantOutcome, tc.wantBump)
		}
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
