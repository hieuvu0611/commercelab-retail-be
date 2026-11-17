// Package promotion implements B08 Marketing Promotion Campaigns: eligibility rules,
// VND budget & quota buckets, priority & stacking evaluation, and atomic reservation.
package promotion

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	TypePercent     = "PERCENT"
	TypeFixedAmount = "FIXED_AMOUNT"

	SegmentAll         = "ALL"
	SegmentNewCustomer = "NEW_CUSTOMER"
	SegmentVIP         = "VIP"

	StatusActive = "ACTIVE"
	StatusPaused = "PAUSED"
)

var (
	ErrNotFound         = errors.New("promotion campaign not found")
	ErrCodeExists       = errors.New("promotion campaign code already exists")
	ErrValidation       = errors.New("invalid promotion campaign")
	ErrBudgetExceeded   = errors.New("promotion campaign budget exceeded")
	ErrQuotaExhausted   = errors.New("promotion campaign quota exhausted")
	ErrCustomerLimitHit = errors.New("customer usage limit reached for promotion campaign")
)

type Campaign struct {
	CampaignID          string     `json:"campaignId"`
	Code                string     `json:"code"`
	Name                string     `json:"name"`
	DiscountType        string     `json:"discountType"`
	DiscountValue       int64      `json:"discountValue"`
	MaxDiscountVND      int64      `json:"maxDiscountVnd"`
	MinOrderVND         int64      `json:"minOrderVnd"`
	EligibleCategoryIDs []string   `json:"eligibleCategoryIds"`
	EligibleSkuIDs      []string   `json:"eligibleSkuIds"`
	CustomerSegment     string     `json:"customerSegment"`
	AutoApply           bool       `json:"autoApply"`
	Stackable           bool       `json:"stackable"`
	Priority            int        `json:"priority"`
	TotalQuota          int64      `json:"totalQuota"`
	ReservedQuota       int64      `json:"reservedQuota"`
	ConsumedQuota       int64      `json:"consumedQuota"`
	AvailableQuota      int64      `json:"availableQuota"`
	BudgetVND           int64      `json:"budgetVnd"`
	BudgetReservedVND   int64      `json:"budgetReservedVnd"`
	BudgetConsumedVND   int64      `json:"budgetConsumedVnd"`
	BudgetRemainingVND  int64      `json:"budgetRemainingVnd"`
	PerCustomerLimit    int        `json:"perCustomerLimit"`
	StartsAt            *time.Time `json:"startsAt,omitempty"`
	EndsAt              *time.Time `json:"endsAt,omitempty"`
	Status              string     `json:"status"`
	Version             int64      `json:"version"`
	CreatedBy           string     `json:"createdBy"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

func (c Campaign) CalcAvailableQuota() int64 {
	return c.TotalQuota - c.ReservedQuota - c.ConsumedQuota
}

func (c Campaign) CalcRemainingBudget() int64 {
	if c.BudgetVND <= 0 {
		return -1 // unlimited
	}
	rem := c.BudgetVND - c.BudgetReservedVND - c.BudgetConsumedVND
	if rem < 0 {
		return 0
	}
	return rem
}

type Fields struct {
	Code                string     `json:"code"`
	Name                string     `json:"name"`
	DiscountType        string     `json:"discountType"`
	DiscountValue       int64      `json:"discountValue"`
	MaxDiscountVND      int64      `json:"maxDiscountVnd"`
	MinOrderVND         int64      `json:"minOrderVnd"`
	EligibleCategoryIDs []string   `json:"eligibleCategoryIds"`
	EligibleSkuIDs      []string   `json:"eligibleSkuIds"`
	CustomerSegment     string     `json:"customerSegment"`
	AutoApply           bool       `json:"autoApply"`
	Stackable           bool       `json:"stackable"`
	Priority            int        `json:"priority"`
	TotalQuota          int64      `json:"totalQuota"`
	BudgetVND           int64      `json:"budgetVnd"`
	PerCustomerLimit    int        `json:"perCustomerLimit"`
	StartsAt            *time.Time `json:"startsAt,omitempty"`
	EndsAt              *time.Time `json:"endsAt,omitempty"`
	Status              string     `json:"status"`
}

type EvalLine struct {
	SkuID        string `json:"skuId"`
	CategoryID   string `json:"categoryId,omitempty"`
	Quantity     int64  `json:"quantity"`
	UnitPriceVND int64  `json:"unitPriceVnd"`
}

type EvalInput struct {
	UserID      string     `json:"userId"`
	CampaignIDs []string   `json:"campaignIds"`
	Lines       []EvalLine `json:"lines"`
}

type AppliedPromotion struct {
	CampaignID   string `json:"campaignId"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	DiscountType string `json:"discountType"`
	DiscountVND  int64  `json:"discountVnd"`
	Priority     int    `json:"priority"`
	Stackable    bool   `json:"stackable"`
}

type SkippedPromotion struct {
	CampaignID string `json:"campaignId"`
	Code       string `json:"code"`
	Reason     string `json:"reason"`
}

type EvalResult struct {
	SubtotalVND      int64              `json:"subtotalVnd"`
	TotalDiscountVND int64              `json:"totalDiscountVnd"`
	Applied          []AppliedPromotion `json:"applied"`
	Skipped          []SkippedPromotion `json:"skipped,omitempty"`
}

type Store interface {
	ListActive(ctx context.Context, now time.Time) ([]Campaign, error)
	List(ctx context.Context, status, q string) ([]Campaign, error)
	Get(ctx context.Context, campaignID string) (Campaign, error)
	Create(ctx context.Context, campaignID, actor string, f Fields) (Campaign, error)
	Update(ctx context.Context, campaignID string, f Fields) (Campaign, error)
	CustomerUsesMap(ctx context.Context, userID string, campaignIDs []string) (map[string]int64, error)
	IsNewCustomer(ctx context.Context, userID string) (bool, error)
	Reserve(ctx context.Context, campaignID, userID, orderID string, discountVND int64, now time.Time) error
	ConsumeOrder(ctx context.Context, orderID string) error
	ReleaseOrder(ctx context.Context, orderID string) error
}

func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func ValidateFields(f Fields, isCreate bool) error {
	if strings.TrimSpace(f.Name) == "" {
		return errors.New("campaign name is required")
	}
	if isCreate && NormalizeCode(f.Code) == "" {
		return errors.New("campaign code is required")
	}
	if f.DiscountType != TypePercent && f.DiscountType != TypeFixedAmount {
		return errors.New("discountType must be PERCENT or FIXED_AMOUNT")
	}
	if f.DiscountValue <= 0 || (f.DiscountType == TypePercent && f.DiscountValue > 100) {
		return errors.New("discountValue out of range")
	}
	if f.TotalQuota <= 0 || f.BudgetVND < 0 || f.MinOrderVND < 0 || f.MaxDiscountVND < 0 || f.PerCustomerLimit < 1 {
		return errors.New("invalid numeric limits")
	}
	if f.StartsAt != nil && f.EndsAt != nil && !f.EndsAt.After(*f.StartsAt) {
		return errors.New("endsAt must be after startsAt")
	}
	if f.Status != StatusActive && f.Status != StatusPaused {
		return errors.New("status must be ACTIVE or PAUSED")
	}
	return nil
}

// Evaluate applies B08 rules deterministically by priority ASC and stackable policy without reserving quota.
func Evaluate(campaigns []Campaign, usageByCamp map[string]int64, isNewCustomer bool, input EvalInput, now time.Time) EvalResult {
	var subtotal int64
	for _, l := range input.Lines {
		if l.Quantity > 0 && l.UnitPriceVND >= 0 {
			subtotal += l.Quantity * l.UnitPriceVND
		}
	}
	if subtotal <= 0 {
		return EvalResult{SubtotalVND: subtotal, Applied: []AppliedPromotion{}}
	}

	requested := make(map[string]bool, len(input.CampaignIDs))
	for _, id := range input.CampaignIDs {
		trimmed := strings.ToUpper(strings.TrimSpace(id))
		if trimmed != "" {
			requested[trimmed] = true
		}
	}

	var candidates []Campaign
	for _, c := range campaigns {
		if c.AutoApply || requested[strings.ToUpper(c.CampaignID)] || requested[strings.ToUpper(c.Code)] {
			candidates = append(candidates, c)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority == candidates[j].Priority {
			return candidates[i].CampaignID < candidates[j].CampaignID
		}
		return candidates[i].Priority < candidates[j].Priority
	})

	applied := []AppliedPromotion{}
	skipped := []SkippedPromotion{}
	remainingOrderPayable := subtotal
	hasNonStackable := false

	for _, c := range candidates {
		if reason := checkEligibility(c, usageByCamp[c.CampaignID], isNewCustomer, subtotal, input.Lines, now); reason != "" {
			skipped = append(skipped, SkippedPromotion{CampaignID: c.CampaignID, Code: c.Code, Reason: reason})
			continue
		}
		if len(applied) > 0 && (hasNonStackable || !c.Stackable) {
			skipped = append(skipped, SkippedPromotion{CampaignID: c.CampaignID, Code: c.Code, Reason: "NOT_STACKABLE"})
			continue
		}
		eligibleSubtotal := computeEligibleSubtotal(c, input.Lines)
		if eligibleSubtotal <= 0 {
			skipped = append(skipped, SkippedPromotion{CampaignID: c.CampaignID, Code: c.Code, Reason: "NO_ELIGIBLE_ITEMS"})
			continue
		}
		base := eligibleSubtotal
		if base > remainingOrderPayable {
			base = remainingOrderPayable
		}
		disc := computeDiscount(c, base)
		if disc <= 0 {
			skipped = append(skipped, SkippedPromotion{CampaignID: c.CampaignID, Code: c.Code, Reason: "BUDGET_EXHAUSTED"})
			continue
		}
		applied = append(applied, AppliedPromotion{
			CampaignID:   c.CampaignID,
			Code:         c.Code,
			Name:         c.Name,
			DiscountType: c.DiscountType,
			DiscountVND:  disc,
			Priority:     c.Priority,
			Stackable:    c.Stackable,
		})
		remainingOrderPayable -= disc
		if !c.Stackable {
			hasNonStackable = true
		}
		if remainingOrderPayable <= 0 {
			break
		}
	}

	var totalDisc int64
	for _, a := range applied {
		totalDisc += a.DiscountVND
	}
	return EvalResult{
		SubtotalVND:      subtotal,
		TotalDiscountVND: totalDisc,
		Applied:          applied,
		Skipped:          skipped,
	}
}

func checkEligibility(c Campaign, uses int64, isNewCustomer bool, subtotal int64, _ []EvalLine, now time.Time) string {
	if c.Status != StatusActive {
		return "CAMPAIGN_PAUSED"
	}
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return "CAMPAIGN_NOT_STARTED"
	}
	if c.EndsAt != nil && !now.Before(*c.EndsAt) {
		return "CAMPAIGN_EXPIRED"
	}
	if c.CalcAvailableQuota() <= 0 {
		return "QUOTA_EXHAUSTED"
	}
	if rem := c.CalcRemainingBudget(); rem == 0 {
		return "BUDGET_EXHAUSTED"
	}
	if uses >= int64(c.PerCustomerLimit) {
		return "CUSTOMER_LIMIT_REACHED"
	}
	if subtotal < c.MinOrderVND {
		return "MIN_ORDER_NOT_MET"
	}
	if c.CustomerSegment == SegmentNewCustomer && !isNewCustomer {
		return "SEGMENT_NEW_CUSTOMER_ONLY"
	}
	return ""
}

func computeEligibleSubtotal(c Campaign, lines []EvalLine) int64 {
	skuAllowed := toSet(c.EligibleSkuIDs)
	catAllowed := toSet(c.EligibleCategoryIDs)
	var sum int64
	for _, l := range lines {
		if l.Quantity <= 0 || l.UnitPriceVND < 0 {
			continue
		}
		if len(skuAllowed) > 0 && !skuAllowed[l.SkuID] {
			continue
		}
		if len(catAllowed) > 0 && !catAllowed[l.CategoryID] {
			continue
		}
		sum += l.Quantity * l.UnitPriceVND
	}
	return sum
}

func computeDiscount(c Campaign, eligibleBase int64) int64 {
	if eligibleBase <= 0 {
		return 0
	}
	var disc int64
	switch c.DiscountType {
	case TypePercent:
		disc = (eligibleBase * c.DiscountValue) / 100
	case TypeFixedAmount:
		disc = c.DiscountValue
	}
	if c.MaxDiscountVND > 0 && disc > c.MaxDiscountVND {
		disc = c.MaxDiscountVND
	}
	if disc > eligibleBase {
		disc = eligibleBase
	}
	if rem := c.CalcRemainingBudget(); rem >= 0 && disc > rem {
		disc = rem
	}
	return disc
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, it := range items {
		if s := strings.TrimSpace(it); s != "" {
			m[s] = true
		}
	}
	return m
}
