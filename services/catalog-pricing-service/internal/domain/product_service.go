package domain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

var (
	ErrProductNotFound    = errors.New("product not found")
	ErrProductSlugTaken   = errors.New("another product already uses this slug")
	ErrVersionConflict    = errors.New("the product was changed by someone else")
	ErrProductNotEditable = errors.New("the product cannot be edited in its current status")
	ErrAxesLocked         = errors.New("variant axes cannot change while the product has SKUs")
	ErrSKUNotFound        = errors.New("SKU not found")
)

const (
	defaultProductPageSize = 20
	maxProductPageSize     = 100
)

// ProductRevision is the pending revision of a published product (collection catalog_drafts). The live product
// stays untouched until the revision is published; Product.Version is the version of the draft.
type ProductRevision struct {
	ProductID string  `bson:"_id"`
	Product   Product `bson:"product"`
}

// ProductFilter selects products for the admin list. Q matches name and slug as plain text.
type ProductFilter struct {
	Status ProductStatus
	Q      string
	Page   int
	Limit  int
}

// ProductStore is the persistence of products, their SKUs and draft revisions.
type ProductStore interface {
	// InsertProduct returns ErrProductSlugTaken when the slug exists.
	InsertProduct(ctx context.Context, p Product) error
	GetProduct(ctx context.Context, id string) (Product, error)
	// SaveProduct replaces the product when its stored version equals expectedVersion; it returns
	// ErrProductNotFound, ErrVersionConflict or ErrProductSlugTaken.
	SaveProduct(ctx context.Context, p Product, expectedVersion int64) error
	ListProducts(ctx context.Context, f ProductFilter) ([]Product, int64, error)
	// DeleteDraft removes the pending revision; a missing draft is not an error.
	DeleteDraft(ctx context.Context, productID string) error
	// GetDraft reports found=false when the product has no pending revision.
	GetDraft(ctx context.Context, productID string) (ProductRevision, bool, error)
	// SaveDraft stores the draft: expectedVersion 0 inserts a new one (ErrVersionConflict if it exists),
	// otherwise the stored draft version must equal expectedVersion.
	SaveDraft(ctx context.Context, d ProductRevision, expectedVersion int64) error
	ListSKUs(ctx context.Context, productID string) ([]SKU, error)
	GetSKU(ctx context.Context, skuID string) (SKU, error)
	// InsertSKU returns ErrSKUCodeTaken when the code exists.
	InsertSKU(ctx context.Context, s SKU) error
	SaveSKU(ctx context.Context, s SKU) error
	DeleteSKU(ctx context.Context, skuID string) error
}

// CategoryLookup is the part of the category tree the product rules need.
type CategoryLookup interface {
	Get(ctx context.Context, id string) (Category, error)
}

// ProductService implements the admin product rules.
type ProductService struct {
	store      ProductStore
	categories CategoryLookup
	prices     PriceChecker
	gate       OrderabilityGate
	now        func() time.Time
	newID      func() string
}

func NewProductService(store ProductStore, categories CategoryLookup) *ProductService {
	return &ProductService{
		store: store, categories: categories,
		now:   func() time.Time { return time.Now().UTC() },
		newID: func() string { return "prod-" + strings.TrimPrefix(newCategoryID(), "cat-") },
	}
}

// ProductView is what the admin API returns: the product (the pending draft revision when there is one,
// otherwise the live product) with its SKUs. Version is the value clients send back for optimistic concurrency.
type ProductView struct {
	Product
	HasDraft bool  `json:"hasDraft"`
	SKUs     []SKU `json:"skus"`
}

// ProductInput is the data to create a product.
type ProductInput struct {
	Slug              string
	Name              string
	Description       string
	Brand             string
	PrimaryCategoryID string
	CategoryIDs       []string
	VariantAxes       []string
	Specs             map[string]any
}

// ProductPatch lists the fields to change; nil fields stay as they are.
type ProductPatch struct {
	Slug              *string
	Name              *string
	Description       *string
	Brand             *string
	PrimaryCategoryID *string
	CategoryIDs       *[]string
	VariantAxes       *[]string
	Specs             *map[string]any
}

// SKUInput is the data to create a SKU.
type SKUInput struct {
	SkuID      string
	Attributes map[string]string
}

// SKUPatch changes a SKU: a non-nil Attributes map replaces all attributes.
type SKUPatch struct {
	Attributes map[string]string
	IsActive   *bool
}

func (s *ProductService) checkCategories(ctx context.Context, ids ...string) error {
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err := s.categories.Get(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// CreateProduct creates a DRAFT product at version 1.
func (s *ProductService) CreateProduct(ctx context.Context, actor string, in ProductInput) (ProductView, error) {
	now := s.now()
	p, err := NormalizeProduct(Product{
		ID: s.newID(), Slug: in.Slug, Name: strings.TrimSpace(in.Name), Description: in.Description, Brand: in.Brand,
		PrimaryCategoryID: in.PrimaryCategoryID, CategoryIDs: in.CategoryIDs, VariantAxes: in.VariantAxes,
		Specs: in.Specs, Images: []ProductImage{}, Status: ProductDraft, Version: 1,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return ProductView{}, err
	}
	if p.VariantAxes == nil {
		p.VariantAxes = []string{}
	}
	if err := ValidateProduct(p); err != nil {
		return ProductView{}, err
	}
	if err := s.checkCategories(ctx, p.CategoryIDs...); err != nil {
		return ProductView{}, err
	}
	if err := s.store.InsertProduct(ctx, p); err != nil {
		return ProductView{}, err
	}
	return s.view(ctx, p.ID)
}

func (s *ProductService) view(ctx context.Context, id string) (ProductView, error) {
	p, err := s.store.GetProduct(ctx, id)
	if err != nil {
		return ProductView{}, err
	}
	draft, hasDraft, err := s.store.GetDraft(ctx, id)
	if err != nil {
		return ProductView{}, err
	}
	// A draft that is not newer than the live product is left over from an interrupted publish; ignore it.
	if hasDraft && draft.Product.Version <= p.Version {
		hasDraft = false
	}
	if hasDraft {
		p = draft.Product
	}
	skus, err := s.store.ListSKUs(ctx, id)
	if err != nil {
		return ProductView{}, err
	}
	if skus == nil {
		skus = []SKU{}
	}
	return ProductView{Product: p, HasDraft: hasDraft, SKUs: skus}, nil
}

// GetProduct returns the admin view of a product.
func (s *ProductService) GetProduct(ctx context.Context, id string) (ProductView, error) {
	return s.view(ctx, id)
}

// ListProducts returns one page of live products and the total number of matches.
func (s *ProductService) ListProducts(ctx context.Context, f ProductFilter) ([]Product, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = defaultProductPageSize
	}
	if f.Limit > maxProductPageSize {
		f.Limit = maxProductPageSize
	}
	if f.Status != "" && !f.Status.valid() {
		return nil, 0, &ValidationError{Fields: map[string][]string{"status": {"unknown status"}}}
	}
	return s.store.ListProducts(ctx, f)
}

func (s *ProductService) applyPatch(ctx context.Context, base Product, patch ProductPatch, actor string) (Product, error) {
	next := base
	if patch.Slug != nil {
		next.Slug = *patch.Slug
	}
	if patch.Name != nil {
		next.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Description != nil {
		next.Description = *patch.Description
	}
	if patch.Brand != nil {
		next.Brand = *patch.Brand
	}
	if patch.PrimaryCategoryID != nil {
		next.PrimaryCategoryID = *patch.PrimaryCategoryID
	}
	if patch.CategoryIDs != nil {
		next.CategoryIDs = *patch.CategoryIDs
	}
	if patch.VariantAxes != nil {
		next.VariantAxes = *patch.VariantAxes
	}
	if patch.Specs != nil {
		next.Specs = *patch.Specs
	}
	next, err := NormalizeProduct(next)
	if err != nil {
		return Product{}, err
	}
	if next.VariantAxes == nil {
		next.VariantAxes = []string{}
	}
	if err := ValidateProduct(next); err != nil {
		return Product{}, err
	}
	if err := s.checkCategories(ctx, next.CategoryIDs...); err != nil {
		return Product{}, err
	}
	if !slices.Equal(base.VariantAxes, next.VariantAxes) {
		skus, err := s.store.ListSKUs(ctx, base.ID)
		if err != nil {
			return Product{}, err
		}
		if len(skus) > 0 {
			return Product{}, ErrAxesLocked
		}
	}
	next.Version = base.Version + 1
	next.UpdatedBy, next.UpdatedAt = actor, s.now()
	return next, nil
}

// UpdateProduct edits a product guarded by expectedVersion. DRAFT and UNPUBLISHED products are edited in
// place; a PUBLISHED product gets (or continues) a draft revision and stays live unchanged; IN_REVIEW
// products cannot be edited.
func (s *ProductService) UpdateProduct(ctx context.Context, actor, id string, expectedVersion int64, patch ProductPatch) (ProductView, error) {
	live, err := s.store.GetProduct(ctx, id)
	if err != nil {
		return ProductView{}, err
	}
	switch live.Status {
	case ProductInReview:
		return ProductView{}, ErrProductNotEditable
	case ProductPublished:
		err = s.updateViaDraft(ctx, actor, live, expectedVersion, patch)
	default:
		if live.Version != expectedVersion {
			return ProductView{}, ErrVersionConflict
		}
		var next Product
		if next, err = s.applyPatch(ctx, live, patch, actor); err == nil {
			err = s.store.SaveProduct(ctx, next, expectedVersion)
		}
	}
	if err != nil {
		return ProductView{}, err
	}
	return s.view(ctx, id)
}

func (s *ProductService) updateViaDraft(ctx context.Context, actor string, live Product, expectedVersion int64, patch ProductPatch) error {
	base, draftVersion := live, int64(0)
	draft, found, err := s.store.GetDraft(ctx, live.ID)
	if err != nil {
		return err
	}
	if found {
		base, draftVersion = draft.Product, draft.Product.Version
	}
	if base.Version != expectedVersion {
		return ErrVersionConflict
	}
	next, err := s.applyPatch(ctx, base, patch, actor)
	if err != nil {
		return err
	}
	return s.store.SaveDraft(ctx, ProductRevision{ProductID: live.ID, Product: next}, draftVersion)
}

// editableForSKUs loads the live product for a SKU change: it must be DRAFT or UNPUBLISHED and at expectedVersion.
func (s *ProductService) editableForSKUs(ctx context.Context, productID string, expectedVersion int64) (Product, error) {
	p, err := s.store.GetProduct(ctx, productID)
	if err != nil {
		return Product{}, err
	}
	if p.Status != ProductDraft && p.Status != ProductUnpublished {
		return Product{}, fmt.Errorf("%w: SKUs can only change while the product is a draft or unpublished", ErrProductNotEditable)
	}
	if p.Version != expectedVersion {
		return Product{}, ErrVersionConflict
	}
	return p, nil
}

// touch bumps the product version so every SKU change is visible to concurrent editors.
func (s *ProductService) touch(ctx context.Context, p Product, actor string) error {
	next := p
	next.Version, next.UpdatedBy, next.UpdatedAt = p.Version+1, actor, s.now()
	return s.store.SaveProduct(ctx, next, p.Version)
}

func (s *ProductService) siblings(ctx context.Context, productID string) ([]SKU, error) {
	return s.store.ListSKUs(ctx, productID)
}

// CreateSKU adds an active SKU to a draft or unpublished product.
func (s *ProductService) CreateSKU(ctx context.Context, actor, productID string, expectedVersion int64, in SKUInput) (ProductView, error) {
	p, err := s.editableForSKUs(ctx, productID, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	now := s.now()
	sku := SKU{
		SkuID: in.SkuID, ProductID: productID, Attributes: trimmedAttributes(in.Attributes), IsActive: true,
		CreatedAt: now, UpdatedAt: now,
	}
	others, err := s.siblings(ctx, productID)
	if err != nil {
		return ProductView{}, err
	}
	if err := ValidateSKU(p, sku, others); err != nil {
		return ProductView{}, err
	}
	if _, err := s.store.GetSKU(ctx, sku.SkuID); err == nil {
		return ProductView{}, ErrSKUCodeTaken
	} else if !errors.Is(err, ErrSKUNotFound) {
		return ProductView{}, err
	}
	if err := s.touch(ctx, p, actor); err != nil {
		return ProductView{}, err
	}
	if err := s.store.InsertSKU(ctx, sku); err != nil {
		return ProductView{}, err
	}
	return s.view(ctx, productID)
}

func (s *ProductService) ownedSKU(ctx context.Context, productID, skuID string) (SKU, error) {
	sku, err := s.store.GetSKU(ctx, skuID)
	if err != nil {
		return SKU{}, err
	}
	if sku.ProductID != productID {
		return SKU{}, ErrSKUNotFound
	}
	return sku, nil
}

// UpdateSKU changes the attributes or the active flag of a SKU.
func (s *ProductService) UpdateSKU(ctx context.Context, actor, productID, skuID string, expectedVersion int64, patch SKUPatch) (ProductView, error) {
	p, err := s.editableForSKUs(ctx, productID, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	sku, err := s.ownedSKU(ctx, productID, skuID)
	if err != nil {
		return ProductView{}, err
	}
	if patch.Attributes != nil {
		sku.Attributes = trimmedAttributes(patch.Attributes)
	}
	if patch.IsActive != nil {
		sku.IsActive = *patch.IsActive
	}
	sku.UpdatedAt = s.now()
	others, err := s.siblings(ctx, productID)
	if err != nil {
		return ProductView{}, err
	}
	if err := ValidateSKU(p, sku, others); err != nil {
		return ProductView{}, err
	}
	if err := s.touch(ctx, p, actor); err != nil {
		return ProductView{}, err
	}
	if err := s.store.SaveSKU(ctx, sku); err != nil {
		return ProductView{}, err
	}
	return s.view(ctx, productID)
}

// DeleteSKU removes a SKU from a draft or unpublished product.
func (s *ProductService) DeleteSKU(ctx context.Context, actor, productID, skuID string, expectedVersion int64) (ProductView, error) {
	p, err := s.editableForSKUs(ctx, productID, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	if _, err := s.ownedSKU(ctx, productID, skuID); err != nil {
		return ProductView{}, err
	}
	if err := s.touch(ctx, p, actor); err != nil {
		return ProductView{}, err
	}
	if err := s.store.DeleteSKU(ctx, skuID); err != nil {
		return ProductView{}, err
	}
	return s.view(ctx, productID)
}
