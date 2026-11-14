package domain

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func publishableCheck() PublishCheck {
	p := validProduct()
	p.Description = "Water resistant city backpack"
	p.Images = []ProductImage{{Key: "products/prod-1/a.jpg"}}
	return PublishCheck{
		Product: p,
		SKUs: []SKU{
			{SkuID: "SKU-A", ProductID: p.ID, Attributes: map[string]string{"color": "Black", "capacity": "20 L"}, IsActive: true},
			{SkuID: "SKU-B", ProductID: p.ID, Attributes: map[string]string{"color": "Red", "capacity": "20 L"}, IsActive: false},
		},
		PricedSKUs:            map[string]bool{"SKU-A": true},
		PrimaryCategoryActive: true,
	}
}

func TestValidatePublishAcceptsACompleteProduct(t *testing.T) {
	if got := ValidatePublish(publishableCheck()); len(got) != 0 {
		t.Fatalf("problems = %v", got)
	}
}

func TestValidatePublishRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *PublishCheck)
		field  string
		text   string
	}{
		{"missing name", func(c *PublishCheck) { c.Product.Name = " " }, "name", "required"},
		{"missing description", func(c *PublishCheck) { c.Product.Description = "" }, "description", "required"},
		{"blank description", func(c *PublishCheck) { c.Product.Description = "  \n" }, "description", "required"},
		{"missing primary category", func(c *PublishCheck) { c.Product.PrimaryCategoryID = "" }, "primaryCategoryId", "required"},
		{"inactive or unknown primary category", func(c *PublishCheck) { c.PrimaryCategoryActive = false }, "primaryCategoryId", "active"},
		{"no images", func(c *PublishCheck) { c.Product.Images = nil }, "images", "at least one"},
		{"no SKUs", func(c *PublishCheck) { c.SKUs = nil; c.PricedSKUs = nil }, "skus", "at least one active SKU"},
		{"only inactive SKUs", func(c *PublishCheck) { c.SKUs = c.SKUs[1:] }, "skus", "at least one active SKU"},
		{"active SKU without price", func(c *PublishCheck) { c.PricedSKUs = map[string]bool{} }, "skus.SKU-A", "active price"},
		{"duplicate SKU code", func(c *PublishCheck) { c.SKUs = append(c.SKUs, c.SKUs[0]) }, "skus", "duplicate SKU code SKU-A"},
		{"duplicate combination", func(c *PublishCheck) {
			c.SKUs = append(c.SKUs, SKU{SkuID: "SKU-C", Attributes: map[string]string{"color": "BLACK", "capacity": "20 l"}, IsActive: true})
			c.PricedSKUs["SKU-C"] = true
		}, "skus", "same variant combination"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := publishableCheck()
			tc.mutate(&c)
			got := ValidatePublish(c)
			msgs := got[tc.field]
			if len(msgs) == 0 || !strings.Contains(strings.Join(msgs, "|"), tc.text) {
				t.Fatalf("problems = %v, want %q on %s", got, tc.text, tc.field)
			}
		})
	}
}

func TestValidatePublishReportsEveryProblemAtOnce(t *testing.T) {
	c := publishableCheck()
	c.Product.Name, c.Product.Description, c.Product.Images = "", "", nil
	c.PricedSKUs = nil
	fields := make([]string, 0)
	for f := range ValidatePublish(c) {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	if want := []string{"description", "images", "name", "skus.SKU-A"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}
}

// fakePricing implements PriceChecker and OrderabilityGate.
type fakePricing struct {
	priced    map[string]bool
	orderable map[string][]string
	failGate  bool
}

func newFakePricing(priced ...string) *fakePricing {
	f := &fakePricing{priced: map[string]bool{}, orderable: map[string][]string{}}
	for _, id := range priced {
		f.priced[id] = true
	}
	return f
}

func (f *fakePricing) SKUsWithActivePrice(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		if f.priced[id] {
			out[id] = true
		}
	}
	return out, nil
}

func (f *fakePricing) SetOrderable(_ context.Context, productID string, skuIDs []string) error {
	if f.failGate {
		return errors.New("postgres down")
	}
	f.orderable[productID] = skuIDs
	return nil
}

type workflowEnv struct {
	*productEnv
	pricing *fakePricing
}

func newWorkflowEnv(t *testing.T) *workflowEnv {
	t.Helper()
	e := newProductEnv(t)
	f := newFakePricing("SKU-A")
	e.svc.WithPricing(f, f)
	return &workflowEnv{productEnv: e, pricing: f}
}

// completeProduct creates a product that passes publish validation and returns its draft view.
func (w *workflowEnv) completeProduct(t *testing.T, slug string) ProductView {
	t.Helper()
	ctx := context.Background()
	v := w.newProduct(t, slug)
	desc := "Great product"
	v, err := w.svc.UpdateProduct(ctx, "s", v.ID, v.Version, ProductPatch{Description: &desc})
	if err != nil {
		t.Fatal(err)
	}
	v = w.addSKU(t, v, "SKU-A", "Black")
	p, _ := w.store.GetProduct(ctx, v.ID)
	p.Images = []ProductImage{{Key: "products/x.jpg"}}
	if err := w.store.SaveProduct(ctx, p, p.Version); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSubmitReviewPublishHappyPath(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	v := w.completeProduct(t, "city-backpack")

	in, err := w.svc.SubmitReview(ctx, "staff-1", v.ID, v.Version)
	if err != nil || in.Status != ProductInReview || in.Version != v.Version+1 {
		t.Fatalf("submit: %+v, %v", in.Product, err)
	}
	pub, err := w.svc.Publish(ctx, "staff-2", v.ID, in.Version)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Status != ProductPublished || pub.PublishedAt == nil || pub.UpdatedBy != "staff-2" || pub.Version != in.Version+1 {
		t.Fatalf("publish: %+v", pub.Product)
	}
	if got := w.pricing.orderable[v.ID]; !reflect.DeepEqual(got, []string{"SKU-A"}) {
		t.Fatalf("orderable SKUs = %v", got)
	}
}

func TestPublishFailureListsProblemsAndChangesNothing(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	v := w.newProduct(t, "empty")
	in, err := w.svc.SubmitReview(ctx, "s", v.ID, v.Version)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.Publish(ctx, "s", v.ID, in.Version)
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *PublishError", err)
	}
	for _, f := range []string{"description", "images", "skus"} {
		if len(pe.Fields[f]) == 0 {
			t.Errorf("missing problem for %s: %v", f, pe.Fields)
		}
	}
	got, _ := w.svc.GetProduct(ctx, v.ID)
	if got.Status != ProductInReview || got.Version != in.Version {
		t.Fatalf("product changed after a failed publish: %+v", got.Product)
	}
}

func TestPublishNeedsAPriceForEveryActiveSKU(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	v := w.completeProduct(t, "city-backpack")
	w.pricing.priced = map[string]bool{}
	in, _ := w.svc.SubmitReview(ctx, "s", v.ID, v.Version)
	_, err := w.svc.Publish(ctx, "s", v.ID, in.Version)
	var pe *PublishError
	if !errors.As(err, &pe) || len(pe.Fields["skus.SKU-A"]) == 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectReturnsToDraftWithReason(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	v := w.completeProduct(t, "city-backpack")
	in, _ := w.svc.SubmitReview(ctx, "s", v.ID, v.Version)

	if _, err := w.svc.Reject(ctx, "s", v.ID, in.Version, "  "); err == nil {
		t.Fatal("blank reason accepted")
	} else if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("err = %v", err)
	}
	rej, err := w.svc.Reject(ctx, "reviewer", v.ID, in.Version, "Photos are blurry")
	if err != nil || rej.Status != ProductDraft || rej.RejectionReason != "Photos are blurry" || rej.Version != in.Version+1 {
		t.Fatalf("reject: %+v, %v", rej.Product, err)
	}
	// Resubmitting clears the reason.
	again, err := w.svc.SubmitReview(ctx, "s", v.ID, rej.Version)
	if err != nil || again.RejectionReason != "" {
		t.Fatalf("resubmit: %+v, %v", again.Product, err)
	}
}

func TestInvalidTransitions(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	draft := w.completeProduct(t, "a")
	tests := []struct {
		name string
		do   func() error
	}{
		{"publish a draft", func() error { _, err := w.svc.Publish(ctx, "s", draft.ID, draft.Version); return err }},
		{"reject a draft", func() error { _, err := w.svc.Reject(ctx, "s", draft.ID, draft.Version, "no"); return err }},
		{"unpublish a draft", func() error { _, err := w.svc.Unpublish(ctx, "s", draft.ID, draft.Version); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.do(); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	in, _ := w.svc.SubmitReview(ctx, "s", draft.ID, draft.Version)
	if _, err := w.svc.SubmitReview(ctx, "s", draft.ID, in.Version); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("double submit: %v", err)
	}
	if _, err := w.svc.Unpublish(ctx, "s", draft.ID, in.Version); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unpublish in review: %v", err)
	}
}

func TestTransitionsAreGuardedByVersion(t *testing.T) {
	w := newWorkflowEnv(t)
	v := w.completeProduct(t, "a")
	if _, err := w.svc.SubmitReview(context.Background(), "s", v.ID, v.Version-1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v", err)
	}
	if _, err := w.svc.SubmitReview(context.Background(), "s", "nope", 1); !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func (w *workflowEnv) published(t *testing.T, slug string) ProductView {
	t.Helper()
	ctx := context.Background()
	v := w.completeProduct(t, slug)
	in, err := w.svc.SubmitReview(ctx, "s", v.ID, v.Version)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := w.svc.Publish(ctx, "s", v.ID, in.Version)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestUnpublishMakesSKUsNotOrderableAndKeepsHistory(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	pub := w.published(t, "city-backpack")
	un, err := w.svc.Unpublish(ctx, "s", pub.ID, pub.Version)
	if err != nil || un.Status != ProductUnpublished || un.Version != pub.Version+1 || len(un.SKUs) != 1 {
		t.Fatalf("unpublish: %+v, %v", un.Product, err)
	}
	if got := w.pricing.orderable[pub.ID]; len(got) != 0 {
		t.Fatalf("orderable after unpublish = %v", got)
	}
	// It can go through review again.
	if _, err := w.svc.SubmitReview(ctx, "s", pub.ID, un.Version); err != nil {
		t.Fatalf("resubmit unpublished: %v", err)
	}
}

func TestRejectedUnpublishDoesNotTouchOrderability(t *testing.T) {
	w := newWorkflowEnv(t)
	pub := w.published(t, "city-backpack")
	// The version is checked before anything is changed.
	if _, err := w.svc.Unpublish(context.Background(), "s", pub.ID, pub.Version-1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err = %v", err)
	}
	if got := w.pricing.orderable[pub.ID]; len(got) != 1 {
		t.Fatalf("a rejected request must not touch orderability, got %v", got)
	}
}

func TestPublishingADraftRevisionAppliesItAndClearsTheDraft(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	pub := w.published(t, "city-backpack")
	name := "City Backpack V2"
	rev, err := w.svc.UpdateProduct(ctx, "s", pub.ID, pub.Version, ProductPatch{Name: &name})
	if err != nil || !rev.HasDraft {
		t.Fatalf("revision: %+v, %v", rev.Product, err)
	}

	// Publishing validates the revision, not the live document.
	blank := ""
	bad, err := w.svc.UpdateProduct(ctx, "s", pub.ID, rev.Version, ProductPatch{Description: &blank})
	if err != nil {
		t.Fatal(err)
	}
	var pe *PublishError
	if _, err := w.svc.Publish(ctx, "s", pub.ID, bad.Version); !errors.As(err, &pe) || len(pe.Fields["description"]) == 0 {
		t.Fatalf("err = %v", err)
	}
	desc := "Back again"
	fixed, err := w.svc.UpdateProduct(ctx, "s", pub.ID, bad.Version, ProductPatch{Description: &desc})
	if err != nil {
		t.Fatal(err)
	}

	out, err := w.svc.Publish(ctx, "s", pub.ID, fixed.Version)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != name || out.Description != desc || out.HasDraft || out.Status != ProductPublished || out.Version <= fixed.Version {
		t.Fatalf("out = %+v hasDraft=%v", out.Product, out.HasDraft)
	}
	live, _ := w.store.GetProduct(ctx, pub.ID)
	if live.Name != name || live.Version != out.Version {
		t.Fatalf("live = %+v", live)
	}
	if _, found, _ := w.store.GetDraft(ctx, pub.ID); found {
		t.Fatal("draft must be removed after publishing")
	}
	if !reflect.DeepEqual(w.pricing.orderable[pub.ID], []string{"SKU-A"}) {
		t.Fatalf("orderable = %v", w.pricing.orderable[pub.ID])
	}
}

func TestUnpublishKeepsPendingRevisionEdits(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	pub := w.published(t, "city-backpack")
	name := "Edited while live"
	rev, _ := w.svc.UpdateProduct(ctx, "s", pub.ID, pub.Version, ProductPatch{Name: &name})
	un, err := w.svc.Unpublish(ctx, "s", pub.ID, rev.Version)
	if err != nil || un.Name != name || un.HasDraft || un.Status != ProductUnpublished {
		t.Fatalf("unpublish: %+v, %v", un.Product, err)
	}
}

func TestStaleDraftIsIgnoredWhenTheLiveVersionIsNewer(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	pub := w.published(t, "city-backpack")
	name := "x"
	rev, _ := w.svc.UpdateProduct(ctx, "s", pub.ID, pub.Version, ProductPatch{Name: &name})
	// Simulate a failed draft cleanup: live moves past the draft version but the draft row stays.
	live, _ := w.store.GetProduct(ctx, pub.ID)
	live.Version = rev.Version + 1
	w.store.products[pub.ID] = live
	got, _ := w.svc.GetProduct(ctx, pub.ID)
	if got.HasDraft || got.Version != live.Version {
		t.Fatalf("stale draft shown: %+v hasDraft=%v", got.Product, got.HasDraft)
	}
}

func TestPublishFailsSafelyWhenTheOrderabilityGateIsDown(t *testing.T) {
	w := newWorkflowEnv(t)
	ctx := context.Background()
	v := w.completeProduct(t, "city-backpack")
	in, _ := w.svc.SubmitReview(ctx, "s", v.ID, v.Version)
	w.pricing.failGate = true
	if _, err := w.svc.Publish(ctx, "s", v.ID, in.Version); err == nil {
		t.Fatal("expected an error")
	}
}
