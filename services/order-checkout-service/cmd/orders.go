package main

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/contracts/ids"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
)

const maxQuoteIDLength = 64

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func registerOrderRoutes(mux *http.ServeMux, svc *checkout.Service) {
	mux.Handle("POST /api/v1/orders", auth.RequireAuth(auth.RequireRole("CUSTOMER")(http.HandlerFunc(placeOrder(svc)))))
}

// orderView is the order as the customer sees it.
type orderView struct {
	orders.Order
	PaymentAttemptID string `json:"paymentAttemptId,omitempty"`
}

func viewOf(o orders.Order) orderView {
	v := orderView{Order: o}
	if o.Status != string(saga.StateFailedClosed) {
		v.PaymentAttemptID = ids.PaymentAttemptID(o.OrderID)
	}
	return v
}

// placeOrder: identity and token come from the verified access token; the body names only the accepted quote and
// every price, line and address is read from the stored quote (B03, I02).
func placeOrder(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			response.Error(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "The Idempotency-Key header is required", nil)
			return
		}
		if !idempotencyKeyPattern.MatchString(key) {
			response.Error(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "The Idempotency-Key must be 1 to 128 characters: letters, digits, . _ : -", nil)
			return
		}
		var body struct {
			QuoteID string `json:"quoteId"`
		}
		if !request.DecodeJSON(w, r, &body) {
			return
		}
		quoteID := strings.TrimSpace(body.QuoteID)
		if quoteID == "" || len(quoteID) > maxQuoteIDLength {
			response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", map[string][]string{"quoteId": {"quoteId is required"}})
			return
		}
		claims, _ := auth.ClaimsFrom(r.Context())
		bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))

		order, replayed, err := svc.Place(r.Context(), checkout.Principal{UserID: claims.UserID, Bearer: bearer}, key, quoteID)
		if err != nil {
			writePlaceError(w, r, err)
			return
		}
		if order.Status == string(saga.StateFailedClosed) && order.FailureCode != "" {
			writeFailure(w, r, order.FailureCode)
			return
		}
		status, message := http.StatusCreated, "Order placed. Complete the payment before the reservation expires."
		if replayed {
			status, message = http.StatusOK, "Order already placed with this Idempotency-Key"
		}
		response.JSON(w, status, message, viewOf(order), nil)
	}
}

func writePlaceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, orders.ErrIdempotencyConflict):
		response.Error(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "This Idempotency-Key was already used for a different order", nil)
	case errors.Is(err, checkout.ErrQuoteNotFound):
		response.Error(w, r, http.StatusNotFound, "QUOTE_NOT_FOUND", "Quote not found", nil)
	case errors.Is(err, checkout.ErrQuoteExpired):
		response.Error(w, r, http.StatusConflict, "QUOTE_EXPIRED", "The quote has expired; review the order again to get a new one", nil)
	case errors.Is(err, checkout.ErrQuoteInvalid):
		response.Error(w, r, http.StatusConflict, "QUOTE_INVALID", "The quote cannot be ordered; review the order again to get a new one", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[order] place order failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}

// writeFailure answers for an order that was closed during placement. The same answer is given again when the
// same Idempotency-Key is replayed.
func writeFailure(w http.ResponseWriter, r *http.Request, code string) {
	switch code {
	case checkout.FailureOutOfStock:
		response.Error(w, r, http.StatusConflict, "OUT_OF_STOCK", "Some items are no longer in stock", nil)
	case checkout.FailureDependencyUnavailable:
		dependency.WriteUnavailable(w, r, dependency.Wrap(errors.New("a service needed to place the order is unavailable")))
	case voucher.ReasonNotFound, voucher.ReasonNotActive, voucher.ReasonNotStarted, voucher.ReasonExpired, voucher.ReasonExhausted, voucher.ReasonLimitReached:
		response.Error(w, r, http.StatusUnprocessableEntity, code, "The voucher cannot be used", nil)
	default:
		log.Printf("[order] closed order with unknown failure code %q", code)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
