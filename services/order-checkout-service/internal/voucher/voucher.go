// Package voucher holds the voucher campaigns, the single source of truth for discount rules (P08, P12).
// Looking a voucher up or checking eligibility never reserves quota; a use is reserved only with an order (SC10).
package voucher

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Campaign statuses.
const (
	StatusActive = "ACTIVE"
	StatusPaused = "PAUSED"
)

// Reasons a voucher cannot be used (the API error codes).
const (
	ReasonNotFound     = "VOUCHER_NOT_FOUND"
	ReasonNotActive    = "VOUCHER_NOT_ACTIVE"
	ReasonNotStarted   = "VOUCHER_NOT_STARTED"
	ReasonExpired      = "VOUCHER_EXPIRED"
	ReasonExhausted    = "VOUCHER_EXHAUSTED"
	ReasonLimitReached = "VOUCHER_LIMIT_REACHED"
)

// ErrNotFound means no campaign has the code.
var ErrNotFound = errors.New("voucher campaign not found")

// Campaign is one voucher campaign with its three quota buckets (P12).
type Campaign struct {
	Code             string
	Name             string
	DiscountPct      int
	TotalQuota       int64
	ReservedQuota    int64
	ConsumedQuota    int64
	PerCustomerLimit int
	StartsAt         *time.Time // nil = open from the start
	EndsAt           *time.Time // nil = no end
	Status           string
	Version          int64
	CreatedBy        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Available is the quota that is neither reserved nor consumed.
func (c Campaign) Available() int64 { return c.TotalQuota - c.ReservedQuota - c.ConsumedQuota }

// Store reads campaigns and customer usage.
type Store interface {
	Get(ctx context.Context, code string) (Campaign, error)
	// CustomerUses counts the uses a customer holds or consumed (RESERVED and CONSUMED, not RELEASED).
	CustomerUses(ctx context.Context, code, userID string) (int64, error)
}

// NormalizeCode trims and upper-cases a voucher code typed by a customer.
func NormalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// Eligibility is the answer to "can this customer use this voucher now". Reason is empty when eligible.
type Eligibility struct {
	Code        string `json:"code"`
	Eligible    bool   `json:"eligible"`
	Reason      string `json:"reason,omitempty"`
	DiscountPct int    `json:"discountPct,omitempty"`
}

// Evaluate applies the campaign rules at time now, given how many uses the customer already has.
func Evaluate(c Campaign, customerUses int64, now time.Time) string {
	switch {
	case c.Status != StatusActive:
		return ReasonNotActive
	case c.StartsAt != nil && now.Before(*c.StartsAt):
		return ReasonNotStarted
	case c.EndsAt != nil && !now.Before(*c.EndsAt):
		return ReasonExpired
	case c.Available() <= 0:
		return ReasonExhausted
	case customerUses >= int64(c.PerCustomerLimit):
		return ReasonLimitReached
	}
	return ""
}

// Service answers eligibility questions. It only reads: no quota is reserved or changed.
type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Check reports whether userID can use the voucher now. An unknown or malformed code is a normal answer
// (not eligible, VOUCHER_NOT_FOUND), not an error; errors are store failures.
func (s *Service) Check(ctx context.Context, code, userID string) (Eligibility, error) {
	code = NormalizeCode(code)
	if !ValidCode(code) {
		return Eligibility{Code: code, Reason: ReasonNotFound}, nil
	}
	campaign, err := s.store.Get(ctx, code)
	if errors.Is(err, ErrNotFound) {
		return Eligibility{Code: code, Reason: ReasonNotFound}, nil
	}
	if err != nil {
		return Eligibility{}, err
	}
	uses, err := s.store.CustomerUses(ctx, code, userID)
	if err != nil {
		return Eligibility{}, err
	}
	if reason := Evaluate(campaign, uses, s.now()); reason != "" {
		return Eligibility{Code: code, Reason: reason}, nil
	}
	return Eligibility{Code: code, Eligible: true, DiscountPct: campaign.DiscountPct}, nil
}
