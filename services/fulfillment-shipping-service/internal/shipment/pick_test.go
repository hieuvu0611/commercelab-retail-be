package shipment

import (
	"errors"
	"testing"
	"time"
)

func pending(t *testing.T) Shipment {
	t.Helper()
	s, err := New("O-1", "u", "WH-HCM-01", []Item{{SkuID: "SKU-A", Quantity: 2}, {SkuID: "SKU-B", Quantity: 1}}, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScanCountsPickedUnitsAndStartsPicking(t *testing.T) {
	s := pending(t)
	got, err := s.Scan("SKU-A", 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPicking || got.Version != 2 || got.Items[0].PickedQuantity != 1 || got.UpdatedAt.Equal(s.UpdatedAt) {
		t.Fatalf("scanned = %+v", got)
	}
	if s.Items[0].PickedQuantity != 0 || s.Version != 1 {
		t.Fatal("Scan changed its receiver")
	}
}

func TestScanRejectsAnUnknownSkuAndTooManyUnits(t *testing.T) {
	s := pending(t)
	if _, err := s.Scan("SKU-X", 1, now); !errors.Is(err, ErrSkuNotOnPickList) {
		t.Fatalf("wrong sku err = %v", err)
	}
	if _, err := s.Scan("SKU-A", 3, now); !errors.Is(err, ErrQuantityExceedsOrder) {
		t.Fatalf("over quantity err = %v", err)
	}
	once, _ := s.Scan("SKU-A", 2, now)
	if _, err := once.Scan("SKU-A", 1, now); !errors.Is(err, ErrQuantityExceedsOrder) {
		t.Fatalf("cumulative over quantity err = %v", err)
	}
	for _, q := range []int64{0, -1} {
		if _, err := s.Scan("SKU-A", q, now); !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("quantity %d err = %v", q, err)
		}
	}
}

func TestPackNeedsEveryLineFullyPicked(t *testing.T) {
	s := pending(t)
	if _, err := s.Pack(now); !errors.Is(err, ErrPickIncomplete) {
		t.Fatalf("pack before scan err = %v", err)
	}
	s, _ = s.Scan("SKU-A", 2, now)
	if _, err := s.Pack(now); !errors.Is(err, ErrPickIncomplete) {
		t.Fatalf("pack with an open line err = %v", err)
	}
	s, _ = s.Scan("SKU-B", 1, now)
	packed, err := s.Pack(now)
	if err != nil || packed.Status != StatusPacked || packed.Version != s.Version+1 {
		t.Fatalf("packed = %+v, %v", packed, err)
	}
}

func TestAPackedParcelCannotBePickedAgainAndPackIsRepeatable(t *testing.T) {
	s := pending(t)
	s, _ = s.Scan("SKU-A", 2, now)
	s, _ = s.Scan("SKU-B", 1, now)
	packed, _ := s.Pack(now)
	if _, err := packed.Scan("SKU-A", 1, now); !errors.Is(err, ErrNotPickable) {
		t.Fatalf("scan after pack err = %v", err)
	}
	again, err := packed.Pack(now)
	if err != nil || again.Version != packed.Version {
		t.Fatalf("repeated pack = %+v, %v", again, err)
	}
}
