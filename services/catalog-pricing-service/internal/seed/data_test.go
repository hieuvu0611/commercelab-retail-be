package seed

import (
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
)

func TestCatalogDataIsValidAndUnique(t *testing.T) {
	products := Products()
	if len(products) != 30 {
		t.Fatalf("products = %d, want 30", len(products))
	}
	paths := map[string]bool{}
	for _, c := range Categories() {
		if parent, _, ok := strings.Cut(c.Path, "/"); ok && !paths[parent] {
			t.Fatalf("category %s listed before its parent", c.Path)
		}
		paths[c.Path] = true
	}
	ids, slugs, codes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, spec := range products {
		if !paths[spec.Category] {
			t.Errorf("%s: unknown category %s", spec.Slug, spec.Category)
		}
		if ids[spec.ProductID()] || slugs[spec.Slug] {
			t.Errorf("duplicate id or slug %s", spec.Slug)
		}
		ids[spec.ProductID()], slugs[spec.Slug] = true, true
		product := domain.Product{
			ID: spec.ProductID(), Slug: spec.Slug, Name: spec.Name, PrimaryCategoryID: "cat", CategoryIDs: []string{"cat"},
			VariantAxes: spec.Axes, Status: domain.ProductPublished, Version: 1, PublishedAt: ptr(PublishedAt(i)),
		}
		if err := domain.ValidateProduct(product); err != nil {
			t.Errorf("%s: %v", spec.Slug, err)
		}
		var siblings []domain.SKU
		skus := spec.SKUs()
		if len(skus) == 0 {
			t.Errorf("%s has no SKUs", spec.Slug)
		}
		for _, k := range skus {
			sku := domain.SKU{SkuID: k.Code, ProductID: product.ID, Attributes: k.Attributes, IsActive: true}
			if err := domain.ValidateSKU(product, sku, siblings); err != nil {
				t.Errorf("%s/%s: %v", spec.Slug, k.Code, err)
			}
			if codes[k.Code] {
				t.Errorf("duplicate SKU code %s", k.Code)
			}
			codes[k.Code] = true
			siblings = append(siblings, sku)
			if k.PriceVND < 1 {
				t.Errorf("%s: price %d", k.Code, k.PriceVND)
			}
		}
	}
}

func TestScenarioSKUsOfSC01(t *testing.T) {
	prices := map[string]int64{}
	for _, spec := range Products() {
		for _, k := range spec.SKUs() {
			prices[k.Code] = k.PriceVND
		}
	}
	if prices["SKU-BALO-BLACK-20L"] != 300000 || prices["SKU-BOTTLE-BLUE-500ML"] != 100000 {
		t.Fatalf("SC01 prices = %d / %d", prices["SKU-BALO-BLACK-20L"], prices["SKU-BOTTLE-BLUE-500ML"])
	}
	for _, spec := range Products() {
		switch spec.Slug {
		case "city-backpack":
			if spec.ProductID() != "PROD-BALO-CITY" || spec.Category != "backpacks/city" {
				t.Fatalf("city backpack = %+v", spec)
			}
		case "insulated-bottle":
			if spec.ProductID() != "PROD-BOTTLE-ECO" || spec.Category != "bottles/insulated" {
				t.Fatalf("insulated bottle = %+v", spec)
			}
		}
	}
}

func TestEveryLeafCategoryHasProducts(t *testing.T) {
	used := map[string]int{}
	for _, p := range Products() {
		used[p.Category]++
	}
	for _, c := range Categories() {
		if strings.Contains(c.Path, "/") && used[c.Path] == 0 {
			t.Errorf("category %s has no products", c.Path)
		}
	}
}

func ptr[T any](v T) *T { return &v }
