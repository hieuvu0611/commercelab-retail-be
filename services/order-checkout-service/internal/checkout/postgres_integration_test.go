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
	svc := New(Deps{Orders: orders.NewPostgresStore(pool), Quotes: quotes, Stock: stock, Payments: payments, Vouchers: vouchers, WarehouseID: "WH-HCM-01"})

	// SC04: twenty concurrent clicks with one key make one order and one reservation of everything.
	a := Principal{UserID: userA, Bearer: "tok"}
	quotes.quote.UserID = userA
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, err := svc.Place(ctx, a, "key-"+run, "Q-1")
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
	b, _, err := svc.Place(ctx, Principal{UserID: userB, Bearer: "tok"}, "key-b-"+run, "Q-2")
	if err != nil || b.Status != string(saga.StateFailedClosed) || b.FailureCode != voucher.ReasonExhausted {
		t.Fatalf("second customer = %+v, %v", b, err)
	}
	c, _ := vouchers.Get(ctx, code)
	if c.ReservedQuota != 1 || c.ConsumedQuota != 0 {
		t.Fatalf("buckets reserved=%d consumed=%d, want 1 and 0", c.ReservedQuota, c.ConsumedQuota)
	}
}
