package checkout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/snapshot"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

var now = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

type fakeQuotes struct {
	mu    sync.Mutex
	calls int
	quote Quote
	err   error
}

func (f *fakeQuotes) Get(_ context.Context, _, quoteID string) (Quote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return Quote{}, f.err
	}
	q := f.quote
	q.ID = quoteID
	return q, nil
}

type fakeStock struct {
	mu        sync.Mutex
	reserves  int
	releases  int
	commits   int
	holds     int
	commitErr error
	holdErr   error
	reserveFn func() error
	lastLines []StockLine
	lastWH    string
}

func (f *fakeStock) Reserve(_ context.Context, _ string, warehouseID string, lines []StockLine) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserves++
	f.lastLines, f.lastWH = lines, warehouseID
	if f.reserveFn != nil {
		return f.reserveFn()
	}
	return nil
}

func (f *fakeStock) Commit(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	return f.commitErr
}

func (f *fakeStock) ExceptionHold(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holds++
	return f.holdErr
}

func (f *fakeStock) Release(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return nil
}

type fakeAddresses struct {
	mu    sync.Mutex
	calls int
	err   error
	addr  snapshot.Address
}

func (f *fakeAddresses) Get(_ context.Context, _, _ string) (snapshot.Address, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.addr, f.err
}

type failingSnapshots struct{ snapshot.Store }

func (failingSnapshots) Save(context.Context, snapshot.Snapshot) error {
	return dependency.Wrap(errors.New("mongo down"))
}

type fakeCarts struct {
	mu      sync.Mutex
	removed [][]string
	err     error
}

func (f *fakeCarts) RemoveOrdered(_ context.Context, userID string, skuIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, append([]string{userID}, skuIDs...))
	return f.err
}

type fakePayments struct {
	mu       sync.Mutex
	closes   []string
	states   map[string]PaymentState
	stateErr error
	closeErr error
	calls    int
	amount   int64
	userID   string
	failure  error
}

func (f *fakePayments) CreateAttempt(_ context.Context, orderID, userID string, amountVND int64) (Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.amount, f.userID = amountVND, userID
	if f.failure != nil {
		return Attempt{}, f.failure
	}
	return Attempt{AttemptID: "PAY-" + orderID, Status: "PROCESSING"}, nil
}

func (f *fakePayments) AttemptState(_ context.Context, orderID string) (PaymentState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stateErr != nil {
		return PaymentState{}, false, f.stateErr
	}
	st, ok := f.states[orderID]
	return st, ok, nil
}

func (f *fakePayments) CloseAttempt(_ context.Context, attemptID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes = append(f.closes, attemptID)
	return f.closeErr
}

type fixture struct {
	addrs    *fakeAddresses
	snaps    *snapshot.MemoryStore
	carts    *fakeCarts
	svc      *Service
	quotes   *fakeQuotes
	stock    *fakeStock
	payments *fakePayments
	vouchers *voucher.MemoryStore
	orders   *orders.MemoryStore
}

func sc01Quote() Quote {
	return Quote{
		UserID: "user-lan", AddressID: "addr-1", VoucherCode: "WELCOME10",
		SubtotalVND: 700000, DiscountVND: 70000, ShippingFeeVND: 30000, TotalPayableVND: 660000,
		ExpiresAt: now.Add(5 * time.Minute),
		Lines: []QuoteLine{
			{SkuID: "SKU-BALO-BLACK-20L", SkuName: "Backpack", Quantity: 2, UnitPriceVND: 300000, LineDiscountVND: 60000, AllocatedUnitPaidVND: 270000},
			{SkuID: "SKU-BOTTLE-BLUE-500ML", SkuName: "Bottle", Quantity: 1, UnitPriceVND: 100000, LineDiscountVND: 10000, AllocatedUnitPaidVND: 90000},
		},
	}
}

func newFixture() *fixture {
	f := &fixture{
		quotes:   &fakeQuotes{quote: sc01Quote()},
		stock:    &fakeStock{},
		carts:    &fakeCarts{},
		addrs:    &fakeAddresses{addr: snapshot.Address{Recipient: "Nguyen Van Lan", Phone: "0901234567", Line1: "1 Le Loi", Ward: "Ben Nghe", District: "District 1", Province: "Ho Chi Minh"}},
		snaps:    snapshot.NewMemoryStore(),
		payments: &fakePayments{},
		vouchers: voucher.NewMemoryStore(voucher.DevCampaigns()...),
		orders:   orders.NewMemoryStore(),
	}
	n := 0
	var mu sync.Mutex
	f.svc = New(Deps{Orders: f.orders, Quotes: f.quotes, Stock: f.stock, Payments: f.payments, Vouchers: f.vouchers, Carts: f.carts, Addresses: f.addrs, Snapshots: f.snaps, WarehouseID: "WH-HCM-01"}).
		WithClock(func() time.Time { return now }).
		WithIDs(func() string { mu.Lock(); defer mu.Unlock(); n++; return "O-" + string(rune('A'+n)) })
	return f
}

var lan = Principal{UserID: "user-lan", Bearer: "tok-lan"}

func place(t *testing.T, f *fixture, p Principal, key, quote string) (orders.Order, bool, error) {
	t.Helper()
	return f.svc.Place(context.Background(), p, key, quote)
}

func TestPlaceOrderReservesStockVoucherAndPayment_SC01(t *testing.T) {
	f := newFixture()
	o, replayed, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil || replayed {
		t.Fatalf("Place = %v replayed=%v", err, replayed)
	}
	if o.Status != string(saga.StateReserved) || o.PaymentStatus != saga.PayProcessing || o.FailureCode != "" {
		t.Fatalf("order = %+v", o)
	}
	if o.TotalPayableVND != 660000 || len(o.Items) != 2 || o.Items[0].AllocatedUnitPaidVND != 270000 || o.WarehouseID != "WH-HCM-01" || o.AddressID != "addr-1" {
		t.Fatalf("order content = %+v", o)
	}
	if o.ExpiresAt == nil || !o.ExpiresAt.Equal(now.Add(ReservationTTL)) {
		t.Fatalf("expiresAt = %v, want %v", o.ExpiresAt, now.Add(ReservationTTL))
	}
	if f.stock.reserves != 1 || f.stock.lastWH != "WH-HCM-01" || len(f.stock.lastLines) != 2 || f.stock.lastLines[0].Quantity != 2 {
		t.Fatalf("stock calls = %d lines=%+v wh=%s", f.stock.reserves, f.stock.lastLines, f.stock.lastWH)
	}
	if f.payments.calls != 1 || f.payments.amount != 660000 || f.payments.userID != "user-lan" {
		t.Fatalf("payment calls = %d amount=%d user=%s", f.payments.calls, f.payments.amount, f.payments.userID)
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ReservedQuota != 1 {
		t.Fatalf("voucher reserved = %d, want 1", c.ReservedQuota)
	}
}

func TestSameKeyReturnsTheSameOrderWithoutTouchingOtherServices_SC04(t *testing.T) {
	f := newFixture()
	first, _, _ := place(t, f, lan, "key-1", "Q-1")
	// The quote expired meanwhile; a replay must still return the original order.
	f.quotes.quote.ExpiresAt = now.Add(-time.Minute)
	second, replayed, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil || !replayed || second.OrderID != first.OrderID {
		t.Fatalf("replay = %+v replayed=%v err=%v", second, replayed, err)
	}
	if f.stock.reserves != 1 || f.payments.calls != 1 || f.quotes.calls != 1 {
		t.Fatalf("calls after replay: stock=%d payments=%d quotes=%d, want 1 each", f.stock.reserves, f.payments.calls, f.quotes.calls)
	}
}

func TestSameKeyWithAnotherQuoteIsAConflict_SC04(t *testing.T) {
	f := newFixture()
	_, _, _ = place(t, f, lan, "key-1", "Q-1")
	_, _, err := place(t, f, lan, "key-1", "Q-2")
	if !errors.Is(err, orders.ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want conflict", err)
	}
	if f.stock.reserves != 1 {
		t.Fatalf("stock reserved %d times", f.stock.reserves)
	}
}

func TestLastUnitGoesToOneCustomer_SC03(t *testing.T) {
	f := newFixture()
	f.stock.reserveFn = func() error { return ErrOutOfStock }
	o, _, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != string(saga.StateFailedClosed) || o.FailureCode != "OUT_OF_STOCK" {
		t.Fatalf("order = %+v", o)
	}
	if f.payments.calls != 0 {
		t.Fatal("a payment attempt was created for an order without stock")
	}
	if f.stock.releases != 0 {
		t.Fatal("nothing was held, so nothing should be released from stock")
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ReservedQuota != 0 {
		t.Fatalf("voucher still reserved: %d", c.ReservedQuota)
	}
	// Replaying the failed key gives the same closed order.
	again, replayed, _ := place(t, f, lan, "key-1", "Q-1")
	if !replayed || again.FailureCode != "OUT_OF_STOCK" || f.stock.reserves != 1 {
		t.Fatalf("replay of failed order = %+v replayed=%v stock calls=%d", again, replayed, f.stock.reserves)
	}
}

func TestLastVoucherUseGoesToOneOrder_SC10(t *testing.T) {
	f := newFixture()
	q := sc01Quote()
	q.VoucherCode = "LASTONE10"
	f.quotes.quote = q
	first, _, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil || first.Status != string(saga.StateReserved) {
		t.Fatalf("first = %+v %v", first, err)
	}
	other := Principal{UserID: "user-minh", Bearer: "tok-minh"}
	q.UserID = "user-minh"
	f.quotes.quote = q
	second, _, err := place(t, f, other, "key-9", "Q-2")
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != string(saga.StateFailedClosed) || second.FailureCode != voucher.ReasonExhausted {
		t.Fatalf("second = %+v", second)
	}
	if f.stock.reserves != 1 || f.payments.calls != 1 {
		t.Fatalf("a refused voucher must stop before stock and payment: stock=%d payments=%d", f.stock.reserves, f.payments.calls)
	}
}

func TestPaymentAttemptFailureReleasesStockAndVoucher(t *testing.T) {
	f := newFixture()
	f.payments.failure = dependency.Wrap(errors.New("connection refused"))
	o, _, err := place(t, f, lan, "key-1", "Q-1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != string(saga.StateFailedClosed) || o.FailureCode != FailureDependencyUnavailable {
		t.Fatalf("order = %+v", o)
	}
	if f.stock.releases != 1 {
		t.Fatalf("stock releases = %d, want 1", f.stock.releases)
	}
	if c, _ := f.vouchers.Get(context.Background(), "WELCOME10"); c.ReservedQuota != 0 {
		t.Fatalf("voucher still reserved: %d", c.ReservedQuota)
	}
}

func TestStockOutageReleasesWhateverMayHaveBeenHeld(t *testing.T) {
	f := newFixture()
	f.stock.reserveFn = func() error { return dependency.Wrap(errors.New("timeout")) }
	o, _, _ := place(t, f, lan, "key-1", "Q-1")
	if o.Status != string(saga.StateFailedClosed) || o.FailureCode != FailureDependencyUnavailable {
		t.Fatalf("order = %+v", o)
	}
	// A timeout does not say whether the stock was reserved, so a release is attempted (it is idempotent).
	if f.stock.releases != 1 || f.payments.calls != 0 {
		t.Fatalf("releases=%d payments=%d", f.stock.releases, f.payments.calls)
	}
}

func TestQuoteProblemsCreateNoOrder(t *testing.T) {
	tests := map[string]struct {
		mutate func(*fixture)
		want   error
	}{
		"price changed": {func(f *fixture) { f.quotes.quote.Stale = true }, ErrQuoteChanged},
		"not found":     {func(f *fixture) { f.quotes.err = ErrQuoteNotFound }, ErrQuoteNotFound},
		"expired":       {func(f *fixture) { f.quotes.quote.ExpiresAt = now }, ErrQuoteExpired},
		"other owner":   {func(f *fixture) { f.quotes.quote.UserID = "user-minh" }, ErrQuoteNotFound},
		"bad total":     {func(f *fixture) { f.quotes.quote.TotalPayableVND = 650000 }, ErrQuoteInvalid},
		"no lines":      {func(f *fixture) { f.quotes.quote.Lines = nil }, ErrQuoteInvalid},
		"zero qty":      {func(f *fixture) { f.quotes.quote.Lines[0].Quantity = 0 }, ErrQuoteInvalid},
		"no address":    {func(f *fixture) { f.quotes.quote.AddressID = "" }, ErrQuoteInvalid},
		"free order":    {func(f *fixture) { f.quotes.quote.TotalPayableVND, f.quotes.quote.ShippingFeeVND = 0, -630000 }, ErrQuoteInvalid},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			tc.mutate(f)
			_, _, err := place(t, f, lan, "key-1", "Q-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if f.stock.reserves != 0 || f.payments.calls != 0 {
				t.Fatal("services were called for an unusable quote")
			}
			if _, found, _ := f.orders.Lookup(context.Background(), "user-lan", "key-1", orders.HashIntent("user-lan", "Q-1")); found {
				t.Fatal("an order was stored for an unusable quote")
			}
		})
	}
}

func TestConcurrentDuplicatesRunTheSagaOnce_SC04(t *testing.T) {
	f := newFixture()
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, err := place(t, f, lan, "key-1", "Q-1")
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
	if len(distinct) != 1 || f.stock.reserves != 1 || f.payments.calls != 1 {
		t.Fatalf("distinct orders=%d stock=%d payments=%d, want 1 each", len(distinct), f.stock.reserves, f.payments.calls)
	}
}
