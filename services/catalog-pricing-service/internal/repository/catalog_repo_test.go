package repository

import (
	"context"
	"errors"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestProductDocFromBSONReadsTheProductModel(t *testing.T) {
	doc := productDocFromBSON(bson.M{
		"_id": "prod-1", "name": "City Backpack", "status": "PUBLISHED", "primaryCategoryId": "cat-city",
		"categoryIds": bson.A{"cat-city", "cat-sale"}, "variantAxes": bson.A{"color", "capacity"},
		"specs": bson.M{"capacityLiters": 20},
	})
	if doc.ProductID != "prod-1" || doc.PrimaryCategoryID != "cat-city" || doc.Status != "PUBLISHED" {
		t.Fatalf("doc = %+v", doc)
	}
	if !reflect.DeepEqual(doc.CategoryIDs, []string{"cat-city", "cat-sale"}) || !reflect.DeepEqual(doc.VariantAxes, []string{"color", "capacity"}) {
		t.Fatalf("lists = %+v", doc)
	}
	if doc.Specs["capacityLiters"] != 20 {
		t.Fatalf("specs = %#v", doc.Specs)
	}
}

func TestProductDocFromBSONReconcilesLegacySnakeCaseSpecsAndIDs(t *testing.T) {
	doc := productDocFromBSON(bson.M{
		"product_id": "PROD-BALO-CITY", "name": "Balo", "category_type": "BACKPACK", "status": "PUBLISHED",
		"specs": bson.M{"capacity_liters": 20, "water_resistance": "IPX4"},
	})
	if doc.ProductID != "PROD-BALO-CITY" {
		t.Fatalf("id = %q", doc.ProductID)
	}
	want := map[string]any{"capacityLiters": 20, "waterResistance": "IPX4"}
	if !reflect.DeepEqual(doc.Specs, want) {
		t.Fatalf("specs = %#v", doc.Specs)
	}
	if len(doc.CategoryIDs) != 0 || len(doc.VariantAxes) != 0 {
		t.Fatalf("legacy docs have no categories or axes: %+v", doc)
	}
}

func TestProductDocFromBSONDropsSpecsWithCollidingKeys(t *testing.T) {
	doc := productDocFromBSON(bson.M{"_id": "p", "specs": bson.M{"capacity_liters": 1, "capacityLiters": 2}})
	if len(doc.Specs) != 0 {
		t.Fatalf("specs = %#v", doc.Specs)
	}
}

func TestMemoryPricingPortsOpenAndCloseSKUs(t *testing.T) {
	repo := NewCatalogPricingRepository(nil, nil)
	ctx := context.Background()

	priced, err := repo.SKUsWithActivePrice(ctx, []string{"SKU-BALO-BLACK-20L", "SKU-UNKNOWN"})
	if err != nil || !priced["SKU-BALO-BLACK-20L"] || priced["SKU-UNKNOWN"] {
		t.Fatalf("priced = %v, %v", priced, err)
	}
	if err := repo.SetOrderable(ctx, "PROD-BALO-CITY", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSkuPrice(ctx, "SKU-BALO-BLACK-20L"); !errors.Is(err, ErrSkuNotOrderable) {
		t.Fatalf("closed SKU must not quote, err = %v", err)
	}
	if err := repo.SetOrderable(ctx, "PROD-BALO-CITY", []string{"SKU-BALO-BLACK-20L"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSkuPrice(ctx, "SKU-BALO-BLACK-20L"); err != nil {
		t.Fatalf("opened SKU must quote: %v", err)
	}
	// Other products are untouched.
	if _, err := repo.GetSkuPrice(ctx, "SKU-BOTTLE-BLUE-500ML"); err != nil {
		t.Fatalf("other product affected: %v", err)
	}
}

func TestMemoryPricePeriods(t *testing.T) {
	repo := NewCatalogPricingRepository(nil, nil)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	ctx := context.Background()
	set := func(price int64, from time.Time) {
		t.Helper()
		err := repo.SetSkuPrice(ctx, domain.SetPriceInput{
			SkuID: "SKU-BALO-BLACK-20L", ProductID: "PROD-BALO-CITY", SkuName: "Balo", UnitPriceVND: price, EffectiveFrom: from, ChangedBy: "m",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	priceAt := func(at time.Time) int64 {
		t.Helper()
		repo.now = func() time.Time { return at }
		sp, err := repo.GetSkuPrice(ctx, "SKU-BALO-BLACK-20L")
		if err != nil {
			t.Fatal(err)
		}
		return sp.UnitPriceVND
	}

	set(320000, now)
	set(350000, now.Add(48*time.Hour))
	if priceAt(now) != 320000 || priceAt(now.Add(47*time.Hour)) != 320000 || priceAt(now.Add(48*time.Hour)) != 350000 {
		t.Fatal("periods are not applied by time")
	}
	history, _ := repo.SkuPriceHistory(ctx, "SKU-BALO-BLACK-20L")
	if len(history) != 3 || history[0].UnitPriceVND != 350000 || history[1].EffectiveTo == nil || !history[1].EffectiveTo.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("history = %+v", history)
	}

	// A new price for an earlier moment supersedes the scheduled one.
	repo.now = func() time.Time { return now }
	set(330000, now.Add(24*time.Hour))
	if priceAt(now.Add(72*time.Hour)) != 330000 {
		t.Fatal("scheduled price must be superseded")
	}
	history, _ = repo.SkuPriceHistory(ctx, "SKU-BALO-BLACK-20L")
	if len(history) != 3 {
		t.Fatalf("history after superseding = %+v", history)
	}
}
