package domain

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrInvalidTransition = errors.New("the product status does not allow this action")
	errPricingMissing    = errors.New("pricing is not configured for the publish workflow")
)

const maxRejectionReasonLen = 500

// PublishError lists everything that blocks publishing, keyed by field (for example "images" or "skus.SKU-A").
type PublishError struct {
	Fields map[string][]string
}

func (e *PublishError) Error() string { return "the product is not ready to be published" }

// PriceChecker tells which SKUs currently have an active price.
type PriceChecker interface {
	SKUsWithActivePrice(ctx context.Context, skuIDs []string) (map[string]bool, error)
}

// OrderabilityGate makes exactly the given SKUs of a product orderable (an empty list closes the product).
type OrderabilityGate interface {
	SetOrderable(ctx context.Context, productID string, orderableSKUIDs []string) error
}

// WithPricing installs the pricing ports used by the publish workflow and returns the service.
func (s *ProductService) WithPricing(prices PriceChecker, gate OrderabilityGate) *ProductService {
	s.prices, s.gate = prices, gate
	return s
}

// PublishCheck is everything ValidatePublish looks at.
type PublishCheck struct {
	Product               Product
	SKUs                  []SKU
	PricedSKUs            map[string]bool
	PrimaryCategoryActive bool
}

// ValidatePublish returns the field-level problems that block publishing; an empty map means ready.
func ValidatePublish(c PublishCheck) map[string][]string {
	problems := map[string][]string{}
	add := func(field, msg string) { problems[field] = append(problems[field], msg) }

	if strings.TrimSpace(c.Product.Name) == "" {
		add("name", "name is required")
	}
	if strings.TrimSpace(c.Product.Description) == "" {
		add("description", "description is required")
	}
	switch {
	case c.Product.PrimaryCategoryID == "":
		add("primaryCategoryId", "a primary category is required")
	case !c.PrimaryCategoryActive:
		add("primaryCategoryId", "the primary category must exist and be active")
	}
	if len(c.Product.Images) == 0 {
		add("images", "at least one image is required")
	}
	for _, code := range DuplicateSKUCodes(c.SKUs) {
		add("skus", fmt.Sprintf("duplicate SKU code %s", code))
	}
	var active []SKU
	for _, s := range c.SKUs {
		if s.IsActive {
			active = append(active, s)
		}
	}
	if len(active) == 0 {
		add("skus", "at least one active SKU is required")
	}
	seen := map[string]string{}
	for _, s := range active {
		if !c.PricedSKUs[s.SkuID] {
			add("skus."+s.SkuID, fmt.Sprintf("SKU %s has no active price", s.SkuID))
		}
		key := combinationKey(c.Product.VariantAxes, s.Attributes)
		if other, dup := seen[key]; dup && other != s.SkuID {
			add("skus", fmt.Sprintf("SKUs %s and %s have the same variant combination", other, s.SkuID))
		}
		seen[key] = s.SkuID
	}
	return problems
}

func activeSKUIDs(skus []SKU) []string {
	ids := []string{}
	for _, s := range skus {
		if s.IsActive {
			ids = append(ids, s.SkuID)
		}
	}
	sort.Strings(ids)
	return ids
}

// target is a product loaded for a status change: Live is the stored document, Base the content the change
// applies to (the draft revision when one is pending), Version the version clients see.
type target struct {
	Live     Product
	Base     Product
	HasDraft bool
}

func (t target) version() int64 { return t.Base.Version }

// nextVersion is above both the live and the draft version so the stored version only ever grows.
func (t target) nextVersion() int64 { return max(t.Live.Version, t.Base.Version) + 1 }

func (s *ProductService) load(ctx context.Context, id string, expectedVersion int64) (target, error) {
	live, err := s.store.GetProduct(ctx, id)
	if err != nil {
		return target{}, err
	}
	t := target{Live: live, Base: live}
	draft, found, err := s.store.GetDraft(ctx, id)
	if err != nil {
		return target{}, err
	}
	if found && draft.Product.Version > live.Version {
		t.Base, t.HasDraft = draft.Product, true
	}
	if t.version() != expectedVersion {
		return target{}, ErrVersionConflict
	}
	return t, nil
}

func (s *ProductService) commit(ctx context.Context, t target, next Product) (ProductView, error) {
	if err := s.store.SaveProduct(ctx, next, t.Live.Version); err != nil {
		return ProductView{}, err
	}
	if t.HasDraft {
		if err := s.store.DeleteDraft(ctx, next.ID); err != nil {
			return ProductView{}, err
		}
	}
	return s.view(ctx, next.ID)
}

func (s *ProductService) stamp(next Product, t target, actor string, status ProductStatus) Product {
	next.Status, next.Version = status, t.nextVersion()
	next.UpdatedBy, next.UpdatedAt = actor, s.now()
	return next
}

// SubmitReview sends a DRAFT or UNPUBLISHED product to review.
func (s *ProductService) SubmitReview(ctx context.Context, actor, id string, expectedVersion int64) (ProductView, error) {
	t, err := s.load(ctx, id, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	if t.Live.Status != ProductDraft && t.Live.Status != ProductUnpublished {
		return ProductView{}, fmt.Errorf("%w: only draft or unpublished products can be submitted", ErrInvalidTransition)
	}
	next := s.stamp(t.Base, t, actor, ProductInReview)
	next.RejectionReason = ""
	return s.commit(ctx, t, next)
}

// Reject returns an IN_REVIEW product to DRAFT with the reviewer's reason.
func (s *ProductService) Reject(ctx context.Context, actor, id string, expectedVersion int64, reason string) (ProductView, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > maxRejectionReasonLen {
		return ProductView{}, &ValidationError{Fields: map[string][]string{
			"reason": {fmt.Sprintf("a reason of 1-%d characters is required", maxRejectionReasonLen)},
		}}
	}
	t, err := s.load(ctx, id, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	if t.Live.Status != ProductInReview {
		return ProductView{}, fmt.Errorf("%w: only products in review can be rejected", ErrInvalidTransition)
	}
	next := s.stamp(t.Base, t, actor, ProductDraft)
	next.RejectionReason = reason
	return s.commit(ctx, t, next)
}

// Publish validates and publishes an IN_REVIEW product, or applies the pending revision of a PUBLISHED one.
func (s *ProductService) Publish(ctx context.Context, actor, id string, expectedVersion int64) (ProductView, error) {
	if s.prices == nil || s.gate == nil {
		return ProductView{}, errPricingMissing
	}
	t, err := s.load(ctx, id, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	revision := t.Live.Status == ProductPublished && t.HasDraft
	if t.Live.Status != ProductInReview && !revision {
		return ProductView{}, fmt.Errorf("%w: only products in review or with a pending revision can be published", ErrInvalidTransition)
	}
	skus, err := s.store.ListSKUs(ctx, id)
	if err != nil {
		return ProductView{}, err
	}
	if problems, err := s.publishProblems(ctx, t.Base, skus); err != nil {
		return ProductView{}, err
	} else if len(problems) > 0 {
		return ProductView{}, &PublishError{Fields: problems}
	}
	next := s.stamp(t.Base, t, actor, ProductPublished)
	next.RejectionReason = ""
	if !revision || t.Live.PublishedAt == nil {
		now := s.now()
		next.PublishedAt = &now
	} else {
		next.PublishedAt = t.Live.PublishedAt
	}
	view, err := s.commit(ctx, t, next)
	if err != nil {
		return ProductView{}, err
	}
	if err := s.gate.SetOrderable(ctx, id, activeSKUIDs(skus)); err != nil {
		return ProductView{}, fmt.Errorf("product %s is published but its SKUs could not be opened for ordering: %w", id, err)
	}
	return view, nil
}

func (s *ProductService) publishProblems(ctx context.Context, p Product, skus []SKU) (map[string][]string, error) {
	priced, err := s.prices.SKUsWithActivePrice(ctx, activeSKUIDs(skus))
	if err != nil {
		return nil, err
	}
	categoryActive := false
	if p.PrimaryCategoryID != "" {
		switch c, err := s.categories.Get(ctx, p.PrimaryCategoryID); {
		case err == nil:
			categoryActive = c.IsActive
		case !errors.Is(err, ErrCategoryNotFound):
			return nil, err
		}
	}
	return ValidatePublish(PublishCheck{Product: p, SKUs: skus, PricedSKUs: priced, PrimaryCategoryActive: categoryActive}), nil
}

// Unpublish takes a PUBLISHED product off the storefront and makes its SKUs not orderable. Edits of a pending
// revision are kept as the product's content. Orders are closed first, so a failure later leaves the product
// unorderable rather than orderable-but-hidden.
func (s *ProductService) Unpublish(ctx context.Context, actor, id string, expectedVersion int64) (ProductView, error) {
	if s.gate == nil {
		return ProductView{}, errPricingMissing
	}
	t, err := s.load(ctx, id, expectedVersion)
	if err != nil {
		return ProductView{}, err
	}
	if t.Live.Status != ProductPublished {
		return ProductView{}, fmt.Errorf("%w: only published products can be unpublished", ErrInvalidTransition)
	}
	if err := s.gate.SetOrderable(ctx, id, nil); err != nil {
		return ProductView{}, fmt.Errorf("could not close the SKUs of product %s for ordering: %w", id, err)
	}
	return s.commit(ctx, t, s.stamp(t.Base, t, actor, ProductUnpublished))
}
