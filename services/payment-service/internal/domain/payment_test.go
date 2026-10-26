package domain

import "testing"

func TestRefundCeiling_SC08(t *testing.T) {
	eng := NewPaymentEngine()

	// Thực thu 660.000. Hoàn lần 1: 270.000 (SC07 trả 1 Balo)
	if _, err := eng.ReserveRefundCeiling("O-001", 270000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := eng.CompleteRefund("O-001", 270000); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Khoản hoàn 2: 300.000 đang xử lý (chưa có kết quả cuối)
	sum, err := eng.ReserveRefundCeiling("O-001", 300000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.RemainingRefundable() != 90000 {
		t.Fatalf("expected remaining ceiling 90000, got %d", sum.RemainingRefundable())
	}

	// Nhân viên khác bấm hoàn thêm 100.000 -> bắt buộc bị chặn (SC08)
	if _, err := eng.ReserveRefundCeiling("O-001", 100000); err != ErrRefundCeilingExceeded {
		t.Fatalf("expected ErrRefundCeilingExceeded, got %v", err)
	}
}
