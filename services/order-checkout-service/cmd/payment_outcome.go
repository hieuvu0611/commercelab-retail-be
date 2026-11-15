package main

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/request"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/saga"
)

const maxStatusLength = 32

// registerPaymentOutcomeRoute serves the payment service's callback. Only services holding the internal token
// may call it: the expected amount is the order's own total and the body carries no price.
func registerPaymentOutcomeRoute(mux *http.ServeMux, svc *checkout.Service) {
	mux.Handle("POST /api/v1/orders/{orderId}/payment-outcome", internalauth.Require(http.HandlerFunc(paymentOutcome(svc))))
}

func paymentOutcome(svc *checkout.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AttemptID         string `json:"attemptId"`
			Status            string `json:"status"`
			CapturedAmountVND int64  `json:"capturedAmountVnd"`
		}
		if !request.DecodeJSON(w, r, &body) {
			return
		}
		body.AttemptID, body.Status = strings.TrimSpace(body.AttemptID), strings.TrimSpace(body.Status)
		if body.AttemptID == "" || body.Status == "" || len(body.Status) > maxStatusLength || body.CapturedAmountVND < 0 {
			response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The request has invalid fields", nil)
			return
		}
		o, err := svc.ApplyPayment(r.Context(), r.PathValue("orderId"), checkout.PaymentReport{
			AttemptID: body.AttemptID, Status: body.Status, CapturedVND: body.CapturedAmountVND,
		})
		if err != nil {
			writePaymentOutcomeError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, "Payment outcome applied", map[string]any{
			"orderId": o.OrderID, "status": o.Status, "paymentStatus": o.PaymentStatus,
		}, nil)
	}
}

func writePaymentOutcomeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, orders.ErrNotFound):
		response.Error(w, r, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found", nil)
	case errors.Is(err, checkout.ErrAttemptMismatch):
		response.Error(w, r, http.StatusConflict, "PAYMENT_ATTEMPT_MISMATCH", "The payment attempt does not belong to this order", nil)
	case errors.Is(err, checkout.ErrUnknownPaymentStatus):
		response.Error(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "The payment status is not known", map[string][]string{"status": {"unknown payment status"}})
	case errors.Is(err, saga.ErrInvalidTransition):
		response.Error(w, r, http.StatusConflict, "PAYMENT_OUTCOME_CONFLICT", "The payment outcome does not fit the order's state", nil)
	case errors.Is(err, orders.ErrStaleState):
		response.Error(w, r, http.StatusConflict, "ORDER_STATE_CHANGED", "The order changed while the outcome was applied; retry", nil)
	case dependency.IsUnavailable(err):
		dependency.WriteUnavailable(w, r, err)
	default:
		log.Printf("[order] payment outcome failed: %v", err)
		response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
