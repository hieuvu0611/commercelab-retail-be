package orders

import (
	"fmt"
	"testing"
)

func countTopLevelSQLColumns(cols string) int {
	count, depth := 1, 0
	for _, r := range cols {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				count++
			}
		}
	}
	return count
}

type stubRow struct {
	vals []any
}

func (r stubRow) Scan(dest ...any) error {
	if len(dest) != len(r.vals) {
		return fmt.Errorf("got %d vals, want %d dests", len(r.vals), len(dest))
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			if v, ok := r.vals[i].(string); ok {
				*p = v
			}
		case *[]byte:
			if v, ok := r.vals[i].([]byte); ok {
				*p = v
			}
		case *int64:
			if v, ok := r.vals[i].(int64); ok {
				*p = v
			}
		}
	}
	return nil
}

func TestOrderScanDestMatchesOrderColumns(t *testing.T) {
	colCount := countTopLevelSQLColumns(orderColumns)
	var o Order
	var promoRaw []byte
	dest := orderScanDest(&o, &promoRaw)
	if len(dest) != colCount {
		t.Fatalf("orderScanDest has %d targets, orderColumns has %d columns", len(dest), colCount)
	}

	vals := make([]any, colCount+1)
	vals[0] = "O-TEST"
	vals[6] = []byte(`[{"campaignId":"CAMP-TIER1M","code":"TIER1M5","name":"Tier","discountVnd":50000}]`)
	vals[colCount] = int64(3)

	var itemCount int64
	got, err := scanOrder(stubRow{vals: vals}, &itemCount)
	if err != nil {
		t.Fatalf("scanOrder with extra itemCount failed: %v", err)
	}
	if got.OrderID != "O-TEST" || itemCount != 3 || len(got.AppliedPromotions) != 1 || got.AppliedPromotions[0].Code != "TIER1M5" {
		t.Fatalf("unexpected scanned order: %+v itemCount=%d", got, itemCount)
	}
}
