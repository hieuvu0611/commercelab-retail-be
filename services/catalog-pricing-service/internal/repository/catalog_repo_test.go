package repository

import (
	"context"
	"errors"
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
	if _, err := repo.GetSkuPrice(ctx, "SKU-BALO-BLACK-20L"); !errors.Is(err, ErrSkuNotFound) {
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
