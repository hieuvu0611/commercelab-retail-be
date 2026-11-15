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

func TestQuoteVersionChangesWhenWhatTheCustomerAcceptsChanges(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	lines := func(price int64) []QuoteLineInput {
		return []QuoteLineInput{{SkuID: "SKU-A", SkuName: "A", Quantity: 2, UnitPriceVND: price}}
	}
	base, _ := CalculateQuote("Q-1", "u1", "WELCOME10", 10, lines(300000), now)
	same, _ := CalculateQuote("Q-2", "u1", "WELCOME10", 10, lines(300000), now.Add(time.Minute))
	repriced, _ := CalculateQuote("Q-3", "u1", "WELCOME10", 10, lines(310000), now)
	noVoucher, _ := CalculateQuote("Q-4", "u1", "", 0, lines(300000), now)

	if base.Version == "" || base.Version != same.Version {
		t.Fatalf("identical content must give the same version: %q vs %q", base.Version, same.Version)
	}
	if repriced.Version == base.Version || noVoucher.Version == base.Version {
		t.Fatalf("a price or voucher change must change the version: %q %q %q", base.Version, repriced.Version, noVoucher.Version)
	}
}
