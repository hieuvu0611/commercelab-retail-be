package domain

import "testing"

func TestCancelVsHandoverArbitration_SC06(t *testing.T) {
	// Nhánh A: Kho xác nhận dừng trước khi bàn giao -> Chặn bàn giao
	arbA := NewFulfillmentArbitrator()
	if _, err := arbA.RequestStopForCancel("S-001"); err != nil {
		t.Fatalf("expected stop to succeed, got %v", err)
	}
	if _, err := arbA.ConfirmHandover("S-001"); err != ErrAlreadyStopped {
		t.Fatalf("expected ErrAlreadyStopped when handing over stopped shipment, got %v", err)
	}

	// Nhánh B: Bàn giao có hiệu lực trước -> Từ chối hủy
	arbB := NewFulfillmentArbitrator()
	if _, err := arbB.ConfirmHandover("S-001"); err != nil {
		t.Fatalf("expected handover to succeed, got %v", err)
	}
	if _, err := arbB.RequestStopForCancel("S-001"); err != ErrAlreadyHandedOver {
		t.Fatalf("expected ErrAlreadyHandedOver when stopping handed-over shipment, got %v", err)
	}
}
