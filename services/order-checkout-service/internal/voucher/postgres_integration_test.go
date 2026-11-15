//go:build integration

package voucher

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestPostgresStoreReadsSeededCampaignsAndCustomerUsage(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_ORDERS")
	if dsn == "" {
		t.Skip("DATABASE_URL_ORDERS is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	store := NewPostgresStore(pool)
	svc := NewService(store)

	welcome, err := store.Get(ctx, "WELCOME10")
	if err != nil || welcome.DiscountPct != 10 || welcome.Status != StatusActive || welcome.Available() <= 0 {
		t.Fatalf("seeded WELCOME10 = %+v, %v", welcome, err)
	}
	last, err := store.Get(ctx, "LASTONE10")
	if err != nil || last.TotalQuota != 1 || last.PerCustomerLimit != 1 {
		t.Fatalf("seeded LASTONE10 = %+v, %v", last, err)
	}
	if _, err := store.Get(ctx, "NOPE"); err != ErrNotFound {
		t.Fatalf("unknown code: %v", err)
	}

	// Customer usage counts RESERVED and CONSUMED, not RELEASED, and never changes the campaign.
	user := "it-voucher-" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders.voucher_usages WHERE user_id = $1`, user)
	})
	for i, state := range []string{"RESERVED", "RELEASED"} {
		if _, err := pool.Exec(ctx, `INSERT INTO orders.voucher_usages (voucher_code, user_id, order_id, state) VALUES ('LASTONE10', $1, $2, $3)`,
			user, user+"-"+string(rune('a'+i)), state); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := store.CustomerUses(ctx, "LASTONE10", user); err != nil || n != 1 {
		t.Fatalf("uses = %d, %v; want 1 (the released one does not count)", n, err)
	}
	got, err := svc.Check(ctx, "lastone10", user)
	if err != nil || got.Eligible || got.Reason != ReasonLimitReached {
		t.Fatalf("Check with a held use = %+v, %v", got, err)
	}
	after, _ := store.Get(ctx, "LASTONE10")
	if after.ReservedQuota != last.ReservedQuota || after.ConsumedQuota != last.ConsumedQuota || after.Version != last.Version {
		t.Fatalf("eligibility must not change the campaign: before %+v after %+v", last, after)
	}
}

func TestPostgresAdminStoreFollowsTheCampaignRules(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_ORDERS")
	if dsn == "" {
		t.Skip("DATABASE_URL_ORDERS is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders.voucher_campaigns WHERE voucher_code LIKE 'CTR-%'`)
	})
	runAdminStoreContract(t, NewPostgresStore(pool), func(code string, reserved, consumed int64) {
		if _, err := pool.Exec(ctx, `UPDATE orders.voucher_campaigns SET reserved_quota = $2, consumed_quota = $3 WHERE voucher_code = $1`, code, reserved, consumed); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPostgresStoreReserveContract(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_ORDERS")
	if dsn == "" {
		t.Skip("DATABASE_URL_ORDERS is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders.voucher_usages WHERE voucher_code LIKE 'RSV-%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM orders.voucher_campaigns WHERE voucher_code LIKE 'RSV-%'`)
	})
	runReserveContract(t, NewPostgresStore(pool), "RSV")
}
