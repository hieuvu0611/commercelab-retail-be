package shipment

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func TestNewBuildsAPendingParcelPerOrder(t *testing.T) {
	s, err := New("O-1", "user-1", "WH-HCM-01", []Item{{SkuID: "SKU-A", Quantity: 2}, {SkuID: "SKU-B", Quantity: 1}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.ShipmentID != "SHP-O-1" || s.Status != StatusPendingPick || s.Version != 1 || s.WarehouseID != "WH-HCM-01" {
		t.Fatalf("shipment = %+v", s)
	}
}

func TestNewMergesRepeatedSkusAndRejectsBadInput(t *testing.T) {
	s, err := New("O-1", "u", "WH-1", []Item{{SkuID: "A", Quantity: 1}, {SkuID: "A", Quantity: 2}}, now)
	if err != nil || len(s.Items) != 1 || s.Items[0].Quantity != 3 {
		t.Fatalf("merge = %+v, %v", s.Items, err)
	}
	bad := []struct {
		order, user, wh string
		items           []Item
	}{
		{"", "u", "WH-1", []Item{{SkuID: "A", Quantity: 1}}},
		{"O", "", "WH-1", []Item{{SkuID: "A", Quantity: 1}}},
		{"O", "u", "", []Item{{SkuID: "A", Quantity: 1}}},
		{"O", "u", "WH-1", nil},
		{"O", "u", "WH-1", []Item{{SkuID: "", Quantity: 1}}},
		{"O", "u", "WH-1", []Item{{SkuID: "A", Quantity: 0}}},
	}
	for i, c := range bad {
		if _, err := New(c.order, c.user, c.wh, c.items, now); err == nil {
			t.Errorf("case %d: accepted invalid input", i)
		}
	}
}
