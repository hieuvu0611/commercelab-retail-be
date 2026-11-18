//go:build integration

package checkout

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

// The order and voucher stores are real; stock, payment and the quote are fakes.
func TestPlaceOrderOnPostgresSC04AndSC10(t *testing.T) {
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

	run := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	code := "CHK-" + run
	userA, userB := "it-chk-a-"+run, "it-chk-b-"+run
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM orders.voucher_usages WHERE voucher_code = $1`, code)
		_, _ = pool.Exec(bg, `DELETE FROM orders.voucher_campaigns WHERE voucher_code = $1`, code)
		_, _ = pool.Exec(bg, `DELETE FROM orders.idempotency_keys WHERE user_id IN ($1, $2)`, userA, userB)
		_, _ = pool.Exec(bg, `DELETE FROM orders.orders WHERE user_id IN ($1, $2)`, userA, userB)
	})
	vouchers := voucher.NewPostgresStore(pool)
	if _, err := voucher.NewAdmin(vouchers).Create(ctx, code, "it", voucher.Fields{Name: "SC10", DiscountPct: 10, TotalQuota: 1, PerCustomerLimit: 1, Status: voucher.StatusActive}); err != nil {
		t.Fatal(err)
	}

	quotes := &fakeQuotes{quote: sc01Quote()}
	quotes.quote.VoucherCode = code
	stock, payments := &fakeStock{}, &fakePayments{}
	svc := New(Deps{Orders: orders.NewPostgresStore(pool), Quotes: quotes, Stock: stock, Payments: payments, Vouchers: vouchers,
		Addresses: &fakeAddresses{}, Snapshots: snapshot.NewMemoryStore(), WarehouseID: "WH-HCM-01"}).
		WithClock(func() time.Time { return now }) // the fake quote expires relative to the fixed test clock

	// SC04: twenty concurrent clicks with one key make one order and one reservation of everything.
	a := Principal{UserID: userA, Bearer: "tok"}
	quotes.quote.UserID = userA
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, err := svc.Place(ctx, a, "key-"+run, "Q-1", "")
			if err != nil {
				t.Errorf("Place: %v", err)
				return
			}
			ids <- o.OrderID
		}()
	}
	wg.Wait()
	close(ids)
	distinct := map[string]bool{}
	for id := range ids {
		distinct[id] = true
	}
	if len(distinct) != 1 || stock.reserves != 1 || payments.calls != 1 {
		t.Fatalf("orders=%d stock=%d payments=%d, want 1 each", len(distinct), stock.reserves, payments.calls)
	}

	// SC10: the only voucher use is held, so another customer's order is closed with VOUCHER_EXHAUSTED.
	quotes.quote.UserID = userB
	b, _, err := svc.Place(ctx, Principal{UserID: userB, Bearer: "tok"}, "key-b-"+run, "Q-2", "")
	if err != nil || b.Status != string(saga.StateFailedClosed) || b.FailureCode != voucher.ReasonExhausted {
		t.Fatalf("second customer = %+v, %v", b, err)
	}
	c, _ := vouchers.Get(ctx, code)
	if c.ReservedQuota != 1 || c.ConsumedQuota != 0 {
		t.Fatalf("buckets reserved=%d consumed=%d, want 1 and 0", c.ReservedQuota, c.ConsumedQuota)
	}
}

// Cancellation on the real order and voucher stores: five "instances" (own locks, shared Postgres) cancel at once.
func TestConcurrentCancelsOnPostgresAreOneCancellation(t *testing.T) {
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
	run := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	user := "it-cancel-" + run
	for _, paid := range []bool{true, false} {
		code := fmt.Sprintf("CAN-%s-%s", map[bool]string{true: "PAID", false: "UNPAID"}[paid], run)
		t.Cleanup(func() {
			bg := context.Background()
			_, _ = pool.Exec(bg, `DELETE FROM orders.voucher_usages WHERE voucher_code = $1`, code)
			_, _ = pool.Exec(bg, `DELETE FROM orders.voucher_campaigns WHERE voucher_code = $1`, code)
			_, _ = pool.Exec(bg, `DELETE FROM orders.idempotency_keys WHERE user_id = $1`, user)
			_, _ = pool.Exec(bg, `DELETE FROM orders.outbox_events WHERE payload->>'userId' = $1`, user)
			_, _ = pool.Exec(bg, `DELETE FROM orders.orders WHERE user_id = $1`, user)
		})
		vouchers := voucher.NewPostgresStore(pool)
		if _, err := voucher.NewAdmin(vouchers).Create(ctx, code, "it", voucher.Fields{Name: "cancel", DiscountPct: 10, TotalQuota: 5, PerCustomerLimit: 5, Status: voucher.StatusActive}); err != nil {
			t.Fatal(err)
		}
		quotes := &fakeQuotes{quote: sc01Quote()}
		quotes.quote.VoucherCode, quotes.quote.UserID = code, user
		stock, payments, ful := &fakeStock{}, &fakePayments{}, &fakeFulfillment{result: StopStopped}
		build := func() *Service {
			return New(Deps{Orders: orders.NewPostgresStore(pool), Quotes: quotes, Stock: stock, Payments: payments, Vouchers: vouchers,
				Addresses: &fakeAddresses{}, Snapshots: snapshot.NewMemoryStore(), Fulfillment: ful, WarehouseID: "WH-HCM-01"}).
				WithClock(func() time.Time { return now })
		}
		o, _, err := build().Place(ctx, Principal{UserID: user, Bearer: "tok"}, "key-"+code, "Q-1", "")
		if err != nil || o.Status != string(saga.StateReserved) {
			t.Fatalf("place = %+v %v", o, err)
		}
		if paid {
			if o, err = build().ApplyPayment(ctx, o.OrderID, report(o, "CAPTURED", 660000)); err != nil || o.Status != "CONFIRMED" {
				t.Fatalf("capture = %+v %v", o, err)
			}
		}
		services := make([]*Service, 5)
		for i := range services {
			services[i] = build()
		}
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := services[i%5].Cancel(ctx, user, o.OrderID)
				if err != nil || got.Status != "CANCELLED" {
					t.Errorf("cancel = %+v %v", got, err)
				}
			}()
		}
		wg.Wait()
		var cancelled int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders.outbox_events WHERE event_type = 'order.cancelled.v1' AND aggregate_id = $1`, o.OrderID).Scan(&cancelled); err != nil || cancelled != 1 {
			t.Fatalf("paid=%v: order.cancelled events = %d (%v), want exactly one", paid, cancelled, err)
		}
		c, _ := vouchers.Get(ctx, code)
		wantReserved, wantConsumed := int64(0), int64(0)
		if paid {
			wantConsumed = 1 // a paid cancellation never restores the voucher use
		}
		if c.ReservedQuota != wantReserved || c.ConsumedQuota != wantConsumed {
			t.Fatalf("paid=%v: voucher reserved=%d consumed=%d, want %d and %d", paid, c.ReservedQuota, c.ConsumedQuota, wantReserved, wantConsumed)
		}
		final, _ := orders.NewPostgresStore(pool).Get(ctx, o.OrderID)
		if paid && (final.PaymentStatus != PayRefundPending || final.RefundPendingVND != 660000) || !paid && final.PaymentStatus != saga.PayUnpaid {
			t.Fatalf("paid=%v: final order = %+v", paid, final)
		}
		// A new order for the same user in the next round must not see this one.
		_, _ = pool.Exec(ctx, `DELETE FROM orders.idempotency_keys WHERE user_id = $1`, user)
	}
}

// SC06 on the real stores: a waiting cancellation settled by five instances at once is one cancellation (branch
// A) or one rejection (branch B); never both, never twice.
func TestConcurrentSettlesOnPostgresAreOneOutcome_SC06(t *testing.T) {
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
	run := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	for _, branch := range []string{"A", "B"} {
		user, code := "it-sc06-"+branch+"-"+run, "SC06-"+branch+"-"+run
		t.Cleanup(func() {
			bg := context.Background()
			_, _ = pool.Exec(bg, `DELETE FROM orders.voucher_usages WHERE voucher_code = $1`, code)
			_, _ = pool.Exec(bg, `DELETE FROM orders.voucher_campaigns WHERE voucher_code = $1`, code)
			_, _ = pool.Exec(bg, `DELETE FROM orders.idempotency_keys WHERE user_id = $1`, user)
			_, _ = pool.Exec(bg, `DELETE FROM orders.outbox_events WHERE payload->>'userId' = $1`, user)
			_, _ = pool.Exec(bg, `DELETE FROM orders.orders WHERE user_id = $1`, user)
		})
		vouchers := voucher.NewPostgresStore(pool)
		if _, err := voucher.NewAdmin(vouchers).Create(ctx, code, "it", voucher.Fields{Name: "sc06", DiscountPct: 10, TotalQuota: 5, PerCustomerLimit: 5, Status: voucher.StatusActive}); err != nil {
			t.Fatal(err)
		}
		quotes := &fakeQuotes{quote: sc01Quote()}
		quotes.quote.VoucherCode, quotes.quote.UserID = code, user
		stock, payments, ful := &fakeStock{}, &fakePayments{}, &fakeFulfillment{result: StopRequested}
		build := func() *Service {
			return New(Deps{Orders: orders.NewPostgresStore(pool), Quotes: quotes, Stock: stock, Payments: payments, Vouchers: vouchers,
				Addresses: &fakeAddresses{}, Snapshots: snapshot.NewMemoryStore(), Fulfillment: ful, WarehouseID: "WH-HCM-01"}).
				WithClock(func() time.Time { return now })
		}
		o, _, err := build().Place(ctx, Principal{UserID: user, Bearer: "tok"}, "key-"+code, "Q-1", "")
		if err != nil {
			t.Fatal(err)
		}
		if o, err = build().ApplyPayment(ctx, o.OrderID, report(o, "CAPTURED", 660000)); err != nil || o.Status != "CONFIRMED" {
			t.Fatalf("capture = %+v %v", o, err)
		}
		if got, err := build().Cancel(ctx, user, o.OrderID); err != nil || got.Status != string(saga.StateCancelRequested) {
			t.Fatalf("request = %+v %v", got, err)
		}
		ful.result = map[string]StopResult{"A": StopStopped, "B": StopTooLate}[branch]
		services := make([]*Service, 5)
		for i := range services {
			services[i] = build()
		}
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := services[i%5].SettleCancel(ctx, o.OrderID); err != nil {
					t.Errorf("settle: %v", err)
				}
			}()
		}
		wg.Wait()
		count := func(typ string) (n int) {
			_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders.outbox_events WHERE event_type = $1 AND aggregate_id = $2`, typ, o.OrderID).Scan(&n)
			return n
		}
		final, _ := orders.NewPostgresStore(pool).Get(ctx, o.OrderID)
		c, _ := vouchers.Get(ctx, code)
		if c.ConsumedQuota != 1 || c.ReservedQuota != 0 {
			t.Fatalf("branch %s: voucher reserved=%d consumed=%d, want the use kept", branch, c.ReservedQuota, c.ConsumedQuota)
		}
		if branch == "A" && (final.Status != "CANCELLED" || final.RefundPendingVND != 660000 || count("order.cancelled.v1") != 1 || count("order.cancel_rejected.v1") != 0) {
			t.Fatalf("branch A: order = %+v cancelled=%d", final, count("order.cancelled.v1"))
		}
		if branch == "B" && (final.Status != "IN_FULFILLMENT" || final.RefundPendingVND != 0 || count("order.cancel_rejected.v1") != 1 || count("order.cancelled.v1") != 0) {
			t.Fatalf("branch B: order = %+v rejected=%d", final, count("order.cancel_rejected.v1"))
		}
	}
}
