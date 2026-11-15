package voucher

import (
	"context"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time {
	t := fixedNow.Add(d)
	return &t
}

func campaign(mutate func(*Campaign)) Campaign {
	c := Campaign{Code: "SAVE10", DiscountPct: 10, TotalQuota: 5, PerCustomerLimit: 1, Status: StatusActive}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func TestEvaluateReturnsTheReasonAVoucherCannotBeUsed(t *testing.T) {
	tests := []struct {
		name string
		c    Campaign
		uses int64
		want string
	}{
		{"eligible", campaign(nil), 0, ""},
		{"eligible inside the window", campaign(func(c *Campaign) { c.StartsAt, c.EndsAt = at(-time.Hour), at(time.Hour) }), 0, ""},
		{"paused", campaign(func(c *Campaign) { c.Status = StatusPaused }), 0, ReasonNotActive},
		{"not started", campaign(func(c *Campaign) { c.StartsAt = at(time.Minute) }), 0, ReasonNotStarted},
		{"starts exactly now", campaign(func(c *Campaign) { c.StartsAt = at(0) }), 0, ""},
		{"expired", campaign(func(c *Campaign) { c.EndsAt = at(-time.Second) }), 0, ReasonExpired},
		{"ends exactly now is over", campaign(func(c *Campaign) { c.EndsAt = at(0) }), 0, ReasonExpired},
		{"all quota reserved", campaign(func(c *Campaign) { c.ReservedQuota = 5 }), 0, ReasonExhausted},
		{"quota split between reserved and consumed", campaign(func(c *Campaign) { c.ReservedQuota, c.ConsumedQuota = 2, 3 }), 0, ReasonExhausted},
		{"last use still available", campaign(func(c *Campaign) { c.ReservedQuota, c.ConsumedQuota = 2, 2 }), 0, ""},
		{"customer limit reached", campaign(nil), 1, ReasonLimitReached},
		{"customer under a higher limit", campaign(func(c *Campaign) { c.PerCustomerLimit = 3 }), 2, ""},
		{"paused wins over exhausted", campaign(func(c *Campaign) { c.Status, c.ConsumedQuota = StatusPaused, 5 }), 0, ReasonNotActive},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.c, tc.uses, fixedNow); got != tc.want {
				t.Fatalf("Evaluate = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestCheckNormalizesCodesAndNeverMutatesQuota(t *testing.T) {
	store := NewMemoryStore(campaign(nil))
	svc := NewService(store).WithClock(func() time.Time { return fixedNow })
	ctx := context.Background()

	for range 3 {
		got, err := svc.Check(ctx, "  save10 ", "user-1")
		if err != nil || !got.Eligible || got.Code != "SAVE10" || got.DiscountPct != 10 || got.Reason != "" {
			t.Fatalf("Check = %+v, %v", got, err)
		}
	}
	after, _ := store.Get(ctx, "SAVE10")
	if after.ReservedQuota != 0 || after.ConsumedQuota != 0 || after.Available() != 5 {
		t.Fatalf("eligibility must not reserve anything: %+v", after)
	}
}

func TestCheckAnswersUnknownAndMalformedCodesAsNotFound(t *testing.T) {
	svc := NewService(NewMemoryStore(campaign(nil))).WithClock(func() time.Time { return fixedNow })
	for _, code := range []string{"NOPE", "", "bad code!", "SAVE10; DROP TABLE x", ".*", "A"} {
		got, err := svc.Check(context.Background(), code, "user-1")
		if err != nil || got.Eligible || got.Reason != ReasonNotFound {
			t.Fatalf("Check(%q) = %+v, %v", code, got, err)
		}
	}
}

func TestCheckAppliesThePerCustomerLimit(t *testing.T) {
	store := NewMemoryStore(campaign(nil))
	store.SetUses("SAVE10", "user-1", 1)
	svc := NewService(store).WithClock(func() time.Time { return fixedNow })

	if got, _ := svc.Check(context.Background(), "SAVE10", "user-1"); got.Eligible || got.Reason != ReasonLimitReached {
		t.Fatalf("user-1: %+v", got)
	}
	if got, _ := svc.Check(context.Background(), "SAVE10", "user-2"); !got.Eligible {
		t.Fatalf("user-2: %+v", got)
	}
}
