package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth/authtest"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cart"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/peer"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

// fakeAddresses knows which addresses each user owns; the owner is read from the forwarded token, like IAM does.
type fakeAddresses struct {
	owned map[string][]string
	down  bool
}

func (f *fakeAddresses) Exists(_ context.Context, bearer, addressID string) (bool, error) {
	if f.down {
		return false, dependency.Wrap(context.DeadlineExceeded)
	}
	claims, err := auth.ParseAccessToken(bearer)
	if err != nil {
		return false, nil
	}
	for _, id := range f.owned[claims.UserID] {
		if id == addressID {
			return true, nil
		}
	}
	return false, nil
}

type fakeVouchers struct {
	byCode map[string]peer.Eligibility
	down   bool
	asked  []string
}

func (f *fakeVouchers) Eligibility(_ context.Context, _, code string) (peer.Eligibility, error) {
	f.asked = append(f.asked, code)
	if f.down {
		return peer.Eligibility{}, dependency.Wrap(context.DeadlineExceeded)
	}
	if e, ok := f.byCode[code]; ok {
		return e, nil
	}
	return peer.Eligibility{Code: code, Reason: "VOUCHER_NOT_FOUND"}, nil
}

type quoteEnv struct {
	srv      http.Handler
	repo     *repository.CatalogPricingRepository
	carts    *cart.MemoryStore
	vouchers *fakeVouchers
	addrs    *fakeAddresses
}

func newQuoteEnv(t *testing.T) *quoteEnv {
	t.Helper()
	authtest.Configure(t)
	e := &quoteEnv{
		repo:  repository.NewCatalogPricingRepository(nil, nil),
		carts: cart.NewMemoryStore(),
		vouchers: &fakeVouchers{byCode: map[string]peer.Eligibility{
			"WELCOME10": {Code: "WELCOME10", Eligible: true, DiscountPct: 10},
			"OLD10":     {Code: "OLD10", Reason: "VOUCHER_EXPIRED"},
			"LASTONE10": {Code: "LASTONE10", Reason: "VOUCHER_EXHAUSTED"},
		}},
		addrs: &fakeAddresses{owned: map[string][]string{"lan": {"addr-lan"}, "bob": {"addr-bob"}}},
	}
	e.srv = NewServerWithDeps(e.repo, false, false, Deps{Carts: e.carts, Vouchers: e.vouchers, Addresses: e.addrs})
	return e
}

func (e *quoteEnv) fillCart(t *testing.T, user string, lines map[string]int64) {
	t.Helper()
	if err := e.carts.SetMany(context.Background(), user, lines); err != nil {
		t.Fatal(err)
	}
}

func (e *quoteEnv) post(t *testing.T, user, body string) (int, string) {
	t.Helper()
	rec := cartDo(e.srv, http.MethodPost, "/api/v1/quotes", cartToken(t, user), body)
	return rec.Code, rec.Body.String()
}

type quoteResp struct {
	Data domain.Quote `json:"data"`
}

func TestQuoteFromCartSC01Is660000(t *testing.T) {
	e := newQuoteEnv(t)
	e.fillCart(t, "lan", map[string]int64{skuBackpack: 2, skuBottle: 1})
	before := time.Now().UTC()

	rec := cartDo(e.srv, http.MethodPost, "/api/v1/quotes", cartToken(t, "lan"), `{"addressId":"addr-lan","voucherCode":" welcome10 "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	q := decode[quoteResp](t, rec).Data
	if q.SubtotalVND != 700000 || q.DiscountVND != 70000 || q.ShippingFeeVND != 30000 || q.TotalPayableVND != 660000 {
		t.Fatalf("totals = %+v", q)
	}
	if q.UserID != "lan" || q.AddressID != "addr-lan" || q.VoucherCode != "WELCOME10" || q.DiscountPct != 10 || q.Version == "" {
		t.Fatalf("identity fields = %+v", q)
	}
	discounts := map[string]int64{}
	for _, l := range q.Lines {
		discounts[l.SkuID] = l.LineDiscountVND
	}
	if discounts[skuBackpack] != 60000 || discounts[skuBottle] != 10000 {
		t.Fatalf("allocation = %v; want 60,000 / 10,000", discounts)
	}
	if ttl := q.ExpiresAt.Sub(before); ttl < 4*time.Minute+50*time.Second || ttl > 5*time.Minute+5*time.Second {
		t.Fatalf("expiresAt is %v after the request; want 5 minutes", ttl)
	}
	if got := e.vouchers.asked; len(got) != 1 || got[0] != "WELCOME10" {
		t.Fatalf("voucher asked = %v", got)
	}

	// The cart is not consumed and the saved quote reads back for its owner.
	if cartView := decode[cartResp](t, cartDo(e.srv, http.MethodGet, "/api/v1/carts/me", cartToken(t, "lan"), "")).Data; cartView.ItemCount != 3 {
		t.Fatalf("a quote must not change the cart: %+v", cartView)
	}
	get := cartDo(e.srv, http.MethodGet, "/api/v1/quotes/"+q.QuoteID, cartToken(t, "lan"), "")
	read := decode[struct {
		Data struct {
			Quote     domain.Quote `json:"quote"`
			IsExpired bool         `json:"isExpired"`
		} `json:"data"`
	}](t, get)
	if get.Code != http.StatusOK || read.Data.Quote.TotalPayableVND != 660000 || read.Data.IsExpired || read.Data.Quote.Version != q.Version {
		t.Fatalf("get: %d %s", get.Code, get.Body)
	}
}

func TestQuoteRejectsClientIdentityAndPrices(t *testing.T) {
	e := newQuoteEnv(t)
	e.fillCart(t, "lan", map[string]int64{skuBackpack: 1})
	for name, body := range map[string]string{
		"userId":    `{"addressId":"addr-lan","userId":"bob"}`,
		"items":     `{"addressId":"addr-lan","items":[{"skuId":"` + skuBackpack + `","quantity":1,"unitPriceVnd":1}]}`,
		"price":     `{"addressId":"addr-lan","unitPriceVnd":1}`,
		"quoteId":   `{"addressId":"addr-lan","quoteId":"Q-MINE"}`,
		"not json":  `[]`,
		"no addres": `{}`,
	} {
		if code, resp := e.post(t, "lan", body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, code, resp)
		}
	}
	// The quote is priced for the token's user whatever an identity header says.
	req := newJSONRequest(http.MethodPost, "/api/v1/quotes", `{"addressId":"addr-lan"}`)
	req.Header.Set("Authorization", "Bearer "+cartToken(t, "lan"))
	req.Header.Set("X-User-ID", "bob")
	if rec := serve(e.srv, req); rec.Code != http.StatusCreated || decode[quoteResp](t, rec).Data.UserID != "lan" {
		t.Fatalf("X-User-ID: %d %s", rec.Code, rec.Body)
	}
}

func TestQuoteRequiresAuthentication(t *testing.T) {
	e := newQuoteEnv(t)
	for _, c := range []struct{ method, path string }{{http.MethodPost, "/api/v1/quotes"}, {http.MethodGet, "/api/v1/quotes/Q-1"}} {
		if rec := cartDo(e.srv, c.method, c.path, "", `{"addressId":"addr-lan"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", c.method, rec.Code)
		}
	}
}

func TestQuoteShippingIsFreeFromOneMillionNetMerchandise(t *testing.T) {
	e := newQuoteEnv(t)
	// 4 x 300,000 = 1,200,000: free shipping without a voucher, and still free after the 10% voucher (1,080,000).
	e.fillCart(t, "lan", map[string]int64{skuBackpack: 4})
	code, body := e.post(t, "lan", `{"addressId":"addr-lan"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"shippingFeeVnd":0`) || !strings.Contains(body, `"totalPayableVnd":1200000`) {
		t.Fatalf("no voucher: %d %s", code, body)
	}
	code, body = e.post(t, "lan", `{"addressId":"addr-lan","voucherCode":"WELCOME10"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"shippingFeeVnd":0`) || !strings.Contains(body, `"totalPayableVnd":1080000`) {
		t.Fatalf("with voucher: %d %s", code, body)
	}
	// 3 x 300,000 + 100,000 = 1,000,000 gross, but 900,000 after the voucher: shipping applies (discounted merchandise).
	e.fillCart(t, "lan", map[string]int64{skuBackpack: 3, skuBottle: 1})
	code, body = e.post(t, "lan", `{"addressId":"addr-lan","voucherCode":"WELCOME10"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"shippingFeeVnd":30000`) || !strings.Contains(body, `"totalPayableVnd":930000`) {
		t.Fatalf("discounted below the threshold: %d %s", code, body)
	}
}

func TestQuoteErrors(t *testing.T) {
	e := newQuoteEnv(t)
	tests := []struct {
		name   string
		setup  func()
		user   string
		body   string
		status int
		code   string
	}{
		{"empty cart", func() {}, "lan", `{"addressId":"addr-lan"}`, 409, "CART_EMPTY"},
		{"address of another user", func() { e.fillCart(t, "lan", map[string]int64{skuBackpack: 1}) }, "lan", `{"addressId":"addr-bob"}`, 422, "ADDRESS_NOT_FOUND"},
		{"unknown address", func() {}, "lan", `{"addressId":"addr-nope"}`, 422, "ADDRESS_NOT_FOUND"},
		{"expired voucher", func() {}, "lan", `{"addressId":"addr-lan","voucherCode":"OLD10"}`, 422, "VOUCHER_EXPIRED"},
		{"exhausted voucher", func() {}, "lan", `{"addressId":"addr-lan","voucherCode":"LASTONE10"}`, 422, "VOUCHER_EXHAUSTED"},
		{"unknown voucher", func() {}, "lan", `{"addressId":"addr-lan","voucherCode":"NOPE"}`, 422, "VOUCHER_NOT_FOUND"},
		{"voucher service down", func() { e.vouchers.down = true }, "lan", `{"addressId":"addr-lan","voucherCode":"WELCOME10"}`, 503, "DEPENDENCY_UNAVAILABLE"},
		{"address service down", func() { e.vouchers.down = false; e.addrs.down = true }, "lan", `{"addressId":"addr-lan"}`, 503, "DEPENDENCY_UNAVAILABLE"},
	}
	for _, tc := range tests {
		tc.setup()
		code, body := e.post(t, tc.user, tc.body)
		if code != tc.status || !strings.Contains(body, `"code":"`+tc.code+`"`) {
			t.Fatalf("%s: %d %s", tc.name, code, body)
		}
	}
}

func TestQuoteRejectsACartWithAClosedSku(t *testing.T) {
	e := newQuoteEnv(t)
	e.fillCart(t, "lan", map[string]int64{skuBackpack: 1, skuBottle: 1})
	if err := e.repo.SetOrderable(context.Background(), "PROD-BOTTLE-ECO", nil); err != nil {
		t.Fatal(err)
	}
	rec := cartDo(e.srv, http.MethodPost, "/api/v1/quotes", cartToken(t, "lan"), `{"addressId":"addr-lan"}`)
	got := decode[errorBody](t, rec)
	if rec.Code != http.StatusConflict || got.Code != "SKU_NOT_ORDERABLE" || len(got.Errors["items"]) != 1 || got.Errors["items"][0] != skuBottle {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestQuotesAreOwnedByTheirCustomerAndReportExpiry(t *testing.T) {
	e := newQuoteEnv(t)
	e.fillCart(t, "lan", map[string]int64{skuBackpack: 1})
	_, body := e.post(t, "lan", `{"addressId":"addr-lan"}`)
	id := decode[quoteResp](t, httpRec(http.StatusCreated, []byte(body))).Data.QuoteID

	if rec := cartDo(e.srv, http.MethodGet, "/api/v1/quotes/"+id, cartToken(t, "bob"), ""); rec.Code != http.StatusNotFound || decode[errorBody](t, rec).Code != "QUOTE_NOT_FOUND" {
		t.Fatalf("another user's quote: %d %s", rec.Code, rec.Body)
	}
	if rec := cartDo(e.srv, http.MethodGet, "/api/v1/quotes/Q-NOPE", cartToken(t, "lan"), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown quote: %d", rec.Code)
	}

	stale := domain.Quote{QuoteID: "Q-STALE", UserID: "lan", ExpiresAt: time.Now().UTC().Add(-time.Second)}
	if err := e.repo.SaveQuote(context.Background(), &stale); err != nil {
		t.Fatal(err)
	}
	rec := cartDo(e.srv, http.MethodGet, "/api/v1/quotes/Q-STALE", cartToken(t, "lan"), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isExpired":true`) {
		t.Fatalf("expired quote: %d %s", rec.Code, rec.Body)
	}
}
