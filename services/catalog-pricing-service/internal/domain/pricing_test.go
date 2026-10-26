package domain

import (
	"testing"
	"time"
)

func TestCalculateQuote_SC01AndSC07(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	inputs := []QuoteLineInput{
		{SkuID: "SKU-BALO-BLACK-20L", SkuName: "Balo City Đen 20L", Quantity: 2, UnitPriceVND: 300000},
		{SkuID: "SKU-BOTTLE-BLUE-500ML", SkuName: "Bình Nước Xanh 500ml", Quantity: 1, UnitPriceVND: 100000},
	}

	q, err := CalculateQuote("Q-001", "user-lan", "WELCOME10", 10, inputs, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SC01: 600.000 + 100.000 - 70.000 + 30.000 = 660.000
	if q.SubtotalVND != 700000 {
		t.Fatalf("expected subtotal 700000, got %d", q.SubtotalVND)
	}
	if q.DiscountVND != 70000 {
		t.Fatalf("expected discount 70000, got %d", q.DiscountVND)
	}
	if q.ShippingFeeVND != 30000 {
		t.Fatalf("expected shipping 30000, got %d", q.ShippingFeeVND)
	}
	if q.TotalPayableVND != 660000 {
		t.Fatalf("expected total 660000, got %d", q.TotalPayableVND)
	}

	// SC07: Mỗi chiếc Balo thực trả 270.000đ, Bình nước thực trả 90.000đ
	if q.Lines[0].AllocatedUnitPaidVND != 270000 {
		t.Fatalf("expected balo unit paid 270000, got %d", q.Lines[0].AllocatedUnitPaidVND)
	}
	if q.Lines[1].AllocatedUnitPaidVND != 90000 {
		t.Fatalf("expected bottle unit paid 90000, got %d", q.Lines[1].AllocatedUnitPaidVND)
	}
}
