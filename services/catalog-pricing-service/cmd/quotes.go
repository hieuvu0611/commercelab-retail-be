package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/cart"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/domain"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/peer"
	"github.com/hieuvu0611/commercelab-retail-be/services/catalog-pricing-service/internal/repository"
)

const maxVoucherCodeLength = 64

// VoucherChecker asks the order service whether the token's owner can use a voucher. It reserves nothing.
type VoucherChecker interface {
	Eligibility(ctx context.Context, bearer, code string) (peer.Eligibility, error)
}

// AddressChecker asks the IAM service whether an address is in the token owner's address book.
type AddressChecker interface {
	Exists(ctx context.Context, bearer, addressID string) (bool, error)
}

type quoteStore interface {
	SaveQuote(ctx context.Context, q *domain.Quote) error
	GetQuoteByID(ctx context.Context, quoteID string) (*domain.Quote, error)
}

type quoteAPI struct {
	store     quoteStore
	cart      *cart.Service
	pricer    cart.Pricer
	vouchers  VoucherChecker
	addresses AddressChecker
	now       func() time.Time
}

func registerQuoteRoutes(mux *http.ServeMux, store quoteStore, carts *cart.Service, pricer cart.Pricer, vouchers VoucherChecker, addresses AddressChecker) {
	api := &quoteAPI{store: store, cart: carts, pricer: pricer, vouchers: vouchers, addresses: addresses, now: func() time.Time { return time.Now().UTC() }}
	mux.Handle("POST /api/v1/quotes", auth.RequireAuth(http.HandlerFunc(api.create)))
	mux.Handle("GET /api/v1/quotes/{quoteId}", auth.RequireAuth(http.HandlerFunc(api.get)))
}

// create prices the caller's cart. Identity comes from the token and every price from the catalog: the body
// carries only the delivery address and an optional voucher code, and any other field is rejected.
func (a *quoteAPI) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AddressID   string `json:"addressId"`
		VoucherCode string `json:"voucherCode"`
	}
	if !request.DecodeJSON(w, r, &body) {
		return
	}
	addressID, code := strings.TrimSpace(body.AddressID), strings.ToUpper(strings.TrimSpace(body.VoucherCode))
	fieldErrors := map[string][]string{}
	if addressID == "" || len(addressID) > 64 {
		fieldErrors["addressId"] = []string{"addressId is required"}
	}
	if len(code) > maxVoucherCodeLength {
		fieldErrors["voucherCode"] = []string{"voucherCode is too long"}
	}
	if len(fieldErrors) > 0 {
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", fieldErrors)
		return
	}
	claims, _ := auth.ClaimsFrom(r.Context())
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))

	lines, ok := a.cartLines(w, r, claims.UserID)
	if !ok {
		return
	}
	if !a.addressBelongsToCaller(w, r, bearer, addressID) {
		return
	}
	discountPct, ok := a.voucherDiscount(w, r, bearer, code)
	if !ok {
		return
	}

	quote, err := domain.CalculateQuote(newQuoteID(), claims.UserID, code, discountPct, lines, a.now())
	if err != nil {
		log.Printf("[catalog] quote calculation failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
		return
	}
	quote.AddressID = addressID
	if err := a.store.SaveQuote(r.Context(), quote); err != nil {
		writeQuoteError(w, r, err)
		return
	}
	response.JSON(w, http.StatusCreated, "Quote created", quote, nil)
}

// cartLines prices the cart. It writes the error response itself and returns false when the cart cannot be quoted.
func (a *quoteAPI) cartLines(w http.ResponseWriter, r *http.Request, userID string) ([]domain.QuoteLineInput, bool) {
	view, err := a.cart.View(r.Context(), userID)
	if err != nil {
		writeQuoteError(w, r, err)
		return nil, false
	}
	if len(view.Items) == 0 {
		response.Error(w, r, http.StatusConflict, "CART_EMPTY", "The cart is empty", nil)
		return nil, false
	}
	lines := make([]domain.QuoteLineInput, 0, len(view.Items))
	var closed []string
	for _, item := range view.Items {
		if !item.Orderable {
			closed = append(closed, item.SkuID)
			continue
		}
		lines = append(lines, domain.QuoteLineInput{SkuID: item.SkuID, SkuName: item.SkuName, Quantity: item.Quantity, UnitPriceVND: item.UnitPriceVND})
	}
	if len(closed) > 0 {
		response.Error(w, r, http.StatusConflict, "SKU_NOT_ORDERABLE", "Some items in the cart cannot be ordered right now",
			map[string][]string{"items": closed})
		return nil, false
	}
	return lines, true
}

func (a *quoteAPI) addressBelongsToCaller(w http.ResponseWriter, r *http.Request, bearer, addressID string) bool {
	if a.addresses == nil {
		dependency.WriteUnavailable(w, r, dependency.Wrap(errors.New("address checker is not configured")))
		return false
	}
	exists, err := a.addresses.Exists(r.Context(), bearer, addressID)
	if err != nil {
		writeQuoteError(w, r, err)
		return false
	}
	if !exists {
		response.Error(w, r, http.StatusUnprocessableEntity, "ADDRESS_NOT_FOUND", "The delivery address does not exist", nil)
		return false
	}
	return true
}

// voucherDiscount returns the discount percentage of the voucher (0 without a code). An ineligible voucher is a
// 422 whose code is the reason (VOUCHER_EXPIRED, VOUCHER_EXHAUSTED, ...).
func (a *quoteAPI) voucherDiscount(w http.ResponseWriter, r *http.Request, bearer, code string) (int64, bool) {
	if code == "" {
		return 0, true
	}
	if a.vouchers == nil {
		dependency.WriteUnavailable(w, r, dependency.Wrap(errors.New("voucher checker is not configured")))
		return 0, false
	}
	result, err := a.vouchers.Eligibility(r.Context(), bearer, code)
	if err != nil {
		writeQuoteError(w, r, err)
		return 0, false
	}
	if !result.Eligible {
		reason := result.Reason
		if reason == "" {
			reason = "VOUCHER_NOT_ELIGIBLE"
		}
		response.Error(w, r, http.StatusUnprocessableEntity, reason, "The voucher cannot be used", nil)
		return 0, false
	}
	return result.DiscountPct, true
}

// get returns one of the caller's own quotes; another user's quote looks missing.
func (a *quoteAPI) get(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.ClaimsFrom(r.Context())
	quote, err := a.store.GetQuoteByID(r.Context(), r.PathValue("quoteId"))
	if errors.Is(err, repository.ErrQuoteNotFound) || (err == nil && quote.UserID != claims.UserID) {
		response.Error(w, r, http.StatusNotFound, "QUOTE_NOT_FOUND", "Quote not found", nil)
		return
	}
	if err != nil {
		writeQuoteError(w, r, err)
		return
	}
	stale, err := a.staleSkus(r.Context(), quote)
	if err != nil {
		writeQuoteError(w, r, err)
		return
	}
	response.JSON(w, http.StatusOK, "Quote", map[string]any{
		"quote":       quote,
		"isExpired":   !a.now().Before(quote.ExpiresAt),
		"isStale":     len(stale) > 0,
		"staleSkuIds": stale,
	}, nil)
}

// staleSkus lists the quote's SKUs whose price changed or that cannot be ordered any more since the quote was
// made. A stale quote must be accepted again before an order is placed from it (P02).
func (a *quoteAPI) staleSkus(ctx context.Context, q *domain.Quote) ([]string, error) {
	stale := []string{}
	for _, l := range q.Lines {
		sp, err := a.pricer.GetSkuPrice(ctx, l.SkuID)
		switch {
		case errors.Is(err, repository.ErrSkuNotFound), errors.Is(err, repository.ErrSkuNotOrderable):
			stale = append(stale, l.SkuID)
		case err != nil:
			return nil, err
		case sp.UnitPriceVND != l.UnitPriceVND:
			stale = append(stale, l.SkuID)
		}
	}
	return stale, nil
}

func newQuoteID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the OS random source failing is not recoverable
	}
	return "Q-" + hex.EncodeToString(b[:])
}

func writeQuoteError(w http.ResponseWriter, r *http.Request, err error) {
	if dependency.IsUnavailable(err) {
		dependency.WriteUnavailable(w, r, err)
		return
	}
	log.Printf("[catalog] quote request failed: %v", err)
	response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
}
