package cart

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

type fakePricer map[string]repository.SkuPrice

func (f fakePricer) GetSkuPrice(_ context.Context, skuID string) (repository.SkuPrice, error) {
	sp, ok := f[skuID]
	switch {
	case !ok:
		return repository.SkuPrice{}, repository.ErrSkuNotFound
	case !sp.IsPublished:
		return repository.SkuPrice{}, repository.ErrSkuNotOrderable
	}
	return sp, nil
}

func newTestService() (*Service, fakePricer) {
	prices := fakePricer{
		"SKU-A":      {SkuID: "SKU-A", SkuName: "Backpack", UnitPriceVND: 300000, IsPublished: true},
		"SKU-B":      {SkuID: "SKU-B", SkuName: "Bottle", UnitPriceVND: 100000, IsPublished: true},
		"SKU-CLOSED": {SkuID: "SKU-CLOSED", SkuName: "Closed", UnitPriceVND: 5000, IsPublished: false},
	}
	return NewService(NewMemoryStore(), prices), prices
}

func TestViewPricesLinesOnTheServer(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	if err := svc.SetQuantity(ctx, "u1", "SKU-A", 2); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetQuantity(ctx, "u1", "SKU-B", 1); err != nil {
		t.Fatal(err)
	}

	view, err := svc.View(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if view.SubtotalVND != 700000 || view.ItemCount != 3 || len(view.Items) != 2 || view.Items[0].SkuID != "SKU-A" {
		t.Fatalf("view = %+v", view)
	}
	if view.Items[0].LineTotalVND != 600000 || !view.Items[0].Orderable {
		t.Fatalf("line = %+v", view.Items[0])
	}
}

func TestSetQuantityRules(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	for _, q := range []int64{0, -1, 100} {
		if err := svc.SetQuantity(ctx, "u1", "SKU-A", q); !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("quantity %d: err = %v; want ErrInvalidQuantity", q, err)
		}
	}
	if err := svc.SetQuantity(ctx, "u1", "SKU-A", 99); err != nil {
		t.Fatalf("99 is allowed: %v", err)
	}
	if err := svc.SetQuantity(ctx, "u1", "SKU-NOPE", 1); !errors.Is(err, repository.ErrSkuNotFound) {
		t.Fatalf("unknown SKU: %v", err)
	}
	if err := svc.SetQuantity(ctx, "u1", "SKU-CLOSED", 1); !errors.Is(err, repository.ErrSkuNotOrderable) {
		t.Fatalf("unpublished SKU: %v", err)
	}
}

func TestCartsAreIsolatedPerUser(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	_ = svc.SetQuantity(ctx, "alice", "SKU-A", 3)
	view, _ := svc.View(ctx, "bob")
	if len(view.Items) != 0 || view.SubtotalVND != 0 {
		t.Fatalf("bob sees %+v", view)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	_ = svc.SetQuantity(ctx, "u1", "SKU-A", 1)
	for range 2 {
		if err := svc.Remove(ctx, "u1", "SKU-A"); err != nil {
			t.Fatal(err)
		}
	}
	if view, _ := svc.View(ctx, "u1"); len(view.Items) != 0 {
		t.Fatalf("view = %+v", view)
	}
}

func TestCartHoldsAtMostFiftyLines(t *testing.T) {
	prices := fakePricer{}
	for i := range MaxLines + 1 {
		id := fmt.Sprintf("SKU-%03d", i)
		prices[id] = repository.SkuPrice{SkuID: id, UnitPriceVND: 1000, IsPublished: true}
	}
	svc := NewService(NewMemoryStore(), prices)
	ctx := context.Background()
	for i := range MaxLines {
		if err := svc.SetQuantity(ctx, "u1", fmt.Sprintf("SKU-%03d", i), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SetQuantity(ctx, "u1", fmt.Sprintf("SKU-%03d", MaxLines), 1); !errors.Is(err, ErrCartFull) {
		t.Fatalf("51st line: %v; want ErrCartFull", err)
	}
	if err := svc.SetQuantity(ctx, "u1", "SKU-000", 5); err != nil {
		t.Fatalf("changing an existing line must still work: %v", err)
	}
}

func TestClosedSkuStaysVisibleButIsNotCounted(t *testing.T) {
	svc, prices := newTestService()
	ctx := context.Background()
	_ = svc.SetQuantity(ctx, "u1", "SKU-A", 1)
	_ = svc.SetQuantity(ctx, "u1", "SKU-B", 1)
	sp := prices["SKU-B"]
	sp.IsPublished = false
	prices["SKU-B"] = sp

	view, err := svc.View(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if view.SubtotalVND != 300000 || len(view.Items) != 2 || view.Items[1].Orderable || view.Items[1].LineTotalVND != 0 {
		t.Fatalf("view = %+v", view)
	}
}

func TestMergeTakesMaxPerSkuAndIsIdempotent(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	_ = svc.SetQuantity(ctx, "u1", "SKU-A", 5)
	guest := []Line{
		{SkuID: "SKU-A", Quantity: 2},
		{SkuID: "SKU-B", Quantity: 150},
		{SkuID: "SKU-CLOSED", Quantity: 1},
		{SkuID: "SKU-NOPE", Quantity: 1},
		{SkuID: "SKU-A", Quantity: 0},
	}
	var skipped []Skipped
	for range 2 {
		var err error
		if skipped, err = svc.Merge(ctx, "u1", guest); err != nil {
			t.Fatal(err)
		}
	}
	view, _ := svc.View(ctx, "u1")
	if len(view.Items) != 2 || view.Items[0].Quantity != 5 || view.Items[1].Quantity != 99 {
		t.Fatalf("view = %+v", view)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.SkuID] = s.Reason
	}
	if reasons["SKU-CLOSED"] != "SKU_NOT_ORDERABLE" || reasons["SKU-NOPE"] != "SKU_NOT_FOUND" || len(skipped) != 3 {
		t.Fatalf("skipped = %+v", skipped)
	}
}
