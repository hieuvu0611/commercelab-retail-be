package voucher

import (
	"context"
	"sync"
)

// MemoryStore is the in-memory Store for tests and the development fallback (no Postgres).
type MemoryStore struct {
	mu        sync.Mutex
	campaigns map[string]Campaign
	uses      map[string]int64 // code|user -> RESERVED + CONSUMED
}

func NewMemoryStore(seed ...Campaign) *MemoryStore {
	m := &MemoryStore{campaigns: map[string]Campaign{}, uses: map[string]int64{}}
	for _, c := range seed {
		m.campaigns[c.Code] = c
	}
	return m
}

// DevCampaigns are the development vouchers that migration 00004 seeds in Postgres.
func DevCampaigns() []Campaign {
	return []Campaign{
		{Code: "WELCOME10", Name: "Welcome 10% off", DiscountPct: 10, TotalQuota: 100, PerCustomerLimit: 100, Status: StatusActive, Version: 1},
		{Code: "LASTONE10", Name: "Last single use 10% off (SC10)", DiscountPct: 10, TotalQuota: 1, PerCustomerLimit: 1, Status: StatusActive, Version: 1},
	}
}

func (m *MemoryStore) Get(_ context.Context, code string) (Campaign, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[code]
	if !ok {
		return Campaign{}, ErrNotFound
	}
	return c, nil
}

func (m *MemoryStore) CustomerUses(_ context.Context, code, userID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.uses[code+"|"+userID], nil
}

// SetUses records how many uses a customer holds (tests).
func (m *MemoryStore) SetUses(code, userID string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uses[code+"|"+userID] = n
}
