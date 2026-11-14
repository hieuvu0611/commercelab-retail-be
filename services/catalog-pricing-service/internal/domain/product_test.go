package domain

import (
	"reflect"
	"strings"
	"testing"
)

func validProduct() Product {
	return Product{
		ID: "prod-1", Slug: "city-backpack", Name: "City Backpack", Brand: "Acme",
		PrimaryCategoryID: "cat-city", CategoryIDs: []string{"cat-city", "cat-sale"},
		VariantAxes: []string{"color", "capacity"}, Status: ProductDraft, Version: 1,
	}
}

func fieldsOf(t *testing.T, err error) map[string][]string {
	t.Helper()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	return ve.Fields
}

func TestValidateProductAcceptsAValidDraft(t *testing.T) {
	if err := ValidateProduct(validProduct()); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateProductRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(p *Product)
		field  string
	}{
		{"blank name", func(p *Product) { p.Name = "  " }, "name"},
		{"too long name", func(p *Product) { p.Name = strings.Repeat("a", 201) }, "name"},
		{"bad slug", func(p *Product) { p.Slug = "City Backpack" }, "slug"},
		{"unknown status", func(p *Product) { p.Status = "ARCHIVED" }, "status"},
		{"axis not camelCase", func(p *Product) { p.VariantAxes = []string{"Color"} }, "variantAxes"},
		{"axis with underscore", func(p *Product) { p.VariantAxes = []string{"battery_size"} }, "variantAxes"},
		{"duplicate axis", func(p *Product) { p.VariantAxes = []string{"color", "color"} }, "variantAxes"},
		{"too many axes", func(p *Product) { p.VariantAxes = []string{"a", "b", "c", "d", "e", "f"} }, "variantAxes"},
		{"primary category missing from categoryIds", func(p *Product) { p.CategoryIDs = []string{"cat-sale"} }, "categoryIds"},
		{"duplicate category", func(p *Product) { p.CategoryIDs = []string{"cat-city", "cat-city"} }, "categoryIds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := validProduct()
			tc.mutate(&p)
			if got := fieldsOf(t, ValidateProduct(p)); len(got[tc.field]) == 0 {
				t.Fatalf("fields = %v, want problem on %s", got, tc.field)
			}
		})
	}
}

func TestNormalizeSpecKey(t *testing.T) {
	tests := map[string]string{
		"capacity_liters":         "capacityLiters",
		"laptop_compartment_inch": "laptopCompartmentInch",
		"capacityLiters":          "capacityLiters",
		"material":                "material",
		"keep-hot-hours":          "keepHotHours",
		"Water Resistance":        "waterResistance",
		"IPX_rating":              "ipxRating",
		"  volume_ml ":            "volumeMl",
	}
	for in, want := range tests {
		if got := NormalizeSpecKey(in); got != want {
			t.Errorf("NormalizeSpecKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeSpecsConvertsNestedKeysAndRejectsCollisions(t *testing.T) {
	got, err := NormalizeSpecs(map[string]any{
		"capacity_liters": 20,
		"dimensions_cm":   map[string]any{"max_height": 45},
		"tags":            []any{map[string]any{"tag_name": "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"capacityLiters": 20,
		"dimensionsCm":   map[string]any{"maxHeight": 45},
		"tags":           []any{map[string]any{"tagName": "x"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if _, err := NormalizeSpecs(map[string]any{"capacity_liters": 1, "capacityLiters": 2}); err == nil {
		t.Fatal("expected a collision error")
	}
	if _, err := NormalizeSpecs(map[string]any{"___": 1}); err == nil {
		t.Fatal("expected an error for an empty key")
	}
	if got, err := NormalizeSpecs(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("nil specs = %#v, %v", got, err)
	}
}

func skuOf(code string, attrs map[string]string) SKU {
	return SKU{SkuID: code, ProductID: "prod-1", Attributes: attrs, IsActive: true}
}

func TestValidateSKU(t *testing.T) {
	p := validProduct()
	black20 := skuOf("SKU-CITY-BLACK-20L", map[string]string{"color": "Black", "capacity": "20 L"})
	tests := []struct {
		name     string
		sku      SKU
		siblings []SKU
		field    string // empty means valid
	}{
		{"valid", black20, nil, ""},
		{"valid next to another combination", skuOf("SKU-CITY-RED-20L", map[string]string{"color": "Red", "capacity": "20 L"}), []SKU{black20}, ""},
		{"lowercase code", skuOf("sku-1", map[string]string{"color": "Black", "capacity": "20 L"}), nil, "skuId"},
		{"code with spaces", skuOf("SKU 1", map[string]string{"color": "Black", "capacity": "20 L"}), nil, "skuId"},
		{"empty code", skuOf("", map[string]string{"color": "Black", "capacity": "20 L"}), nil, "skuId"},
		{"missing axis value", skuOf("SKU-1", map[string]string{"color": "Black"}), nil, "attributes.capacity"},
		{"blank axis value", skuOf("SKU-1", map[string]string{"color": "Black", "capacity": " "}), nil, "attributes.capacity"},
		{"attribute that is not an axis", skuOf("SKU-1", map[string]string{"color": "Black", "capacity": "20 L", "weight": "1"}), nil, "attributes.weight"},
		{"duplicate combination", skuOf("SKU-OTHER", map[string]string{"color": "Black", "capacity": "20 L"}), []SKU{black20}, "attributes"},
		{"duplicate combination ignoring case and spaces", skuOf("SKU-OTHER", map[string]string{"color": " black ", "capacity": "20 l"}), []SKU{black20}, "attributes"},
		{"same sku id in siblings is an update, not a duplicate", black20, []SKU{black20}, ""},
		{"duplicate code among siblings", skuOf("SKU-CITY-BLACK-20L", map[string]string{"color": "Red", "capacity": "20 L"}), []SKU{{SkuID: "SKU-CITY-BLACK-20L", ProductID: "prod-1", Attributes: map[string]string{"color": "Black", "capacity": "20 L"}}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSKU(p, tc.sku, tc.siblings)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if got := fieldsOf(t, err); len(got[tc.field]) == 0 {
				t.Fatalf("fields = %v, want problem on %s", got, tc.field)
			}
		})
	}
}

func TestValidateSKURequiresNoAttributesForAProductWithoutAxes(t *testing.T) {
	p := validProduct()
	p.VariantAxes = nil
	if err := ValidateSKU(p, skuOf("SKU-ONLY", nil), nil); err != nil {
		t.Fatalf("single-variant product: %v", err)
	}
	// A second SKU of a product without axes would repeat the empty combination.
	if err := ValidateSKU(p, skuOf("SKU-TWO", nil), []SKU{skuOf("SKU-ONLY", nil)}); err == nil {
		t.Fatal("second SKU without axes must be a duplicate combination")
	}
}

func TestDuplicateSKUCodes(t *testing.T) {
	skus := []SKU{{SkuID: "A"}, {SkuID: "B"}, {SkuID: "A"}, {SkuID: "B"}, {SkuID: "C"}}
	if got := DuplicateSKUCodes(skus); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("got %v", got)
	}
	if got := DuplicateSKUCodes(skus[:2]); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestNormalizeProductAddsPrimaryCategoryAndCleansSpecs(t *testing.T) {
	p := validProduct()
	p.CategoryIDs = nil
	p.Specs = map[string]any{"capacity_liters": 20}
	got, err := NormalizeProduct(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.CategoryIDs, []string{"cat-city"}) || got.Specs["capacityLiters"] != 20 {
		t.Fatalf("got %+v", got)
	}
	if p.CategoryIDs != nil || p.Specs["capacity_liters"] != 20 {
		t.Fatal("NormalizeProduct must not mutate its input")
	}
}
