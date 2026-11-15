package shipment

import (
	"errors"
	"testing"
)

func TestAShortageBlocksTheParcelAndNeverAllowsDispatch(t *testing.T) {
	s := pending(t)
	s, _ = s.Scan("SKU-A", 1, now)
	b, err := s.ReportShortage("SKU-A", 1, "shelf empty", now)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != StatusPickBlocked || b.Version != s.Version+1 || b.FailureReason != "shelf empty" {
		t.Fatalf("blocked = %+v", b)
	}
	if _, err := b.Scan("SKU-B", 1, now); !errors.Is(err, ErrNotPickable) {
		t.Fatalf("scan after shortage err = %v", err)
	}
	if _, err := b.Pack(now); !errors.Is(err, ErrNotPickable) {
		t.Fatalf("pack after shortage err = %v", err)
	}
	if _, err := b.Handover(now); !errors.Is(err, ErrHandoverNotAllowed) {
		t.Fatalf("handover after shortage err = %v", err)
	}
	if _, err := b.MarkDelivered(now); !errors.Is(err, ErrNotDispatched) {
		t.Fatalf("delivery of a blocked parcel err = %v", err)
	}
}

func TestShortageInputIsValidated(t *testing.T) {
	s := pending(t)
	s, _ = s.Scan("SKU-A", 1, now)
	for name, tc := range map[string]struct {
		sku    string
		short  int64
		reason string
		want   error
	}{
		"unknown sku":       {"SKU-X", 1, "r", ErrSkuNotOnPickList},
		"zero":              {"SKU-A", 0, "r", ErrInvalidQuantity},
		"more than missing": {"SKU-A", 2, "r", ErrQuantityExceedsOrder},
		"no reason":         {"SKU-A", 1, " ", ErrInvalid},
	} {
		if _, err := s.ReportShortage(tc.sku, tc.short, tc.reason, now); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	packed := packedParcel(t)
	if _, err := packed.ReportShortage("SKU-A", 1, "r", now); !errors.Is(err, ErrNotPickable) {
		t.Fatalf("shortage after pack err = %v", err)
	}
}

func TestPartialDeliveryRecordsUnitsAndDoesNotCompleteTheParcel(t *testing.T) {
	h := handedOver(t)
	p, err := h.MarkPartiallyDelivered([]Item{{SkuID: "SKU-A", Quantity: 2}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusPartiallyDelivered || p.Items[0].DeliveredQuantity != 2 || p.Items[1].DeliveredQuantity != 0 || p.DeliveredAt != nil || p.ReturnWindowEndsAt != nil {
		t.Fatalf("partial = %+v", p)
	}
	again, err := p.MarkPartiallyDelivered([]Item{{SkuID: "SKU-A", Quantity: 2}}, now)
	if err != nil || again.Version != p.Version {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	done, err := p.MarkDelivered(now)
	if err != nil || done.Status != StatusDelivered || done.Items[1].DeliveredQuantity != 1 || done.ReturnWindowEndsAt == nil {
		t.Fatalf("completion = %+v, %v", done, err)
	}
}

func TestPartialDeliveryInputIsValidated(t *testing.T) {
	h := handedOver(t)
	for name, items := range map[string][]Item{
		"empty":          nil,
		"unknown sku":    {{SkuID: "X", Quantity: 1}},
		"too many units": {{SkuID: "SKU-A", Quantity: 3}},
		"nothing":        {{SkuID: "SKU-A", Quantity: 0}},
		"everything":     {{SkuID: "SKU-A", Quantity: 2}, {SkuID: "SKU-B", Quantity: 1}},
	} {
		if _, err := h.MarkPartiallyDelivered(items, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := pending(t).MarkPartiallyDelivered([]Item{{SkuID: "SKU-A", Quantity: 1}}, now); !errors.Is(err, ErrNotDispatched) {
		t.Fatalf("undispatched err = %v", err)
	}
}
