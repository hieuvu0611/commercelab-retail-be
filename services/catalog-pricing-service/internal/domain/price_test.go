package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingPrices struct {
	set     []SetPriceInput
	history []PricePeriod
}

func (r *recordingPrices) SetSkuPrice(_ context.Context, in SetPriceInput) error {
	r.set = append(r.set, in)
	return nil
}

func (r *recordingPrices) SkuPriceHistory(context.Context, string) ([]PricePeriod, error) {
	return r.history, nil
}

var priceNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func newPriceEnv(t *testing.T) (*PriceService, *recordingPrices, *productEnv) {
	t.Helper()
	e := newProductEnv(t)
	rec := &recordingPrices{}
	svc := NewPriceService(e.svc, rec)
	svc.now = func() time.Time { return priceNow }
	return svc, rec, e
}

func TestSetPriceStoresAnIntegerVNDPriceWithNameAndOrderability(t *testing.T) {
	svc, rec, e := newPriceEnv(t)
	v := e.newProduct(t, "city-backpack")
	v = e.addSKU(t, v, "SKU-A", "Black")

	if _, err := svc.SetPrice(context.Background(), "mkt-1", "SKU-A", 300000, nil); err != nil {
		t.Fatal(err)
	}
	got := rec.set[0]
	if got.SkuID != "SKU-A" || got.ProductID != v.ID || got.UnitPriceVND != 300000 || got.ChangedBy != "mkt-1" {
		t.Fatalf("set = %+v", got)
	}
	if got.SkuName != "Product city-backpack - Black / 20 L" {
		t.Fatalf("sku name = %q", got.SkuName)
	}
	if !got.EffectiveFrom.Equal(priceNow) {
		t.Fatalf("effectiveFrom = %v, want now", got.EffectiveFrom)
	}
	if got.Orderable {
		t.Fatal("a SKU of a draft product must not become orderable")
	}
}

func TestSetPriceOnAPublishedProductKeepsTheSKUOrderable(t *testing.T) {
	svc, rec, e := newPriceEnv(t)
	v := e.newProduct(t, "city-backpack")
	e.addSKU(t, v, "SKU-A", "Black")
	e.publishDirectly(t, v.ID)
	if _, err := svc.SetPrice(context.Background(), "m", "SKU-A", 1000, nil); err != nil {
		t.Fatal(err)
	}
	if !rec.set[0].Orderable {
		t.Fatal("an active SKU of a published product is orderable")
	}
}

func TestSetPriceEffectiveFromRules(t *testing.T) {
	svc, rec, e := newPriceEnv(t)
	v := e.newProduct(t, "city-backpack")
	e.addSKU(t, v, "SKU-A", "Black")
	ctx := context.Background()

	future := priceNow.Add(48 * time.Hour)
	if _, err := svc.SetPrice(ctx, "m", "SKU-A", 1000, &future); err != nil || !rec.set[0].EffectiveFrom.Equal(future) {
		t.Fatalf("future: %+v, %v", rec.set, err)
	}
	slightlyPast := priceNow.Add(-30 * time.Second)
	if _, err := svc.SetPrice(ctx, "m", "SKU-A", 1000, &slightlyPast); err != nil || !rec.set[1].EffectiveFrom.Equal(priceNow) {
		t.Fatalf("clock skew must mean now: %+v, %v", rec.set, err)
	}
	past := priceNow.Add(-2 * time.Hour)
	_, err := svc.SetPrice(ctx, "m", "SKU-A", 1000, &past)
	if ve, ok := err.(*ValidationError); !ok || len(ve.Fields["effectiveFrom"]) == 0 {
		t.Fatalf("past: %v", err)
	}
	if len(rec.set) != 2 {
		t.Fatalf("a rejected price must not be stored, got %d writes", len(rec.set))
	}
}

func TestSetPriceRejectsInvalidAmountsAndUnknownSKUs(t *testing.T) {
	svc, _, e := newPriceEnv(t)
	v := e.newProduct(t, "city-backpack")
	e.addSKU(t, v, "SKU-A", "Black")
	for _, amount := range []int64{0, -1, MaxUnitPriceVND + 1} {
		_, err := svc.SetPrice(context.Background(), "m", "SKU-A", amount, nil)
		if ve, ok := err.(*ValidationError); !ok || len(ve.Fields["unitPriceVnd"]) == 0 {
			t.Fatalf("amount %d: %v", amount, err)
		}
	}
	if _, err := svc.SetPrice(context.Background(), "m", "SKU-NOPE", 1000, nil); !errors.Is(err, ErrSKUNotFound) {
		t.Fatalf("unknown SKU: %v", err)
	}
}

func TestGetPriceReportsTheCurrentPeriodAndHistory(t *testing.T) {
	svc, rec, e := newPriceEnv(t)
	v := e.newProduct(t, "city-backpack")
	e.addSKU(t, v, "SKU-A", "Black")
	soon := priceNow.Add(time.Hour)
	rec.history = []PricePeriod{
		{UnitPriceVND: 350000, EffectiveFrom: soon},
		{UnitPriceVND: 300000, EffectiveFrom: priceNow.Add(-24 * time.Hour), EffectiveTo: &soon},
		{UnitPriceVND: 250000, EffectiveFrom: priceNow.Add(-72 * time.Hour), EffectiveTo: ptrTime(priceNow.Add(-24 * time.Hour))},
	}
	view, err := svc.GetPrice(context.Background(), "SKU-A")
	if err != nil {
		t.Fatal(err)
	}
	if view.Current == nil || view.Current.UnitPriceVND != 300000 || len(view.History) != 3 {
		t.Fatalf("view = %+v", view)
	}
	rec.history = nil
	view, _ = svc.GetPrice(context.Background(), "SKU-A")
	if view.Current != nil || view.History == nil {
		t.Fatalf("no price yet: %+v", view)
	}
	if _, err := svc.GetPrice(context.Background(), "SKU-NOPE"); !errors.Is(err, ErrSKUNotFound) {
		t.Fatalf("unknown SKU: %v", err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
