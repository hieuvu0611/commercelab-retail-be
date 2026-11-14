package seed

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

// Summary reports what a run did.
type Summary struct {
	CategoriesCreated int
	Products          int
	SKUs              int
	PricesCreated     int
}

func (s Summary) String() string {
	return fmt.Sprintf("categories created: %d, products: %d, SKUs: %d, prices created: %d", s.CategoriesCreated, s.Products, s.SKUs, s.PricesCreated)
}

const seedActor = "seed"

// Run loads the demo catalog. It is idempotent: categories are matched by path, products and SKUs are
// rewritten with the same content, and a SKU that already has a price keeps its price history untouched.
// Products are written directly as PUBLISHED (the admin workflow is for staff); every document is validated
// with the same rules the API uses.
func Run(ctx context.Context, mongoStore *db.MongoStore, repo *repository.CatalogPricingRepository) (Summary, error) {
	var sum Summary
	catStore, err := repository.NewMongoCategoryStore(ctx, mongoStore)
	if err != nil {
		return sum, err
	}
	productStore, err := repository.NewMongoProductStore(ctx, mongoStore)
	if err != nil {
		return sum, err
	}
	catSvc := domain.NewCategoryService(catStore)

	existing, err := catSvc.List(ctx)
	if err != nil {
		return sum, err
	}
	idByPath := map[string]string{}
	for _, c := range existing {
		idByPath[strings.TrimPrefix(c.Path, "/")] = c.ID
	}
	for _, spec := range Categories() {
		if _, ok := idByPath[spec.Path]; ok {
			continue
		}
		parent, slug := "", spec.Path
		if i := strings.LastIndex(spec.Path, "/"); i >= 0 {
			parent, slug = idByPath[spec.Path[:i]], spec.Path[i+1:]
		}
		c, err := catSvc.Create(ctx, domain.CreateCategoryInput{ParentID: parent, Name: spec.Name, Slug: slug})
		if err != nil {
			return sum, fmt.Errorf("create category %s: %w", spec.Path, err)
		}
		idByPath[spec.Path] = c.ID
		sum.CategoriesCreated++
	}

	for i, spec := range Products() {
		categoryID := idByPath[spec.Category]
		at := PublishedAt(i)
		product := domain.Product{
			ID: spec.ProductID(), Slug: spec.Slug, Name: spec.Name, Description: spec.Description(), Brand: "CommerceLab",
			PrimaryCategoryID: categoryID, CategoryIDs: []string{categoryID}, VariantAxes: spec.Axes,
			Specs:  map[string]any{"material": spec.Material},
			Images: []domain.ProductImage{{ID: "placeholder", Key: spec.ImageURL(), Alt: spec.Name}},
			Status: domain.ProductPublished, Version: 1, CreatedBy: seedActor, UpdatedBy: seedActor,
			CreatedAt: at, UpdatedAt: at, PublishedAt: &at,
		}
		if err := domain.ValidateProduct(product); err != nil {
			return sum, fmt.Errorf("product %s: %w", spec.Slug, err)
		}
		if err := productStore.Upsert(ctx, product); err != nil {
			return sum, fmt.Errorf("write product %s: %w", spec.Slug, err)
		}
		sum.Products++

		var written []domain.SKU
		skuIDs := []string{}
		for _, k := range spec.SKUs() {
			sku := domain.SKU{SkuID: k.Code, ProductID: product.ID, Attributes: k.Attributes, IsActive: true, CreatedAt: at, UpdatedAt: at}
			if err := domain.ValidateSKU(product, sku, written); err != nil {
				return sum, fmt.Errorf("SKU %s: %w", k.Code, err)
			}
			if err := productStore.UpsertSKU(ctx, sku); err != nil {
				return sum, fmt.Errorf("write SKU %s: %w", k.Code, err)
			}
			written = append(written, sku)
			skuIDs = append(skuIDs, k.Code)
			sum.SKUs++

			history, err := repo.SkuPriceHistory(ctx, k.Code)
			if err != nil {
				return sum, err
			}
			if len(history) > 0 {
				continue // already priced (for example the SC01 prices from the migration): keep its history
			}
			err = repo.SetSkuPrice(ctx, domain.SetPriceInput{
				SkuID: k.Code, ProductID: product.ID, SkuName: spec.Name + " - " + strings.Join(attrValues(spec.Axes, k.Attributes), " / "),
				UnitPriceVND: k.PriceVND, EffectiveFrom: time.Now().UTC(), ChangedBy: seedActor, Orderable: true,
			})
			if err != nil {
				return sum, fmt.Errorf("price %s: %w", k.Code, err)
			}
			sum.PricesCreated++
		}
		if err := repo.SetOrderable(ctx, product.ID, skuIDs); err != nil {
			return sum, fmt.Errorf("open product %s for ordering: %w", spec.Slug, err)
		}
	}
	return sum, nil
}

func attrValues(axes []string, attrs map[string]string) []string {
	out := make([]string, len(axes))
	for i, a := range axes {
		out[i] = attrs[a]
	}
	return out
}
