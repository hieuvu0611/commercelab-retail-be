package snapshot

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func sample(orderID string) Snapshot {
	return Snapshot{
		OrderID: orderID, UserID: "user-lan", QuoteID: "Q-1", VoucherCode: "WELCOME10",
		Lines: []Line{
			{SkuID: "SKU-BALO-BLACK-20L", SkuName: "Backpack", Quantity: 2, UnitPriceVND: 300000, LineDiscountVND: 60000, AllocatedUnitPaidVND: 270000},
		},
		SubtotalVND: 700000, DiscountVND: 70000, ShippingFeeVND: 30000, TotalPayableVND: 660000,
		ShippingAddress: Address{Recipient: "Nguyen Van Lan", Phone: "0901234567", Line1: "1 Le Loi", Ward: "Ben Nghe", District: "District 1", Province: "Ho Chi Minh"},
		CreatedAt:       time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC),
	}
}

// runContract is shared with the MongoDB integration test.
func runContract(t *testing.T, store Store, uniq func() string) {
	ctx := context.Background()
	id := uniq()
	if _, found, err := store.Get(ctx, id); err != nil || found {
		t.Fatalf("Get before Save = found=%v err=%v", found, err)
	}
	if err := store.Save(ctx, sample(id)); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get(ctx, id)
	if err != nil || !found || got.Lines[0].AllocatedUnitPaidVND != 270000 || got.ShippingAddress.District != "District 1" ||
		got.TotalPayableVND != 660000 || got.UserID != "user-lan" || !got.CreatedAt.Equal(sample(id).CreatedAt) {
		t.Fatalf("Get = %+v found=%v err=%v", got, found, err)
	}

	// The snapshot is frozen: a second Save (a retry, or a changed catalog) never rewrites it (I04, SC07).
	changed := sample(id)
	changed.Lines[0].AllocatedUnitPaidVND, changed.TotalPayableVND = 1, 1
	if err := store.Save(ctx, changed); err != nil {
		t.Fatalf("second Save = %v", err)
	}
	again, _, _ := store.Get(ctx, id)
	if again.Lines[0].AllocatedUnitPaidVND != 270000 || again.TotalPayableVND != 660000 {
		t.Fatalf("snapshot changed: %+v", again)
	}
}

func TestMemoryStoreContract(t *testing.T) {
	n := 0
	runContract(t, NewMemoryStore(), func() string { n++; return fmt.Sprintf("O-%d", n) })
}
