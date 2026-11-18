package promotion

import (
	"context"
	"testing"
	"time"
)

func TestEvaluatePriorityStackingSegmentAndBudget(t *testing.T) {
	now := time.Now().UTC()
	campaigns := []Campaign{
		enrich(Campaign{
			CampaignID:       "CAMP-AUTO",
			Code:             "AUTO5",
			Name:             "Auto 5%",
			DiscountType:     TypePercent,
			DiscountValue:    5,
			MaxDiscountVND:   50000,
			MinOrderVND:      500000,
			CustomerSegment:  SegmentAll,
			AutoApply:        true,
			Stackable:        true,
			Priority:         20,
			TotalQuota:       10,
			BudgetVND:        100000,
			PerCustomerLimit: 2,
			Status:           StatusActive,
		}),
		enrich(Campaign{
			CampaignID:       "CAMP-NEW",
			Code:             "WELCOME50K",
			Name:             "New Customer 50k",
			DiscountType:     TypeFixedAmount,
			DiscountValue:    50000,
			MinOrderVND:      200000,
			EligibleSkuIDs:   []string{"SKU-1"},
			CustomerSegment:  SegmentNewCustomer,
			AutoApply:        false,
			Stackable:        true,
			Priority:         10,
			TotalQuota:       10,
			BudgetVND:        200000,
			PerCustomerLimit: 1,
			Status:           StatusActive,
		}),
		enrich(Campaign{
			CampaignID:       "CAMP-EXCL",
			Code:             "NONSTACK",
			Name:             "Exclusive 20%",
			DiscountType:     TypePercent,
			DiscountValue:    20,
			MinOrderVND:      100000,
			CustomerSegment:  SegmentAll,
			AutoApply:        false,
			Stackable:        false,
			Priority:         30,
			TotalQuota:       10,
			BudgetVND:        500000,
			PerCustomerLimit: 1,
			Status:           StatusActive,
		}),
	}

	res := Evaluate(campaigns, map[string]int64{}, true, EvalInput{
		UserID:      "u-1",
		CampaignIDs: []string{"CAMP-NEW", "CAMP-EXCL"},
		Lines: []EvalLine{
			{SkuID: "SKU-1", Quantity: 2, UnitPriceVND: 300000},
		},
	}, now)

	// CAMP-NEW (priority 10, stackable=true) applies first: 50,000 -> remaining payable = 550,000.
	// CAMP-AUTO (priority 20, stackable=true) applies second: min(5% * 550,000 = 27,500, 50,000) = 27,500 -> total = 77,500.
	// CAMP-EXCL (priority 30, stackable=false) is skipped because prior stackable campaign cannot combine with non-stackable.
	if res.TotalDiscountVND != 77500 || len(res.Applied) != 2 {
		t.Fatalf("got total=%d applied=%+v, want 77500 and 2", res.TotalDiscountVND, res.Applied)
	}
	if res.Applied[0].CampaignID != "CAMP-NEW" || res.Applied[1].CampaignID != "CAMP-AUTO" {
		t.Fatalf("unexpected priority ordering: %+v", res.Applied)
	}

	// Returning customer must fail NEW_CUSTOMER check.
	resOld := Evaluate(campaigns, map[string]int64{}, false, EvalInput{
		UserID:      "u-old",
		CampaignIDs: []string{"CAMP-NEW"},
		Lines:       []EvalLine{{SkuID: "SKU-1", Quantity: 2, UnitPriceVND: 300000}},
	}, now)
	if resOld.TotalDiscountVND != 30000 || len(resOld.Applied) != 1 || resOld.Applied[0].CampaignID != "CAMP-AUTO" {
		t.Fatalf("expected only CAMP-AUTO for returning customer, got %+v", resOld)
	}
}

func TestMemoryStoreBudgetAndQuotaLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewMemoryStore(Campaign{
		CampaignID:       "CAMP-CAP",
		Code:             "CAP",
		Name:             "Budget capped",
		DiscountType:     TypeFixedAmount,
		DiscountValue:    40000,
		TotalQuota:       5,
		BudgetVND:        60000,
		PerCustomerLimit: 5,
		Status:           StatusActive,
	})

	if err := store.Reserve(ctx, "CAMP-CAP", "u-1", "O-1", 40000, now); err != nil {
		t.Fatalf("reserve O-1 failed: %v", err)
	}
	// Idempotent replay succeeds.
	if err := store.Reserve(ctx, "CAMP-CAP", "u-1", "O-1", 40000, now); err != nil {
		t.Fatalf("idempotent reserve O-1 failed: %v", err)
	}
	// Second order exceeding remaining 20k budget must fail.
	if err := store.Reserve(ctx, "CAMP-CAP", "u-2", "O-2", 40000, now); err != ErrBudgetExceeded {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	// Consume O-1 transitions reserved -> consumed.
	if err := store.ConsumeOrder(ctx, "O-1"); err != nil {
		t.Fatalf("consume O-1: %v", err)
	}
	c, _ := store.Get(ctx, "CAMP-CAP")
	if c.ConsumedQuota != 1 || c.ReservedQuota != 0 || c.BudgetConsumedVND != 40000 || c.BudgetReservedVND != 0 {
		t.Fatalf("unexpected buckets after consume: %+v", c)
	}
	// Reserve & Release O-3 restores budget & quota.
	if err := store.Reserve(ctx, "CAMP-CAP", "u-3", "O-3", 20000, now); err != nil {
		t.Fatalf("reserve O-3 failed: %v", err)
	}
	if err := store.ReleaseOrder(ctx, "O-3"); err != nil {
		t.Fatalf("release O-3: %v", err)
	}
	c, _ = store.Get(ctx, "CAMP-CAP")
	if c.ReservedQuota != 0 || c.BudgetReservedVND != 0 || c.AvailableQuota != 4 {
		t.Fatalf("unexpected buckets after release: %+v", c)
	}
}
