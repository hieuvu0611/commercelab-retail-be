// Package peer adapts the other services' HTTP APIs to the interfaces the checkout service needs.
package peer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/svcclient"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
)

func trim(base string) string { return strings.TrimRight(base, "/") }

func hasCode(err error, status int, code string) bool {
	var apiErr *svcclient.APIError
	return errors.As(err, &apiErr) && apiErr.Status == status && apiErr.Code == code
}

// Quotes reads quotes from the catalog service.
type Quotes struct {
	base   string
	client *svcclient.Client
}

func NewQuotes(catalogURL string, c *svcclient.Client) *Quotes { return &Quotes{trim(catalogURL), c} }

type quoteView struct {
	IsStale bool `json:"isStale"`
	Quote   struct {
		QuoteID         string    `json:"quoteId"`
		UserID          string    `json:"userId"`
		AddressID       string    `json:"addressId"`
		VoucherCode     string    `json:"voucherCode"`
		SubtotalVND     int64     `json:"subtotalVnd"`
		DiscountVND     int64     `json:"discountVnd"`
		ShippingFeeVND  int64     `json:"shippingFeeVnd"`
		TotalPayableVND int64     `json:"totalPayableVnd"`
		ExpiresAt       time.Time `json:"expiresAt"`
		Lines           []struct {
			SkuID                string `json:"skuId"`
			SkuName              string `json:"skuName"`
			Quantity             int64  `json:"quantity"`
			UnitPriceVND         int64  `json:"unitPriceVnd"`
			LineDiscountVND      int64  `json:"lineDiscountVnd"`
			AllocatedUnitPaidVND int64  `json:"allocatedUnitPaidVnd"`
		} `json:"lines"`
	} `json:"quote"`
}

// Get reads quoteID with the caller's bearer token; the catalog answers 404 for another customer's quote.
func (q *Quotes) Get(ctx context.Context, bearer, quoteID string) (checkout.Quote, error) {
	var view quoteView
	_, err := q.client.Do(ctx, http.MethodGet, q.base+"/api/v1/quotes/"+url.PathEscape(quoteID), svcclient.Options{Bearer: bearer}, nil, &view)
	if hasCode(err, http.StatusNotFound, "QUOTE_NOT_FOUND") {
		return checkout.Quote{}, checkout.ErrQuoteNotFound
	}
	if err != nil {
		return checkout.Quote{}, err
	}
	src := view.Quote
	out := checkout.Quote{
		ID: src.QuoteID, UserID: src.UserID, AddressID: src.AddressID, VoucherCode: src.VoucherCode,
		SubtotalVND: src.SubtotalVND, DiscountVND: src.DiscountVND, ShippingFeeVND: src.ShippingFeeVND, TotalPayableVND: src.TotalPayableVND,
		ExpiresAt: src.ExpiresAt, Stale: view.IsStale,
	}
	for _, l := range src.Lines {
		out.Lines = append(out.Lines, checkout.QuoteLine{SkuID: l.SkuID, SkuName: l.SkuName, Quantity: l.Quantity, UnitPriceVND: l.UnitPriceVND,
			LineDiscountVND: l.LineDiscountVND, AllocatedUnitPaidVND: l.AllocatedUnitPaidVND})
	}
	return out, nil
}

// Stock calls the inventory service's internal reservation API.
type Stock struct {
	base   string
	client *svcclient.Client
}

func NewStock(inventoryURL string, c *svcclient.Client) *Stock { return &Stock{trim(inventoryURL), c} }

func (s *Stock) Reserve(ctx context.Context, orderID, warehouseID string, lines []checkout.StockLine) error {
	type line struct {
		SkuID    string `json:"skuId"`
		Quantity int64  `json:"quantity"`
	}
	wire := make([]line, len(lines))
	for i, l := range lines {
		wire[i] = line{SkuID: l.SkuID, Quantity: l.Quantity}
	}
	body := map[string]any{"orderId": orderID, "warehouseId": warehouseID, "lines": wire}
	_, err := s.client.Do(ctx, http.MethodPost, s.base+"/api/v1/inventory/reservations",
		svcclient.Options{Internal: true, IdempotencyKey: "reserve-" + orderID}, body, nil)
	if hasCode(err, http.StatusConflict, "INSUFFICIENT_STOCK") {
		return checkout.ErrOutOfStock
	}
	return err
}

// Release gives the reservation back. A reservation that does not exist (409) counts as released.
func (s *Stock) Release(ctx context.Context, orderID string) error {
	_, err := s.client.Do(ctx, http.MethodPost, s.base+"/api/v1/inventory/reservations/"+url.PathEscape(orderID)+"/release",
		svcclient.Options{Internal: true, IdempotencyKey: "release-" + orderID}, nil, nil)
	if hasCode(err, http.StatusConflict, "RELEASE_RESERVATION_FAILED") {
		return nil
	}
	return err
}

// Payments calls the payment service's internal API.
type Payments struct {
	base   string
	client *svcclient.Client
}

func NewPayments(paymentURL string, c *svcclient.Client) *Payments {
	return &Payments{trim(paymentURL), c}
}

func (p *Payments) CreateAttempt(ctx context.Context, orderID, userID string, amountVND int64) (checkout.Attempt, error) {
	var out struct {
		AttemptID string `json:"attemptId"`
		Status    string `json:"status"`
	}
	_, err := p.client.Do(ctx, http.MethodPost, p.base+"/api/v1/payments/attempts",
		svcclient.Options{Internal: true, IdempotencyKey: "pay-" + orderID},
		map[string]any{"orderId": orderID, "userId": userID, "amountVnd": amountVND}, &out)
	if err != nil {
		return checkout.Attempt{}, err
	}
	return checkout.Attempt{AttemptID: out.AttemptID, Status: out.Status}, nil
}

// Commit turns the order's reservation into committed stock. A reservation that cannot be committed (expired,
// released or missing) is ErrReservationLost; committing twice is fine (inventory answers 200).
func (s *Stock) Commit(ctx context.Context, orderID string) error {
	_, err := s.client.Do(ctx, http.MethodPost, s.base+"/api/v1/inventory/reservations/"+url.PathEscape(orderID)+"/commit",
		svcclient.Options{Internal: true, IdempotencyKey: "commit-" + orderID}, nil, nil)
	if hasCode(err, http.StatusConflict, "COMMIT_RESERVATION_FAILED") {
		return checkout.ErrReservationLost
	}
	return err
}

// ExceptionHold extends the reservation to the 30-minute hold while the payment outcome is unknown.
func (s *Stock) ExceptionHold(ctx context.Context, orderID string) error {
	_, err := s.client.Do(ctx, http.MethodPost, s.base+"/api/v1/inventory/reservations/"+url.PathEscape(orderID)+"/exception-hold",
		svcclient.Options{Internal: true, IdempotencyKey: "hold-" + orderID}, nil, nil)
	if hasCode(err, http.StatusNotFound, "RESERVATION_NOT_FOUND") || hasCode(err, http.StatusConflict, "RESERVATION_EXPIRED") ||
		hasCode(err, http.StatusConflict, "EXCEPTION_HOLD_FAILED") {
		return checkout.ErrReservationLost
	}
	return err
}

// CloseAttempt tells the payment service the order is closed. An attempt the payment service does not know
// (the creation call never arrived) counts as closed.
func (p *Payments) CloseAttempt(ctx context.Context, attemptID string) error {
	_, err := p.client.Do(ctx, http.MethodPost, p.base+"/api/v1/payments/attempts/"+url.PathEscape(attemptID)+"/close",
		svcclient.Options{Internal: true, IdempotencyKey: "close-" + attemptID}, nil, nil)
	if hasCode(err, http.StatusNotFound, "PAYMENT_NOT_FOUND") {
		return nil
	}
	return err
}

// Carts calls the catalog service's internal cart route.
type Carts struct {
	base   string
	client *svcclient.Client
}

func NewCarts(catalogURL string, c *svcclient.Client) *Carts { return &Carts{trim(catalogURL), c} }

// RemoveOrdered removes the purchased SKUs from the customer's cart. Removing what is not there is fine.
func (c *Carts) RemoveOrdered(ctx context.Context, userID string, skuIDs []string) error {
	_, err := c.client.Do(ctx, http.MethodPost, c.base+"/api/v1/carts/internal/ordered-items",
		svcclient.Options{Internal: true, IdempotencyKey: "ordered-" + digest(userID, skuIDs)},
		map[string]any{"userId": userID, "skuIds": skuIDs}, nil)
	return err
}

// digest is a short stable name for "these SKUs of this customer", used as the idempotency key of the cleanup.
func digest(userID string, skuIDs []string) string {
	sum := sha256.Sum256([]byte(userID + "|" + strings.Join(skuIDs, ",")))
	return hex.EncodeToString(sum[:8])
}
