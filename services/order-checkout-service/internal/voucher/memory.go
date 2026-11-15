package voucher

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store for tests and the development fallback (no Postgres).
type MemoryStore struct {
	mu        sync.Mutex
	campaigns map[string]Campaign
	uses      map[string]int64    // code|user -> RESERVED + CONSUMED
	usages    map[string]memUsage // code|order -> the use an order holds
}

type memUsage struct {
	user, state string
	at          time.Time
}

func NewMemoryStore(seed ...Campaign) *MemoryStore {
	m := &MemoryStore{campaigns: map[string]Campaign{}, uses: map[string]int64{}, usages: map[string]memUsage{}}
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

func (m *MemoryStore) Create(_ context.Context, code, actor string, f Fields) (Campaign, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.campaigns[code]; exists {
		return Campaign{}, ErrCodeExists
	}
	now := time.Now().UTC()
	c := Campaign{
		Code: code, Name: f.Name, DiscountPct: f.DiscountPct, TotalQuota: f.TotalQuota, PerCustomerLimit: f.PerCustomerLimit,
		StartsAt: f.StartsAt, EndsAt: f.EndsAt, Status: f.Status, Version: 1, CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	m.campaigns[code] = c
	return c, nil
}

func (m *MemoryStore) List(_ context.Context, f ListFilter) ([]Campaign, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(f.Q)
	var all []Campaign
	for _, c := range m.campaigns {
		if f.Status != "" && c.Status != f.Status {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(c.Code), q) && !strings.Contains(strings.ToLower(c.Name), q) {
			continue
		}
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].Code < all[j].Code
	})
	total := int64(len(all))
	start := min((f.Page-1)*f.Limit, len(all))
	return all[start:min(start+f.Limit, len(all))], total, nil
}

func (m *MemoryStore) Update(_ context.Context, code string, f Fields) (Campaign, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[code]
	if !ok {
		return Campaign{}, ErrNotFound
	}
	updated, err := applyUpdate(c, f, time.Now().UTC())
	if err != nil {
		return Campaign{}, err
	}
	m.campaigns[code] = updated
	return updated, nil
}

// SetBuckets sets the reserved and consumed quota of a campaign (tests; the checkout saga owns them in production).
func (m *MemoryStore) SetBuckets(code string, reserved, consumed int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.campaigns[code]
	c.ReservedQuota, c.ConsumedQuota = reserved, consumed
	m.campaigns[code] = c
}

// Reserve implements Reserver.
func (m *MemoryStore) Reserve(_ context.Context, code, userID, orderID string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.campaigns[code]
	if !ok {
		return &Refusal{Reason: ReasonNotFound}
	}
	if u, held := m.usages[code+"|"+orderID]; held && u.state != "RELEASED" {
		return nil
	}
	if reason := Evaluate(c, m.uses[code+"|"+userID], now); reason != "" {
		return &Refusal{Reason: reason}
	}
	c.ReservedQuota++
	c.Version++
	m.campaigns[code] = c
	m.uses[code+"|"+userID]++
	m.usages[code+"|"+orderID] = memUsage{user: userID, state: "RESERVED", at: now}
	return nil
}

// Consume implements Reserver.
func (m *MemoryStore) Consume(_ context.Context, code, orderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, held := m.usages[code+"|"+orderID]
	if !held || u.state != "RESERVED" {
		return nil
	}
	c := m.campaigns[code]
	c.ReservedQuota--
	c.ConsumedQuota++
	c.Version++
	m.campaigns[code] = c
	m.usages[code+"|"+orderID] = memUsage{user: u.user, state: "CONSUMED", at: u.at}
	return nil
}

// Release implements Reserver.
func (m *MemoryStore) Release(_ context.Context, code, orderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, held := m.usages[code+"|"+orderID]
	if !held || u.state != "RESERVED" {
		return nil
	}
	c := m.campaigns[code]
	c.ReservedQuota--
	c.Version++
	m.campaigns[code] = c
	m.uses[code+"|"+u.user]--
	m.usages[code+"|"+orderID] = memUsage{user: u.user, state: "RELEASED", at: u.at}
	return nil
}

// ReservedBefore implements Reserver.
func (m *MemoryStore) ReservedBefore(_ context.Context, cutoff time.Time, limit int) ([]Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Reservation
	for key, u := range m.usages {
		if u.state != "RESERVED" || u.at.After(cutoff) {
			continue
		}
		code, order, _ := strings.Cut(key, "|")
		out = append(out, Reservation{Code: code, UserID: u.user, OrderID: order})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OrderID < out[j].OrderID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
