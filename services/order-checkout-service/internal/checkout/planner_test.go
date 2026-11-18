package checkout

import (
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
)

func TestPlanParcelsSingleAndSplitWarehouses(t *testing.T) {
	items := []orders.Item{
		{SkuID: "SKU-BALO-BLACK-20L", SkuName: "Balo", Quantity: 12, UnitPriceVND: 300_000, AllocatedUnitPaidVND: 300_000},
		{SkuID: "SKU-BOTTLE-BLUE-500ML", SkuName: "Bottle", Quantity: 5, UnitPriceVND: 100_000, AllocatedUnitPaidVND: 100_000},
	}

	// Case 1: WH-HCM-01 alone has enough -> 1 parcel
	p1, ok := PlanParcels(items, 4_100_000, false, map[string]map[string]int64{
		"WH-HCM-01": {"SKU-BALO-BLACK-20L": 15, "SKU-BOTTLE-BLUE-500ML": 10},
		"WH-HN-01":  {"SKU-BALO-BLACK-20L": 5, "SKU-BOTTLE-BLUE-500ML": 10},
	})
	if !ok || len(p1) != 1 || p1[0].WarehouseID != "WH-HCM-01" {
		t.Fatalf("expected single parcel from WH-HCM-01, got ok=%v parcels=%+v", ok, p1)
	}

	// Case 2: WH-HCM-01 has 10 Balo, WH-HN-01 has 5 Balo -> split 2 parcels, COD sums exactly to total
	splitItems := []orders.Item{
		{SkuID: "SKU-BALO-BLACK-20L", SkuName: "Balo", Quantity: 12, UnitPriceVND: 100_000, AllocatedUnitPaidVND: 100_000},
	}
	p2, ok := PlanParcels(splitItems, 1_230_000, true, map[string]map[string]int64{
		"WH-HCM-01": {"SKU-BALO-BLACK-20L": 10},
		"WH-HN-01":  {"SKU-BALO-BLACK-20L": 5},
	})
	if !ok || len(p2) != 2 {
		t.Fatalf("expected 2 split parcels, got ok=%v parcels=%+v", ok, p2)
	}
	if p2[0].WarehouseID != "WH-HCM-01" || p2[0].Items[0].Quantity != 10 {
		t.Fatalf("parcel 1 mismatch: %+v", p2[0])
	}
	if p2[1].WarehouseID != "WH-HN-01" || p2[1].Items[0].Quantity != 2 {
		t.Fatalf("parcel 2 mismatch: %+v", p2[1])
	}
	if p2[0].CodAmountVND+p2[1].CodAmountVND != 1_230_000 {
		t.Fatalf("COD split sum mismatch: %d + %d != 1230000", p2[0].CodAmountVND, p2[1].CodAmountVND)
	}

	// Case 3: Combined stock short -> ok=false
	_, ok = PlanParcels(splitItems, 1_200_000, false, map[string]map[string]int64{
		"WH-HCM-01": {"SKU-BALO-BLACK-20L": 5},
		"WH-HN-01":  {"SKU-BALO-BLACK-20L": 5},
	})
	if ok {
		t.Fatalf("expected insufficient combined stock to return ok=false")
	}
}
