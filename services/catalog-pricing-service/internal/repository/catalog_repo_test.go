package repository

import (
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
