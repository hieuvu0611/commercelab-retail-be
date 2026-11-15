package seed

import "testing"

func TestStockPlanCoversEverySkuWithAllAvailabilityStates(t *testing.T) {
	plan := StockPlan()
	want := 0
	for _, p := range Products() {
		want += len(p.SKUs())
	}
	if len(plan) != want {
		t.Fatalf("plan has %d SKUs, catalog has %d", len(plan), want)
	}
	seen := map[string]bool{}
	var out, low, in int
	for _, s := range plan {
		if seen[s.SkuID] {
			t.Fatalf("duplicate SKU %s", s.SkuID)
		}
		seen[s.SkuID] = true
		switch {
		case s.Qty == 0:
			out++
		case s.Qty <= 3:
			low++
		default:
			in++
		}
	}
	if out < 3 || low < 6 || in < 30 {
		t.Fatalf("states out/low/in = %d/%d/%d; the storefront needs a visible mix", out, low, in)
	}
}
