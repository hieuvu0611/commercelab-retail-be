package voucher

import (
	"context"
	"errors"
	"testing"
	"time"
)

func validFields() Fields {
	return Fields{Name: "Spring sale", DiscountPct: 10, TotalQuota: 100, PerCustomerLimit: 1, Status: StatusActive}
}

func TestFieldsValidation(t *testing.T) {
	start := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Fields)
		field  string
	}{
		{"blank name", func(f *Fields) { f.Name = "  " }, "name"},
		{"discount 0", func(f *Fields) { f.DiscountPct = 0 }, "discountPct"},
		{"discount 101", func(f *Fields) { f.DiscountPct = 101 }, "discountPct"},
		{"quota 0", func(f *Fields) { f.TotalQuota = 0 }, "totalQuota"},
		{"negative limit", func(f *Fields) { f.PerCustomerLimit = -1 }, "perCustomerLimit"},
		{"window reversed", func(f *Fields) { f.StartsAt, f.EndsAt = &start, &start }, "endsAt"},
		{"unknown status", func(f *Fields) { f.Status = "DELETED" }, "status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := validFields()
			tc.mutate(&f)
			v := f.Validate()
			if v == nil || len(v.Fields[tc.field]) == 0 {
				t.Fatalf("Validate = %+v; want a problem with %s", v, tc.field)
			}
		})
	}
	f := validFields()
	if v := f.Validate(); v != nil {
		t.Fatalf("valid fields rejected: %+v", v)
	}
}

func TestCreateRejectsMalformedCodes(t *testing.T) {
	admin := NewAdmin(NewMemoryStore())
	for _, code := range []string{"", "ab", "has space", "semi;colon", "x.*", "WAYTOOLONGCODEWAYTOOLONGCODEWAYTOOLONG"} {
		if _, err := admin.Create(context.Background(), code, "staff-marketing", validFields()); err == nil {
			t.Fatalf("code %q was accepted", code)
		}
	}
	got, err := admin.Create(context.Background(), " spring-10 ", "staff-marketing", validFields())
	if err != nil || got.Code != "SPRING-10" {
		t.Fatalf("normalized code: %+v, %v", got, err)
	}
}

func TestEscapeLikeTurnsWildcardsIntoPlainText(t *testing.T) {
	if got := EscapeLike(`50%_off\`); got != `50\%\_off\\` {
		t.Fatalf("EscapeLike = %q", got)
	}
}

func TestMemoryAdminStoreFollowsTheCampaignRules(t *testing.T) {
	store := NewMemoryStore()
	runAdminStoreContract(t, store, func(code string, reserved, consumed int64) { store.SetBuckets(code, reserved, consumed) })
}

// runAdminStoreContract is shared with the Postgres integration test so both stores behave the same.
// setBuckets simulates the checkout saga moving quota into the reserved and consumed buckets.
func runAdminStoreContract(t *testing.T, store AdminStore, setBuckets func(code string, reserved, consumed int64)) {
	t.Helper()
	ctx := context.Background()
	admin := NewAdmin(store)
	suffix := time.Now().Format("150405")
	codeA, codeB := "CTR-A-"+suffix, "CTR-B-"+suffix

	a, err := admin.Create(ctx, codeA, "staff-marketing", validFields())
	if err != nil || a.Version != 1 || a.CreatedBy != "staff-marketing" || a.Status != StatusActive || a.PerCustomerLimit != 1 {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if _, err := admin.Create(ctx, codeA, "staff-marketing", validFields()); !errors.Is(err, ErrCodeExists) {
		t.Fatalf("duplicate code: %v", err)
	}
	other := validFields()
	other.Name = "100% wild_card"
	if _, err := admin.Create(ctx, codeB, "staff-marketing", other); err != nil {
		t.Fatal(err)
	}

	// Search treats user text literally: "%" and "_" are not wildcards.
	for q, want := range map[string]int{"CTR-A-" + suffix: 1, "ctr-b-" + suffix: 1, "100%": 1, "wild_card": 1, "0% w": 1, "w_ld": 0, "wild%card": 0, "nothing-like-this": 0} {
		got, total, err := admin.List(ctx, ListFilter{Q: q})
		if err != nil || int(total) != want || len(got) != want {
			t.Fatalf("List(q=%q) = %d rows, total %d, %v; want %d", q, len(got), total, err, want)
		}
	}
	if _, total, _ := admin.List(ctx, ListFilter{Q: suffix, Status: StatusPaused}); total != 0 {
		t.Fatalf("status filter: total %d", total)
	}
	page1, total, _ := admin.List(ctx, ListFilter{Q: suffix, Page: 1, Limit: 1})
	page2, _, _ := admin.List(ctx, ListFilter{Q: suffix, Page: 2, Limit: 1})
	if total != 2 || len(page1) != 1 || len(page2) != 1 || page1[0].Code == page2[0].Code {
		t.Fatalf("paging: total %d, %+v, %+v", total, page1, page2)
	}

	// Pausing and editing bump the version and keep the code.
	edit := validFields()
	edit.Status, edit.Name, edit.TotalQuota = StatusPaused, "Renamed", 50
	got, err := admin.Update(ctx, codeA, edit)
	if err != nil || got.Version != 2 || got.Status != StatusPaused || got.Name != "Renamed" || got.TotalQuota != 50 {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if _, err := admin.Update(ctx, "CTR-NOPE-"+suffix, edit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}

	// Once quota is reserved or consumed: the discount is frozen and the quota cannot drop below use (P12).
	setBuckets(codeA, 3, 4)
	edit.DiscountPct = 20
	if _, err := admin.Update(ctx, codeA, edit); !errors.Is(err, ErrInUse) {
		t.Fatalf("discount change after use: %v", err)
	}
	edit.DiscountPct, edit.TotalQuota = 10, 6
	if _, err := admin.Update(ctx, codeA, edit); !errors.Is(err, ErrQuotaBelowUse) {
		t.Fatalf("quota below use: %v", err)
	}
	edit.TotalQuota = 7
	if got, err = admin.Update(ctx, codeA, edit); err != nil || got.Available() != 0 || got.ReservedQuota != 3 || got.ConsumedQuota != 4 {
		t.Fatalf("quota equal to use is allowed and keeps the buckets: %+v, %v", got, err)
	}
	if _, err := admin.Get(ctx, "CTR-NOPE-"+suffix); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get unknown: %v", err)
	}
}
