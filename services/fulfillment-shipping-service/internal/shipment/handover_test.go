package shipment

import (
	"errors"
	"testing"
	"time"
)

func packedParcel(t *testing.T) Shipment {
	t.Helper()
	s := pending(t)
	s, _ = s.Scan("SKU-A", 2, now)
	s, _ = s.Scan("SKU-B", 1, now)
	s, err := s.Pack(now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHandoverNeedsAPackedParcel(t *testing.T) {
	for _, s := range []Shipment{pending(t), func() Shipment { x := pending(t); x, _ = x.Scan("SKU-A", 1, now); return x }()} {
		if _, err := s.Handover(now); !errors.Is(err, ErrHandoverNotAllowed) {
			t.Fatalf("status %s: err = %v", s.Status, err)
		}
	}
	stopped := packedParcel(t)
	stopped.Status = "STOPPED_FOR_CANCEL"
	if _, err := stopped.Handover(now); !errors.Is(err, ErrHandoverNotAllowed) {
		t.Fatalf("stopped err = %v", err)
	}
}

func TestHandoverStampsTheTimeAndIsRepeatable(t *testing.T) {
	p := packedParcel(t)
	at := now.Add(time.Hour)
	h, err := p.Handover(at)
	if err != nil || h.Status != StatusHandedOver || h.HandedOverAt == nil || !h.HandedOverAt.Equal(at) || h.Version != p.Version+1 {
		t.Fatalf("handed over = %+v, %v", h, err)
	}
	again, err := h.Handover(at.Add(time.Hour))
	if err != nil || again.Version != h.Version || !again.HandedOverAt.Equal(at) {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
}

func TestHandoverRejectsPackedParcelWithConfirmedStop(t *testing.T) {
	p := packedParcel(t)
	p.StopConfirmedAt = &now
	if _, err := p.Handover(now); !errors.Is(err, ErrHandoverNotAllowed) {
		t.Fatalf("confirmed stop: err = %v", err)
	}
}
