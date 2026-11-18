package promotion

import (
	"context"
	"strings"
	"sync"
	"time"
)

type usageKey struct {
	campaignID string
	orderID    string
}

// MemoryStore is an in-memory Store for unit tests and ALLOW_MEMORY_FALLBACK dev runs.
type MemoryStore struct {
	mu          sync.Mutex
	campaigns   map[string]*Campaign
	usages      map[usageKey]Usage
	newCustomer map[string]bool
}

type Usage struct {
	CampaignID  string
	UserID      string
	OrderID     string
	DiscountVND int64
	State       string
}

func NewMemoryStore(seed ...Campaign) *MemoryStore {
	m := &MemoryStore{
		campaigns:   make(map[string]*Campaign, len(seed)),
		usages:      make(map[usageKey]Usage),
		newCustomer: make(map[string]bool),
	}
	for _, c := range seed {
		cp := enrich(c)
		m.campaigns[cp.CampaignID] = &cp
	}
	return m
}

func DevCampaigns() []Campaign {
	now := time.Now().UTC()
	return []Campaign{
		enrich(Campaign{
			CampaignID:       "CAMP-TIER1M",
			Code:             "TIER1M5",
			Name:             "Auto 5% Off Orders >= 1,000,000 VND",
			DiscountType:     TypePercent,
			DiscountValue:    5,
			MaxDiscountVND:   100000,
			MinOrderVND:      1000000,
			CustomerSegment:  SegmentAll,
			AutoApply:        true,
			Stackable:        true,
			Priority:         50,
			TotalQuota:       1000,
			BudgetVND:        100000000,
			PerCustomerLimit: 5,
			Status:           StatusActive,
			Version:          1,
			CreatedBy:        "seed",
			CreatedAt:        now,
			UpdatedAt:        now,
		}),
		enrich(Campaign{
			CampaignID:       "CAMP-GEAR15",
			Code:             "GEAR15",
			Name:             "Outdoor & Travel Gear 15% Off (Max 150k)",
			DiscountType:     TypePercent,
			DiscountValue:    15,
			MaxDiscountVND:   150000,
			MinOrderVND:      300000,
			EligibleSkuIDs:   []string{"SKU-BALO-BLACK-20L", "SKU-BOTTLE-BLUE-500ML"},
			CustomerSegment:  SegmentAll,
			AutoApply:        false,
			Stackable:        false,
			Priority:         10,
			TotalQuota:       500,
			BudgetVND:        50000000,
			PerCustomerLimit: 2,
			Status:           StatusActive,
			Version:          1,
			CreatedBy:        "seed",
			CreatedAt:        now,
			UpdatedAt:        now,
		}),
	}
}

func enrich(c Campaign) Campaign {
	c.AvailableQuota = c.CalcAvailableQuota()
	rem := c.CalcRemainingBudget()
	if rem < 0 {
		c.BudgetRemainingVND = 0
	} else {
		c.BudgetRemainingVND = rem
	}
	if c.EligibleCategoryIDs == nil {
		c.EligibleCategoryIDs = []string{}
	}
	if c.EligibleSkuIDs == nil {
		c.EligibleSkuIDs = []string{}
	}
	if c.CustomerSegment == "" {
		c.CustomerSegment = SegmentAll
	}
	return c
}

func (m *MemoryStore) ListActive(_ context.Context, now time.Time) ([]Campaign, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Campaign{}
	for _, c := range m.campaigns {
		if c.Status != StatusActive {
			continue
		}
		if c.StartsAt != nil && now.Before(*c.StartsAt) {
			continue
		}
		if c.EndsAt != nil && !now.Before(*c.EndsAt) {
			continue
		}
		out = append(out, enrich(*c))
	}
	return out, nil
}

func (m *MemoryStore) List(_ context.Context, status, q string) ([]Campaign, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	qLower := strings.ToLower(strings.TrimSpace(q))
	out := []Campaign{}
	for _, c := range m.campaigns {
		if status != "" && c.Status != status {
			continue
		}
		if qLower != "" && !strings.Contains(strings.ToLower(c.Code), qLower) && !strings.Contains(strings.ToLower(c.Name), qLower) {
			continue
		}
		out = append(out, enrich(*c))
	}
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, campaignID string) (Campaign, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[campaignID]
	if !ok {
		return Campaign{}, ErrNotFound
	}
	return enrich(*c), nil
}

func (m *MemoryStore) Create(_ context.Context, campaignID, actor string, f Fields) (Campaign, error) {
	if err := ValidateFields(f, true); err != nil {
		return Campaign{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	code := NormalizeCode(f.Code)
	for _, ex := range m.campaigns {
		if ex.Code == code || ex.CampaignID == campaignID {
			return Campaign{}, ErrCodeExists
		}
	}
	now := time.Now().UTC()
	c := enrich(Campaign{
		CampaignID:          campaignID,
		Code:                code,
		Name:                strings.TrimSpace(f.Name),
		DiscountType:        f.DiscountType,
		DiscountValue:       f.DiscountValue,
		MaxDiscountVND:      f.MaxDiscountVND,
		MinOrderVND:         f.MinOrderVND,
		EligibleCategoryIDs: append([]string(nil), f.EligibleCategoryIDs...),
		EligibleSkuIDs:      append([]string(nil), f.EligibleSkuIDs...),
		CustomerSegment:     f.CustomerSegment,
		AutoApply:           f.AutoApply,
		Stackable:           f.Stackable,
		Priority:            f.Priority,
		TotalQuota:          f.TotalQuota,
		BudgetVND:           f.BudgetVND,
		PerCustomerLimit:    f.PerCustomerLimit,
		StartsAt:            f.StartsAt,
		EndsAt:              f.EndsAt,
		Status:              f.Status,
		Version:             1,
		CreatedBy:           actor,
		CreatedAt:           now,
		UpdatedAt:           now,
	})
	m.campaigns[campaignID] = &c
	return c, nil
}

func (m *MemoryStore) Update(_ context.Context, campaignID string, f Fields) (Campaign, error) {
	if err := ValidateFields(f, false); err != nil {
		return Campaign{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[campaignID]
	if !ok {
		return Campaign{}, ErrNotFound
	}
	if f.TotalQuota < c.ReservedQuota+c.ConsumedQuota {
		return Campaign{}, ErrValidation
	}
	if f.BudgetVND > 0 && f.BudgetVND < c.BudgetReservedVND+c.BudgetConsumedVND {
		return Campaign{}, ErrValidation
	}
	c.Name = strings.TrimSpace(f.Name)
	c.DiscountType = f.DiscountType
	c.DiscountValue = f.DiscountValue
	c.MaxDiscountVND = f.MaxDiscountVND
	c.MinOrderVND = f.MinOrderVND
	c.EligibleCategoryIDs = append([]string(nil), f.EligibleCategoryIDs...)
	c.EligibleSkuIDs = append([]string(nil), f.EligibleSkuIDs...)
	if f.CustomerSegment != "" {
		c.CustomerSegment = f.CustomerSegment
	}
	c.AutoApply = f.AutoApply
	c.Stackable = f.Stackable
	c.Priority = f.Priority
	c.TotalQuota = f.TotalQuota
	c.BudgetVND = f.BudgetVND
	c.PerCustomerLimit = f.PerCustomerLimit
	c.StartsAt = f.StartsAt
	c.EndsAt = f.EndsAt
	c.Status = f.Status
	c.Version++
	c.UpdatedAt = time.Now().UTC()
	return enrich(*c), nil
}

func (m *MemoryStore) CustomerUsesMap(_ context.Context, userID string, campaignIDs []string) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(campaignIDs))
	want := toSet(campaignIDs)
	for _, u := range m.usages {
		if u.UserID == userID && (u.State == "RESERVED" || u.State == "CONSUMED") {
			if len(want) == 0 || want[u.CampaignID] {
				out[u.CampaignID]++
			}
		}
	}
	return out, nil
}

func (m *MemoryStore) SetNewCustomer(userID string, isNew bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.newCustomer[userID] = isNew
}

func (m *MemoryStore) IsNewCustomer(_ context.Context, userID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.newCustomer[userID]; ok {
		return v, nil
	}
	return true, nil
}

func (m *MemoryStore) Reserve(_ context.Context, campaignID, userID, orderID string, discountVND int64, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[campaignID]
	if !ok {
		return ErrNotFound
	}
	uk := usageKey{campaignID: campaignID, orderID: orderID}
	if prev, exists := m.usages[uk]; exists && prev.State != "RELEASED" {
		return nil // idempotent
	}
	if reason := checkEligibility(*c, m.customerUsesLocked(campaignID, userID), true, c.MinOrderVND, nil, now); reason != "" {
		switch reason {
		case "QUOTA_EXHAUSTED":
			return ErrQuotaExhausted
		case "BUDGET_EXHAUSTED":
			return ErrBudgetExceeded
		case "CUSTOMER_LIMIT_REACHED":
			return ErrCustomerLimitHit
		default:
			return ErrValidation
		}
	}
	if rem := c.CalcRemainingBudget(); rem >= 0 && discountVND > rem {
		return ErrBudgetExceeded
	}
	c.ReservedQuota++
	c.BudgetReservedVND += discountVND
	c.Version++
	c.UpdatedAt = now
	m.usages[uk] = Usage{CampaignID: campaignID, UserID: userID, OrderID: orderID, DiscountVND: discountVND, State: "RESERVED"}
	return nil
}

func (m *MemoryStore) customerUsesLocked(campaignID, userID string) int64 {
	var n int64
	for _, u := range m.usages {
		if u.CampaignID == campaignID && u.UserID == userID && (u.State == "RESERVED" || u.State == "CONSUMED") {
			n++
		}
	}
	return n
}

func (m *MemoryStore) ConsumeOrder(_ context.Context, orderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, u := range m.usages {
		if k.orderID == orderID && u.State == "RESERVED" {
			c := m.campaigns[u.CampaignID]
			c.ReservedQuota--
			c.ConsumedQuota++
			c.BudgetReservedVND -= u.DiscountVND
			c.BudgetConsumedVND += u.DiscountVND
			c.Version++
			u.State = "CONSUMED"
			m.usages[k] = u
		}
	}
	return nil
}

func (m *MemoryStore) ReleaseOrder(_ context.Context, orderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, u := range m.usages {
		if k.orderID == orderID && u.State == "RESERVED" {
			c := m.campaigns[u.CampaignID]
			c.ReservedQuota--
			c.BudgetReservedVND -= u.DiscountVND
			c.Version++
			u.State = "RELEASED"
			m.usages[k] = u
		}
	}
	return nil
}
