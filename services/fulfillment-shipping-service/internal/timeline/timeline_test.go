package timeline

import (
	"context"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

// runContract holds for every Recorder; newID returns a fresh shipment id.
func runContract(t *testing.T, r Recorder, newID func() string) {
	ctx := context.Background()
	id := newID()
	if got, err := r.Get(ctx, id); err != nil || len(got) != 0 {
		t.Fatalf("empty timeline = %v, %v", got, err)
	}
	for i, typ := range []string{TypeCreated, TypePacked, TypeHandedOver} {
		if err := r.Append(ctx, id, "O-1", Entry{At: t0.Add(time.Duration(i) * time.Minute), Type: typ, Detail: "d" + typ}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Get(ctx, id)
	if err != nil || len(got) != 3 || got[0].Type != TypeCreated || got[2].Type != TypeHandedOver || got[1].Detail != "d"+TypePacked || !got[2].At.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("timeline = %+v, %v", got, err)
	}
	other := newID()
	if got, _ := r.Get(ctx, other); len(got) != 0 {
		t.Fatalf("another shipment's timeline = %+v", got)
	}
}

func TestMemoryRecorderContract(t *testing.T) {
	n := 0
	runContract(t, NewMemory(), func() string { n++; return "SHP-" + string(rune('A'+n)) })
}
