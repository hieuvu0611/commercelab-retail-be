package voucher

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxNameLength    = 255
	maxPageLimit     = 100
	defaultPageLimit = 20
)

var (
	ErrCodeExists = errors.New("a voucher with this code already exists")
	// ErrInUse means the discount percentage cannot change because quota is already reserved or consumed.
	ErrInUse = errors.New("the discount cannot change after the voucher was reserved or used")
	// ErrQuotaBelowUse means the new total quota is below what is already reserved plus consumed (P12).
	ErrQuotaBelowUse = errors.New("the total quota cannot be below the reserved and consumed uses")
)

// Fields are the editable properties of a campaign, shared by create and update.
type Fields struct {
	Name             string
	DiscountPct      int
	TotalQuota       int64
	PerCustomerLimit int
	StartsAt         *time.Time
	EndsAt           *time.Time
	Status           string
}

// ListFilter selects campaigns for the admin list. Q matches the code or the name, case-insensitively, as plain
// text (never as a pattern).
type ListFilter struct {
	Q      string
	Status string
	Page   int
	Limit  int
}

// AdminStore is what Marketing's campaign management needs.
type AdminStore interface {
	Store
	Create(ctx context.Context, code, actor string, f Fields) (Campaign, error)
	List(ctx context.Context, f ListFilter) ([]Campaign, int64, error)
	Update(ctx context.Context, code string, f Fields) (Campaign, error)
}

// ValidationError lists invalid input fields.
type ValidationError struct{ Fields map[string][]string }

func (e *ValidationError) Error() string { return "voucher request is invalid" }

// Validate checks the editable fields (the code is checked separately on create).
func (f *Fields) Validate() *ValidationError {
	problems := map[string][]string{}
	f.Name = strings.TrimSpace(f.Name)
	switch {
	case f.Name == "":
		problems["name"] = []string{"name is required"}
	case utf8.RuneCountInString(f.Name) > maxNameLength:
		problems["name"] = []string{"name must be at most 255 characters"}
	}
	if f.DiscountPct < 1 || f.DiscountPct > 100 {
		problems["discountPct"] = []string{"discountPct must be a whole number from 1 to 100"}
	}
	if f.TotalQuota < 1 {
		problems["totalQuota"] = []string{"totalQuota must be at least 1"}
	}
	if f.PerCustomerLimit < 1 {
		problems["perCustomerLimit"] = []string{"perCustomerLimit must be at least 1"}
	}
	if f.StartsAt != nil && f.EndsAt != nil && !f.EndsAt.After(*f.StartsAt) {
		problems["endsAt"] = []string{"endsAt must be after startsAt"}
	}
	if f.Status != StatusActive && f.Status != StatusPaused {
		problems["status"] = []string{"status must be ACTIVE or PAUSED"}
	}
	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Fields: problems}
}

// Normalize applies defaults of an absent optional field before validation.
func (f *Fields) Normalize() {
	if f.Status == "" {
		f.Status = StatusActive
	}
	if f.PerCustomerLimit == 0 {
		f.PerCustomerLimit = 1
	}
}

// applyUpdate returns c with f applied, or the rule it breaks. The stores share it so both enforce the same rules.
func applyUpdate(c Campaign, f Fields, now time.Time) (Campaign, error) {
	used := c.ReservedQuota + c.ConsumedQuota
	if f.DiscountPct != c.DiscountPct && used > 0 {
		return Campaign{}, ErrInUse
	}
	if f.TotalQuota < used {
		return Campaign{}, ErrQuotaBelowUse
	}
	c.Name, c.DiscountPct, c.TotalQuota, c.PerCustomerLimit = f.Name, f.DiscountPct, f.TotalQuota, f.PerCustomerLimit
	c.StartsAt, c.EndsAt, c.Status = f.StartsAt, f.EndsAt, f.Status
	c.Version++
	c.UpdatedAt = now
	return c, nil
}

// Admin manages campaigns for Marketing. Vouchers are never deleted; pausing stops their use.
type Admin struct {
	store AdminStore
}

func NewAdmin(store AdminStore) *Admin { return &Admin{store: store} }

// Create validates and stores a new campaign owned by actor.
func (a *Admin) Create(ctx context.Context, code, actor string, f Fields) (Campaign, error) {
	f.Normalize()
	problems := map[string][]string{}
	if v := f.Validate(); v != nil {
		problems = v.Fields
	}
	code = NormalizeCode(code)
	if !ValidCode(code) {
		problems["code"] = []string{"code must be 3 to 32 characters: A-Z, 0-9, underscore or hyphen"}
	}
	if len(problems) > 0 {
		return Campaign{}, &ValidationError{Fields: problems}
	}
	return a.store.Create(ctx, code, actor, f)
}

// Update replaces the editable fields of an existing campaign.
func (a *Admin) Update(ctx context.Context, code string, f Fields) (Campaign, error) {
	f.Normalize()
	if v := f.Validate(); v != nil {
		return Campaign{}, v
	}
	return a.store.Update(ctx, NormalizeCode(code), f)
}

// Get returns one campaign with its quota buckets.
func (a *Admin) Get(ctx context.Context, code string) (Campaign, error) {
	return a.store.Get(ctx, NormalizeCode(code))
}

// List returns one page of campaigns and the total count.
func (a *Admin) List(ctx context.Context, f ListFilter) ([]Campaign, int64, error) {
	f.Q = strings.TrimSpace(f.Q)
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = defaultPageLimit
	}
	f.Limit = min(f.Limit, maxPageLimit)
	return a.store.List(ctx, f)
}

// EscapeLike makes user text match literally inside a LIKE/ILIKE pattern that uses `\` as its escape character.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
