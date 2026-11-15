package shipment

import (
	"errors"
	"testing"
	"time"
)

func handedOver(t *testing.T) Shipment {
	t.Helper()
	h, err := packedParcel(t).Handover(now)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDeliveredStartsTheSevenDayReturnWindowAndCountsDeliveredUnits(t *testing.T) {
	at := now.Add(48 * time.Hour)
	d, err := handedOver(t).MarkDelivered(at)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusDelivered || d.DeliveredAt == nil || !d.DeliveredAt.Equal(at) || d.ReturnWindowEndsAt == nil || !d.ReturnWindowEndsAt.Equal(at.Add(7*24*time.Hour)) {
		t.Fatalf("delivered = %+v", d)
	}
	if d.Items[0].DeliveredQuantity != 2 || d.Items[1].DeliveredQuantity != 1 {
		t.Fatalf("delivered quantities = %+v", d.Items)
	}
	again, err := d.MarkDelivered(at.Add(time.Hour))
	if err != nil || again.Version != d.Version || !again.ReturnWindowEndsAt.Equal(*d.ReturnWindowEndsAt) {
		t.Fatalf("repeat moved the window: %+v, %v", again, err)
	}
}

func TestCarrierNoticesNeverMoveAParcelThatWasNotHandedOver(t *testing.T) {
	for _, s := range []Shipment{pending(t), packedParcel(t)} {
		if _, err := s.MarkInTransit(now); !errors.Is(err, ErrNotDispatched) {
			t.Errorf("%s in transit: %v", s.Status, err)
		}
		if _, err := s.MarkDelivered(now); !errors.Is(err, ErrNotDispatched) {
			t.Errorf("%s delivered: %v", s.Status, err)
		}
		if _, err := s.MarkDeliveryFailed("nobody home", now); !errors.Is(err, ErrNotDispatched) {
			t.Errorf("%s failed: %v", s.Status, err)
		}
	}
}

func TestInTransitIsIgnoredOnceTheParcelIsFurtherAlong(t *testing.T) {
	d, _ := handedOver(t).MarkDelivered(now)
	same, err := d.MarkInTransit(now)
	if err != nil || same.Status != StatusDelivered || same.Version != d.Version {
		t.Fatalf("late in-transit = %+v, %v", same, err)
	}
	moving, _ := handedOver(t).MarkInTransit(now)
	if moving.Status != StatusInTransit {
		t.Fatalf("status = %s", moving.Status)
	}
}

func TestFailedDeliveryKeepsTheReasonAndDoesNotCompleteTheParcel(t *testing.T) {
	f, err := handedOver(t).MarkDeliveryFailed("customer not reachable", now)
	if err != nil || f.Status != StatusDeliveryFailed || f.FailureReason != "customer not reachable" || f.DeliveredAt != nil || f.ReturnWindowEndsAt != nil {
		t.Fatalf("failed = %+v, %v", f, err)
	}
	if _, err := handedOver(t).MarkDeliveryFailed(" ", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty reason err = %v", err)
	}
	redelivered, err := f.MarkDelivered(now)
	if err != nil || redelivered.Status != StatusDelivered {
		t.Fatalf("redelivery = %+v, %v", redelivered, err)
	}
}
